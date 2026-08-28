package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orron/pano/internal/bus"
	"github.com/orron/pano/internal/flow"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned by lookups for ids that are not in the store.
var ErrNotFound = errors.New("store: not found")

// ErrClosed is returned by operations attempted after Close.
var ErrClosed = errors.New("store: closed")

// SQLiteOptions configures OpenSQLite. Zero fields take the documented
// defaults.
type SQLiteOptions struct {
	// Path is the database file. Parent directories must exist.
	Path string
	// BatchSize is the maximum number of queued items written per transaction
	// (default 256).
	BatchSize int
	// BatchDelay is how long the writer waits to fill a batch after the first
	// item arrives (default 50ms).
	BatchDelay time.Duration
	// QueueSize bounds the write-behind queue (default 8192). When the queue is
	// full new items are dropped and counted; ingestion never blocks.
	QueueSize int
	// FTSTextCap is the maximum number of decoded body bytes indexed for
	// full-text search per body (default 256 KiB).
	FTSTextCap int
	// Logger receives writer errors. Nil means slog.Default().
	Logger *slog.Logger
}

func (o *SQLiteOptions) defaults() {
	if o.BatchSize <= 0 {
		o.BatchSize = 256
	}
	if o.BatchDelay <= 0 {
		o.BatchDelay = 50 * time.Millisecond
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 8192
	}
	if o.FTSTextCap <= 0 {
		o.FTSTextCap = 256 << 10
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// SQLite is a write-behind persistent flow store with full-text search.
//
// Writes (Enqueue, PutBlob) are queued and applied by a single writer
// goroutine in batched transactions; they never block the caller. Reads go
// to a small pool of read-only connections and observe committed batches.
type SQLite struct {
	opts SQLiteOptions
	log  *slog.Logger

	w *sql.DB // single writer connection
	r *sql.DB // read-only pool

	queue     chan wqItem
	closeMu   sync.RWMutex // guards closed + queue close
	closed    bool
	writerWG  sync.WaitGroup
	done      chan struct{} // closed when Close begins
	dropped   atomic.Int64
	writeErrs atomic.Int64
	batches   atomic.Int64

	subsMu sync.Mutex
	subs   []*bus.Subscriber
	subsWG sync.WaitGroup

	// Owned by the writer goroutine: flows indexed with a missing textual
	// blob, keyed by blob hash, so a late blob can trigger re-indexing.
	pendingText map[string][]flow.ID
}

// OpenSQLite opens (creating if needed) the database at opts.Path, applies
// embedded migrations, and starts the writer goroutine. Flows left in state
// active/held by a previous process are marked failed ("daemon restarted").
func OpenSQLite(opts SQLiteOptions) (*SQLite, error) {
	opts.defaults()
	if opts.Path == "" {
		return nil, errors.New("store: sqlite: Path is required")
	}
	if _, err := os.Stat(opts.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("store: sqlite: stat: %w", err)
	}

	w, err := sql.Open("sqlite", writerDSN(opts.Path))
	if err != nil {
		return nil, fmt.Errorf("store: sqlite: open: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)

	s := &SQLite{
		opts:        opts,
		log:         opts.Logger,
		w:           w,
		queue:       make(chan wqItem, opts.QueueSize),
		done:        make(chan struct{}),
		pendingText: make(map[string][]flow.ID),
	}
	if err := s.setup(); err != nil {
		_ = w.Close()
		return nil, err
	}

	r, err := sql.Open("sqlite", readerDSN(opts.Path))
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("store: sqlite: open reader: %w", err)
	}
	r.SetMaxOpenConns(4)
	r.SetMaxIdleConns(4)
	r.SetConnMaxLifetime(0)
	if err := r.PingContext(context.Background()); err != nil {
		_ = w.Close()
		_ = r.Close()
		return nil, fmt.Errorf("store: sqlite: ping reader: %w", err)
	}
	s.r = r

	s.writerWG.Add(1)
	go s.writerLoop()
	return s, nil
}

func writerDSN(path string) string {
	return "file:" + path + "?" + url.Values{
		"_txlock": {"immediate"},
		"_pragma": {
			"busy_timeout(5000)",
			"journal_mode(WAL)",
			"synchronous(NORMAL)",
			"foreign_keys(ON)",
			"temp_store(MEMORY)",
			"mmap_size(268435456)",
		},
	}.Encode()
}

func readerDSN(path string) string {
	return "file:" + path + "?" + url.Values{
		"mode": {"ro"},
		"_pragma": {
			"busy_timeout(5000)",
			"foreign_keys(ON)",
			"temp_store(MEMORY)",
			"mmap_size(268435456)",
		},
	}.Encode()
}

// setup runs pragmas that must precede table creation, applies migrations,
// and repairs state left behind by an unclean shutdown.
func (s *SQLite) setup() error {
	ctx := context.Background()
	if err := s.ensureIncrementalVacuum(ctx); err != nil {
		return err
	}
	if err := s.migrate(ctx); err != nil {
		return err
	}
	return s.failOrphans(ctx)
}

// ensureIncrementalVacuum switches a fresh database to auto_vacuum=INCREMENTAL.
// The setting is baked into the file header, which the connection pragmas
// (journal_mode) have already written, so a VACUUM is needed to apply it;
// that is instant while the database has no tables and is skipped once it
// has any, since existing databases were created by this same code.
func (s *SQLite) ensureIncrementalVacuum(ctx context.Context) error {
	var mode, objects int
	if err := s.w.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return fmt.Errorf("store: sqlite: auto_vacuum: %w", err)
	}
	if mode == 2 {
		return nil
	}
	if err := s.w.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master").Scan(&objects); err != nil {
		return fmt.Errorf("store: sqlite: auto_vacuum: %w", err)
	}
	if objects > 0 {
		s.log.Warn("sqlite: database was created without incremental auto_vacuum; Prune cannot shrink the file")
		return nil
	}
	if _, err := s.w.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL; VACUUM"); err != nil {
		return fmt.Errorf("store: sqlite: auto_vacuum: %w", err)
	}
	return nil
}

