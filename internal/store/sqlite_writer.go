package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/orron/pano/internal/bus"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/mimeclass"
)

// maxPendingText bounds the writer's map of flows waiting for a late blob.
const maxPendingText = 4096

type wqKind uint8

const (
	wqFlow wqKind = iota + 1
	wqWS
	wqBlob
	wqFlush
)

// wqItem is one unit of write-behind work.
type wqItem struct {
	kind  wqKind
	flow  *flow.Flow
	ws    *flow.WSMessage
	hash  string
	data  []byte
	flush chan struct{}
}

// Enqueue queues an event for persistence and returns immediately. Started,
// Headers and Done events upsert the flow row (the event's Flow snapshot is
// stored as-is); WS events append a message; other events are ignored except
// that bus drop notices are added to Dropped. When the queue is full the
// event is discarded and counted.
func (s *SQLite) Enqueue(ev flow.Event) {
	switch ev.Type {
	case flow.EvStarted, flow.EvHeaders, flow.EvDone:
		if ev.Flow == nil {
			return
		}
		s.offer(wqItem{kind: wqFlow, flow: ev.Flow})
	case flow.EvWS:
		if ev.WS == nil {
			return
		}
		s.offer(wqItem{kind: wqWS, ws: ev.WS})
	case flow.EvDropped:
		if ev.Dropped > 0 {
			s.dropped.Add(int64(ev.Dropped))
		}
	case flow.EvHeld:
		// The flow is re-published as Started/Headers/Done when released.
	}
}

// PutBlob queues a blob for persistence. Duplicates (same hash) are ignored
// by the writer. Implements BlobPersister.
func (s *SQLite) PutBlob(hash string, b []byte) {
	if hash == "" {
		return
	}
	s.offer(wqItem{kind: wqBlob, hash: hash, data: b})
}

// offer is the non-blocking enqueue; drops are counted.
func (s *SQLite) offer(it wqItem) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.queue <- it:
	default:
		s.dropped.Add(1)
	}
}

// Subscribe attaches the store to a bus: a goroutine feeds every flow event
// to Enqueue until the store is closed.
func (s *SQLite) Subscribe(b *bus.Bus) {
	s.closeMu.RLock()
	closed := s.closed
	s.closeMu.RUnlock()
	if closed {
		return
	}
	sub := b.Subscribe(s.opts.QueueSize, func(ev flow.Event) bool {
		switch ev.Type {
		case flow.EvStarted, flow.EvHeaders, flow.EvDone, flow.EvWS, flow.EvDropped:
			return true
		case flow.EvHeld:
			return false
		}
		return false
	})
	s.subsMu.Lock()
	s.subs = append(s.subs, sub)
	s.subsMu.Unlock()
	s.subsWG.Add(1)
	go func() {
		defer s.subsWG.Done()
		for ev := range sub.C {
			s.Enqueue(ev)
		}
	}()
}