func (s *SQLite) migrate(ctx context.Context) error {
	if _, err := s.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version(
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: sqlite: schema_version: %w", err)
	}
	applied := map[int]bool{}
	rows, err := s.w.QueryContext(ctx, "SELECT version FROM schema_version")
	if err != nil {
		return fmt.Errorf("store: sqlite: read versions: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return err
		}
		applied[v] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: sqlite: read versions: %w", err)
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: sqlite: migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		ver, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if applied[ver] {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.w.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: sqlite: migrate %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: sqlite: migrate %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version(version, applied_at) VALUES(?, ?)",
			ver, time.Now().UnixMicro()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: sqlite: migrate %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: sqlite: migrate %s: %w", name, err)
		}
	}
	return nil
}

// migrationVersion parses the leading integer of "0001_init.sql".
func migrationVersion(name string) (int, error) {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("store: sqlite: migration %q has no numeric prefix", name)
	}
	return strconv.Atoi(name[:i])
}

// failOrphans marks flows that were in flight when the previous process died.
func (s *SQLite) failOrphans(ctx context.Context) error {
	_, err := s.w.ExecContext(ctx,
		`UPDATE flows SET state = ?, error = ?, ts_end = COALESCE(ts_end, ?) WHERE state IN (?, ?)`,
		string(flow.StateFailed), "daemon restarted", time.Now().UnixMicro(),
		string(flow.StateActive), string(flow.StateHeld))
	if err != nil {
		return fmt.Errorf("store: sqlite: fail orphans: %w", err)
	}
	return nil
}

// Close flushes the queue, stops the writer, and closes both connection
// pools. It is safe to call more than once.
func (s *SQLite) Close() error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	close(s.done)
	s.closeMu.Unlock()

	// Detach from the bus first so the feeders stop producing.
	s.subsMu.Lock()
	for _, sub := range s.subs {
		sub.Close()
	}
	s.subs = nil
	s.subsMu.Unlock()
	s.subsWG.Wait()

	// Nobody can enqueue after closed=true, so closing the channel is safe.
	close(s.queue)
	s.writerWG.Wait()

	var errs []error
	if err := s.r.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := s.w.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// MaxID returns the highest flow id stored, for seeding the ID generator.
func (s *SQLite) MaxID() (flow.ID, error) {
	var id int64
	if err := s.r.QueryRowContext(context.Background(), "SELECT COALESCE(MAX(id), 0) FROM flows").Scan(&id); err != nil {
		return 0, fmt.Errorf("store: sqlite: max id: %w", err)
	}
	return idFrom(id), nil
}

// Dropped is the number of events and blobs discarded because the write
// queue was full, plus events the bus reported dropping before they reached
// the store.
func (s *SQLite) Dropped() int64 { return s.dropped.Load() }

// Get returns a flow by id, or ErrNotFound.
func (s *SQLite) Get(ctx context.Context, id flow.ID) (*flow.Flow, error) {
	row := s.r.QueryRowContext(ctx, "SELECT "+flowCols+" FROM flows WHERE id = ?", idArg(id))
	f, err := scanFlow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: sqlite: get %d: %w", id, err)
	}
	return f, nil
}

// GetBlob reads a blob by hash synchronously. Implements BlobPersister.
func (s *SQLite) GetBlob(hash string) ([]byte, bool) {
	var b []byte
	err := s.r.QueryRowContext(context.Background(), "SELECT data FROM blobs WHERE hash = ?", hash).Scan(&b)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("sqlite: get blob", "hash", hash, "err", err)
		}
		return nil, false
	}
	return b, true
}

// BodyText returns the cached decoded text of a blob, if the writer produced
// one (textual MIME class, decodable, valid UTF-8).
func (s *SQLite) BodyText(ctx context.Context, hash string) (string, bool) {
	var t string
	err := s.r.QueryRowContext(ctx, "SELECT text FROM blob_text WHERE hash = ?", hash).Scan(&t)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("sqlite: body text", "hash", hash, "err", err)
		}
		return "", false
	}
	return t, true
}

// Stats reports counters: flows, blobs, blob_bytes, fts_rows, ws_messages,
// sessions, db_bytes (main file), wal_bytes, free_pages, dropped, queued,
// batches and write_errors.
func (s *SQLite) Stats(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{
		"dropped":      s.dropped.Load(),
		"queued":       int64(len(s.queue)),
		"batches":      s.batches.Load(),
		"write_errors": s.writeErrs.Load(),
	}
	counts := map[string]string{
		"flows":       "SELECT COUNT(*) FROM flows",
		"blobs":       "SELECT COUNT(*) FROM blobs",
		"blob_bytes":  "SELECT COALESCE(SUM(size), 0) FROM blobs",
		"fts_rows":    "SELECT COUNT(*) FROM flows_fts",
		"ws_messages": "SELECT COUNT(*) FROM ws_messages",
		"sessions":    "SELECT COUNT(*) FROM sessions",
		"free_pages":  "PRAGMA freelist_count",
	}
	for k, q := range counts {
		var n int64
		if err := s.r.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return nil, fmt.Errorf("store: sqlite: stats %s: %w", k, err)
		}
		out[k] = n
	}
	out["db_bytes"] = fileSize(s.opts.Path)
	out["wal_bytes"] = fileSize(s.opts.Path + "-wal")
	return out, nil
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// ---------------------------------------------------------------------------
// Flow <-> row mapping. flowCols, flowArgs and scanFlow must stay in sync.

const flowCols = `id, session, kind, state, ts_start, ts_end, ttfb_us, total_us,
	client, proto, up_proto, scheme, host, port, method, path, query, status,
	req_headers, resp_headers, trailers,
	req_blob, req_size, req_captured, req_trunc, req_enc, req_mime,
	resp_blob, resp_size, resp_captured, resp_trunc, resp_enc, resp_mime,
	error, tags, rules, replay, replay_of, timing`

const flowColCount = 39