// Flush blocks until everything queued before the call has been committed,
// or ctx is done. Unlike Enqueue it waits for queue space.
func (s *SQLite) Flush(ctx context.Context) error {
	ch := make(chan struct{})
	s.closeMu.RLock()
	if s.closed {
		s.closeMu.RUnlock()
		return ErrClosed
	}
	select {
	case s.queue <- wqItem{kind: wqFlush, flush: ch}:
		s.closeMu.RUnlock()
	case <-ctx.Done():
		s.closeMu.RUnlock()
		return ctx.Err()
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// writerLoop drains the queue into batched transactions until the queue is
// closed, then drains whatever is left.
func (s *SQLite) writerLoop() {
	defer s.writerWG.Done()
	batch := make([]wqItem, 0, s.opts.BatchSize)
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	for {
		first, ok := <-s.queue
		if !ok {
			return
		}
		batch = append(batch[:0], first)
		timer.Reset(s.opts.BatchDelay)
	collect:
		for len(batch) < s.opts.BatchSize {
			select {
			case it, ok := <-s.queue:
				if !ok {
					break collect
				}
				batch = append(batch, it)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		s.applyBatch(batch)
	}
}

// applyBatch writes one batch in a single transaction. Blobs are inserted
// first so flows finishing in the same batch can index their bodies; flow
// snapshots are coalesced to the last event per id; WS messages keep order.
// A failing statement is logged and skipped; the rest of the batch commits.
func (s *SQLite) applyBatch(batch []wqItem) {
	ctx := context.Background()
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		s.batchFailed(batch, err)
		return
	}
	b := &batchWriter{s: s, tx: tx, ctx: ctx}
	defer b.closeStmts()

	// 1. Blobs.
	var lateHashes []string
	for i := range batch {
		it := &batch[i]
		if it.kind != wqBlob {
			continue
		}
		if err := b.insertBlob(it.hash, it.data); err != nil {
			s.stmtFailed("blob", err)
			continue
		}
		if _, waiting := s.pendingText[it.hash]; waiting {
			lateHashes = append(lateHashes, it.hash)
		}
	}

	// 2. Flows: last snapshot per id wins.
	last := make(map[flow.ID]int, len(batch))
	for i := range batch {
		if batch[i].kind == wqFlow {
			last[batch[i].flow.ID] = i
		}
	}
	for i := range batch {
		it := &batch[i]
		if it.kind != wqFlow || last[it.flow.ID] != i {
			continue
		}
		if err := b.upsertFlow(it.flow); err != nil {
			s.stmtFailed("flow", err)
			continue
		}
		if isFinal(it.flow.State) {
			if err := b.indexFlow(it.flow); err != nil {
				s.stmtFailed("fts", err)
			}
		}
	}

	// 3. WebSocket messages, in arrival order.
	for i := range batch {
		it := &batch[i]
		if it.kind != wqWS {
			continue
		}
		if err := b.insertWS(it.ws); err != nil {
			s.stmtFailed("ws", err)
		}
	}

	// 4. Flows that finished earlier while their textual blob was missing.
	for _, h := range lateHashes {
		ids := s.pendingText[h]
		delete(s.pendingText, h)
		for _, id := range ids {
			if err := b.reindexFlow(id); err != nil {
				s.stmtFailed("fts reindex", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		s.batchFailed(batch, err)
		return
	}
	s.batches.Add(1)
	for i := range batch {
		if batch[i].kind == wqFlush {
			close(batch[i].flush)
		}
	}
}

func (s *SQLite) batchFailed(batch []wqItem, err error) {
	s.writeErrs.Add(1)
	s.log.Error("sqlite: batch failed; dropping", "items", len(batch), "err", err)
	for i := range batch {
		switch batch[i].kind {
		case wqFlush:
			close(batch[i].flush)
		case wqFlow, wqWS, wqBlob:
			s.dropped.Add(1)
		}
	}
}

func (s *SQLite) stmtFailed(what string, err error) {
	s.writeErrs.Add(1)
	s.dropped.Add(1)
	s.log.Warn("sqlite: write failed", "what", what, "err", err)
}

func isFinal(st flow.State) bool {
	return st == flow.StateDone || st == flow.StateFailed
}

// batchWriter holds the per-transaction prepared statements.
type batchWriter struct {
	s   *SQLite
	tx  *sql.Tx
	ctx context.Context

	upsert, blob, blobSeed, blobText, ws, ftsDel, ftsIns *sql.Stmt
}

func (b *batchWriter) closeStmts() {
	for _, st := range []*sql.Stmt{b.upsert, b.blob, b.blobSeed, b.blobText, b.ws, b.ftsDel, b.ftsIns} {
		if st != nil {
			_ = st.Close()
		}
	}
}

func (b *batchWriter) prep(dst **sql.Stmt, q string) (*sql.Stmt, error) {
	if *dst != nil {
		return *dst, nil
	}
	st, err := b.tx.PrepareContext(b.ctx, q)
	if err != nil {
		return nil, err
	}
	*dst = st
	return st, nil
}

func (b *batchWriter) upsertFlow(f *flow.Flow) error {
	st, err := b.prep(&b.upsert, flowUpsertSQL)
	if err != nil {
		return err
	}
	args, err := flowArgs(f)
	if err != nil {
		return err
	}
	_, err = st.ExecContext(b.ctx, args...)
	return err
}

// insertBlob stores a blob once. A blob that is genuinely new then has its
// refcount seeded from flows already referencing it (normally none: blobs
// precede the Done event) so a late blob is never mistaken for an orphan.
// Duplicates return before the count so shared blobs stay O(1).
func (b *batchWriter) insertBlob(hash string, data []byte) error {
	st, err := b.prep(&b.blob, `INSERT OR IGNORE INTO blobs(hash, size, data, created_at, refcount) VALUES(?, ?, ?, ?, 0)`)
	if err != nil {
		return err
	}
	if data == nil {
		data = []byte{}
	}
	res, err := st.ExecContext(b.ctx, hash, int64(len(data)), data, time.Now().UnixMicro())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	seed, err := b.prep(&b.blobSeed, `UPDATE blobs SET refcount =
		(SELECT COUNT(*) FROM flows WHERE req_blob = ?1) + (SELECT COUNT(*) FROM flows WHERE resp_blob = ?1)
		WHERE hash = ?1`)
	if err != nil {
		return err
	}
	_, err = seed.ExecContext(b.ctx, hash)
	return err
}

func (b *batchWriter) insertWS(m *flow.WSMessage) error {
	st, err := b.prep(&b.ws, `INSERT OR IGNORE INTO ws_messages(flow_id, seq, ts, dir, opcode, len, payload, masked)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	_, err = st.ExecContext(b.ctx, idArg(m.FlowID), m.Seq, m.TS.UnixMicro(), m.Dir, m.Opcode, m.Len, m.Payload, boolInt(m.Masked))
	return err
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// indexFlow (re)writes the FTS row for a final flow.
func (b *batchWriter) indexFlow(f *flow.Flow) error {
	del, err := b.prep(&b.ftsDel, "DELETE FROM flows_fts WHERE rowid = ?")
	if err != nil {
		return err
	}
	ins, err := b.prep(&b.ftsIns, `INSERT INTO flows_fts(rowid, host, path, req_headers, resp_headers, req_text, resp_text)
		VALUES(?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	reqText, err := b.bodyText(f.ID, f.ReqBody)
	if err != nil {
		return err
	}
	respText, err := b.bodyText(f.ID, f.RespBody)
	if err != nil {
		return err
	}
	if _, err := del.ExecContext(b.ctx, idArg(f.ID)); err != nil {
		return err
	}
	path := f.Path
	if f.Query != "" {
		path += "?" + f.Query
	}
	_, err = ins.ExecContext(b.ctx, idArg(f.ID), f.Host, path,
		headerLines(f.ReqHeaders), headerLines(f.RespHeaders), reqText, respText)
	return err
}

// reindexFlow reloads a stored flow and rebuilds its FTS row.
func (b *batchWriter) reindexFlow(id flow.ID) error {
	row := b.tx.QueryRowContext(b.ctx, "SELECT "+flowCols+" FROM flows WHERE id = ?", idArg(id))
	f, err := scanFlow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // pruned meanwhile
	}
	if err != nil {
		return err
	}
	if !isFinal(f.State) {
		return nil
	}
	return b.indexFlow(f)
}

// bodyText returns the decoded text to index for a body: the blob_text cache
// entry if present, else decoded from the blob (and cached). Bodies whose
// MIME class is not textual, that fail to decode, or that are not UTF-8
// yield "". A missing blob records the flow as pending so a late blob can
// trigger re-indexing.
func (b *batchWriter) bodyText(id flow.ID, ref flow.BodyRef) (string, error) {
	if ref.Hash == "" {
		return "", nil
	}
	if !mimeclass.IsTextual(mimeclass.Of(ref.MIME)) {
		return "", nil
	}
	var text string
	err := b.tx.QueryRowContext(b.ctx, "SELECT text FROM blob_text WHERE hash = ?", ref.Hash).Scan(&text)
	if err == nil {
		return truncateUTF8(text, b.s.opts.FTSTextCap), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var data []byte
	err = b.tx.QueryRowContext(b.ctx, "SELECT data FROM blobs WHERE hash = ?", ref.Hash).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		b.s.notePending(ref.Hash, id)
		return "", nil
	}
	if err != nil {
		return "", err
	}
	dec, derr := DecodeBody(ref.Encoding, data, 0)
	if derr != nil && !errors.Is(derr, ErrDecodeLimit) {
		return "", nil
	}
	if !utf8.Valid(dec) {
		return "", nil
	}
	text = string(dec)
	st, err := b.prep(&b.blobText, "INSERT OR IGNORE INTO blob_text(hash, text) VALUES(?, ?)")
	if err != nil {
		return "", err
	}
	if _, err := st.ExecContext(b.ctx, ref.Hash, text); err != nil {
		return "", fmt.Errorf("blob_text: %w", err)
	}
	return truncateUTF8(text, b.s.opts.FTSTextCap), nil
}

// notePending remembers that flow id was indexed without the blob hash.
// Writer-goroutine only.
func (s *SQLite) notePending(hash string, id flow.ID) {
	if len(s.pendingText) >= maxPendingText {
		if _, ok := s.pendingText[hash]; !ok {
			return
		}
	}
	s.pendingText[hash] = append(s.pendingText[hash], id)
}

// truncateUTF8 cuts s to at most n bytes on a rune boundary.
func truncateUTF8(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