// flowColList is flowCols split into names.
var flowColList = func() []string {
	cols := strings.Split(strings.ReplaceAll(strings.ReplaceAll(flowCols, "\n", ""), "\t", ""), ",")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	if len(cols) != flowColCount {
		panic("store: flowCols count mismatch")
	}
	return cols
}()

// flowColsQualified is flowCols with a "flows." prefix, for joins.
var flowColsQualified = func() string {
	q := make([]string, len(flowColList))
	for i, c := range flowColList {
		q[i] = "flows." + c
	}
	return strings.Join(q, ", ")
}()

// flowUpsertSQL is built once from flowCols so column order is single-sourced.
var flowUpsertSQL = func() string {
	cols := flowColList
	var sb strings.Builder
	sb.WriteString("INSERT INTO flows(")
	sb.WriteString(strings.Join(cols, ", "))
	sb.WriteString(") VALUES(")
	sb.WriteString(strings.Repeat("?, ", len(cols)-1))
	sb.WriteString("?) ON CONFLICT(id) DO UPDATE SET ")
	for i, c := range cols {
		if c == "id" {
			continue
		}
		if i > 1 {
			sb.WriteString(", ")
		}
		sb.WriteString(c)
		sb.WriteString(" = excluded.")
		sb.WriteString(c)
	}
	return sb.String()
}()

// idArg converts a flow ID for binding. IDs are a small monotonic counter,
// so the signed/unsigned conversion cannot overflow in practice.
func idArg(id flow.ID) int64 { return int64(id) } //nolint:gosec // see above

// idFrom converts a stored id back. The column is never negative.
func idFrom(v int64) flow.ID { return flow.ID(v) } //nolint:gosec // see above

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullBool(b bool) any {
	if !b {
		return nil
	}
	return int64(1)
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMicro()
}

func jsonOrNull(v any, empty bool) (any, error) {
	if empty {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// flowArgs renders a flow as the positional values for flowUpsertSQL.
func flowArgs(f *flow.Flow) ([]any, error) {
	reqH, err := jsonOrNull(f.ReqHeaders, len(f.ReqHeaders) == 0)
	if err != nil {
		return nil, err
	}
	respH, err := jsonOrNull(f.RespHeaders, len(f.RespHeaders) == 0)
	if err != nil {
		return nil, err
	}
	trailers, err := jsonOrNull(f.Trailers, len(f.Trailers) == 0)
	if err != nil {
		return nil, err
	}
	tags, err := jsonOrNull(f.Tags, len(f.Tags) == 0)
	if err != nil {
		return nil, err
	}
	rules, err := jsonOrNull(f.Rules, len(f.Rules) == 0)
	if err != nil {
		return nil, err
	}
	timing, err := json.Marshal(f.T)
	if err != nil {
		return nil, err
	}
	var ttfb, total any
	if !f.T.FirstByte.IsZero() {
		ttfb = f.T.TTFB().Microseconds()
	}
	if !f.T.End.IsZero() {
		total = f.T.End.Sub(f.T.Start).Microseconds()
	}
	args := []any{
		idArg(f.ID), f.Session, string(f.Kind), string(f.State),
		f.T.Start.UnixMicro(), nullTime(f.T.End), ttfb, total,
		nullStr(f.Client), nullStr(f.Proto), nullStr(f.UpProto), nullStr(f.Scheme),
		f.Host, nullInt(int64(f.Port)), nullStr(f.Method), nullStr(f.Path), nullStr(f.Query),
		nullInt(int64(f.Status)),
		reqH, respH, trailers,
		nullStr(f.ReqBody.Hash), f.ReqBody.Size, f.ReqBody.Captured, nullBool(f.ReqBody.Truncated),
		nullStr(f.ReqBody.Encoding), nullStr(f.ReqBody.MIME),
		nullStr(f.RespBody.Hash), f.RespBody.Size, f.RespBody.Captured, nullBool(f.RespBody.Truncated),
		nullStr(f.RespBody.Encoding), nullStr(f.RespBody.MIME),
		nullStr(f.Error), tags, rules, nullBool(f.Replay), nullInt(idArg(f.ReplayOf)), string(timing),
	}
	if len(args) != flowColCount {
		panic("store: flowArgs count mismatch")
	}
	return args, nil
}

// scanner is the subset of *sql.Row / *sql.Rows used by scanFlow.
type scanner interface {
	Scan(dest ...any) error
}

// scanFlow reads one flowCols row.
func scanFlow(sc scanner) (*flow.Flow, error) {
	var (
		id, tsStart                                           int64
		session, kind, state, host                            string
		tsEnd, ttfb, total, port, status                      sql.NullInt64
		client, proto, upProto, scheme, method, path, query   sql.NullString
		reqH, respH, trailers                                 sql.NullString
		reqBlob, reqEnc, reqMime, respBlob, respEnc, respMime sql.NullString
		reqSize, reqCaptured, reqTrunc                        sql.NullInt64
		respSize, respCaptured, respTrunc                     sql.NullInt64
		errStr, tags, rules, timing                           sql.NullString
		replay, replayOf                                      sql.NullInt64
	)
	err := sc.Scan(&id, &session, &kind, &state, &tsStart, &tsEnd, &ttfb, &total,
		&client, &proto, &upProto, &scheme, &host, &port, &method, &path, &query, &status,
		&reqH, &respH, &trailers,
		&reqBlob, &reqSize, &reqCaptured, &reqTrunc, &reqEnc, &reqMime,
		&respBlob, &respSize, &respCaptured, &respTrunc, &respEnc, &respMime,
		&errStr, &tags, &rules, &replay, &replayOf, &timing)
	if err != nil {
		return nil, err
	}
	f := &flow.Flow{
		ID: idFrom(id), Session: session, Kind: flow.Kind(kind), State: flow.State(state),
		Client: client.String, Proto: proto.String, UpProto: upProto.String, Scheme: scheme.String,
		Host: host, Port: int(port.Int64), Method: method.String, Path: path.String, Query: query.String,
		Status: int(status.Int64),
		ReqBody: flow.BodyRef{
			Hash: reqBlob.String, Size: reqSize.Int64, Captured: reqCaptured.Int64,
			Truncated: reqTrunc.Int64 != 0, Encoding: reqEnc.String, MIME: reqMime.String,
		},
		RespBody: flow.BodyRef{
			Hash: respBlob.String, Size: respSize.Int64, Captured: respCaptured.Int64,
			Truncated: respTrunc.Int64 != 0, Encoding: respEnc.String, MIME: respMime.String,
		},
		Error: errStr.String, Replay: replay.Int64 != 0, ReplayOf: idFrom(replayOf.Int64),
	}
	if err := unmarshalInto(reqH, &f.ReqHeaders); err != nil {
		return nil, fmt.Errorf("req_headers: %w", err)
	}
	if err := unmarshalInto(respH, &f.RespHeaders); err != nil {
		return nil, fmt.Errorf("resp_headers: %w", err)
	}
	if err := unmarshalInto(trailers, &f.Trailers); err != nil {
		return nil, fmt.Errorf("trailers: %w", err)
	}
	if err := unmarshalInto(tags, &f.Tags); err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}
	if err := unmarshalInto(rules, &f.Rules); err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	if timing.Valid && timing.String != "" {
		if err := json.Unmarshal([]byte(timing.String), &f.T); err != nil {
			return nil, fmt.Errorf("timing: %w", err)
		}
	}
	if f.T.Start.IsZero() {
		f.T.Start = time.UnixMicro(tsStart).UTC()
		if tsEnd.Valid {
			f.T.End = time.UnixMicro(tsEnd.Int64).UTC()
		}
	}
	return f, nil
}

func unmarshalInto(ns sql.NullString, v any) error {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return json.Unmarshal([]byte(ns.String), v)
}

// headerLines renders headers as "Name: value" lines in a stable order, the
// form indexed for full-text search.
func headerLines(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		for _, v := range h[k] {
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
