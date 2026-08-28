package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/klauspost/compress/gzip"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/bus"
	"github.com/orron/pano/internal/flow"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openTest opens a store in a temp dir and closes it on cleanup.
func openTest(t testing.TB, mod func(*SQLiteOptions)) *SQLite {
	t.Helper()
	opts := SQLiteOptions{Path: filepath.Join(t.TempDir(), "pano.db"), Logger: quietLogger()}
	if mod != nil {
		mod(&opts)
	}
	s, err := OpenSQLite(opts)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func flush(t testing.TB, s *SQLite) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func mkFlow(id flow.ID, host, method, path string, status int, start time.Time) *flow.Flow {
	f := &flow.Flow{
		ID: id, Session: "s1", Kind: flow.KindHTTP, Client: "127.0.0.1:5000", Proto: "HTTP/1.1",
		Scheme: "https", Host: host, Port: 443, Method: method, Path: path, Status: status,
		ReqHeaders:  http.Header{"User-Agent": {"pano-test"}},
		RespHeaders: http.Header{"Content-Type": {"application/json"}},
		T:           flow.Timing{Start: start, FirstByte: start.Add(5 * time.Millisecond), End: start.Add(20 * time.Millisecond)},
		State:       flow.StateDone,
	}
	return f
}

func done(f *flow.Flow) flow.Event { return flow.Event{Type: flow.EvDone, Flow: f} }

func ids(l api.FlowList) []flow.ID {
	out := make([]flow.ID, 0, len(l.Flows))
	for _, r := range l.Flows {
		out = append(out, r.ID)
	}
	return out
}

func TestOpenMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pano.db")
	for i := 0; i < 2; i++ {
		s, err := OpenSQLite(SQLiteOptions{Path: path, Logger: quietLogger()})
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		var n int
		if err := s.r.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("open #%d: schema_version rows = %d, want 1", i, n)
		}
		var av int
		if err := s.r.QueryRow("PRAGMA auto_vacuum").Scan(&av); err != nil {
			t.Fatal(err)
		}
		if av != 2 {
			t.Fatalf("auto_vacuum = %d, want 2 (INCREMENTAL)", av)
		}
		var jm string
		if err := s.w.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
			t.Fatal(err)
		}
		if jm != "wal" {
			t.Fatalf("journal_mode = %q, want wal", jm)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
	}
	if _, err := OpenSQLite(SQLiteOptions{}); err == nil {
		t.Fatal("expected error for empty Path")
	}
}

func TestFlowRoundTrip(t *testing.T) {
	s := openTest(t, nil)
	start := time.Now().Add(-time.Second)
	f := &flow.Flow{
		ID: 42, Session: "sess-a", Kind: flow.KindHTTP, Client: "127.0.0.1:61234",
		Proto: "HTTP/2.0", UpProto: "h2", Scheme: "https", Host: "api.anthropic.com", Port: 8443,
		Method: "POST", Path: "/v1/messages", Query: "beta=true&x=1",
		ReqHeaders:  http.Header{"Content-Type": {"application/json"}, "X-Multi": {"a", "b"}},
		ReqBody:     flow.BodyRef{Hash: Hash([]byte("req")), Size: 3, Captured: 3, MIME: "application/json"},
		Status:      200,
		RespHeaders: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {"gzip"}},
		RespBody:    flow.BodyRef{Hash: Hash([]byte("resp")), Size: 1000, Captured: 500, Truncated: true, Encoding: "gzip", MIME: "text/event-stream"},
		Trailers:    http.Header{"Grpc-Status": {"0"}},
		T: flow.Timing{
			Start: start, DNSDone: start.Add(time.Millisecond), Connected: start.Add(2 * time.Millisecond),
			TLSDone: start.Add(3 * time.Millisecond), WroteReq: start.Add(4 * time.Millisecond),
			FirstByte: start.Add(50 * time.Millisecond), HeadersSent: start.Add(51 * time.Millisecond),
			End: start.Add(200 * time.Millisecond), Reused: true,
		},
		Error: "",
		Tags:  []string{"llm", "prod"},
		Rules: []flow.RuleHit{{RuleID: "r1", Name: "slow", Phase: "request", Action: "delay", Note: "200ms"}},
		State: flow.StateDone, Replay: true, ReplayOf: 41,
	}
	active := f.Clone()
	active.State = flow.StateActive
	active.Status = 0
	active.RespHeaders = nil
	s.Enqueue(flow.Event{Type: flow.EvStarted, Flow: active})
	headers := f.Clone()
	headers.State = flow.StateActive
	s.Enqueue(flow.Event{Type: flow.EvHeaders, Flow: headers})
	s.Enqueue(done(f))
	flush(t, s)

	got, err := s.Get(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(f)
	have, _ := json.Marshal(got)
	if !bytes.Equal(want, have) {
		t.Fatalf("round trip mismatch\n want %s\n have %s", want, have)
	}
	if !got.T.Start.Equal(f.T.Start) || got.T.TTFB() != f.T.TTFB() {
		t.Fatalf("timing mismatch: %+v vs %+v", got.T, f.T)
	}
	if _, err := s.Get(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}

	// Derived columns.
	var ttfb, total int64
	if err := s.r.QueryRow("SELECT ttfb_us, total_us FROM flows WHERE id = 42").Scan(&ttfb, &total); err != nil {
		t.Fatal(err)
	}
	if ttfb != 50_000 || total != 200_000 {
		t.Fatalf("ttfb_us=%d total_us=%d", ttfb, total)
	}
}

func TestEnqueueIgnoresOtherEvents(t *testing.T) {
	s := openTest(t, nil)
	s.Enqueue(flow.Event{Type: flow.EvHeld, Flow: mkFlow(1, "h", "GET", "/", 200, time.Now())})
	s.Enqueue(flow.Event{Type: flow.EvStarted})
	s.Enqueue(flow.Event{Type: flow.EvWS})
	s.Enqueue(flow.Event{Type: flow.EvDropped, Dropped: 3})
	flush(t, s)
	if n, _ := s.MaxID(); n != 0 {
		t.Fatalf("unexpected rows: max id %d", n)
	}
	if s.Dropped() != 3 {
		t.Fatalf("Dropped = %d, want 3 (bus drop notice)", s.Dropped())
	}
}

func TestBlobDedupeRefcount(t *testing.T) {
	s := openTest(t, nil)
	body := []byte(`{"hello":"world"}`)
	h := Hash(body)
	s.PutBlob(h, body)
	s.PutBlob(h, body)
	now := time.Now()
	f1 := mkFlow(1, "a.example.com", "POST", "/x", 200, now)
	f1.ReqBody = flow.BodyRef{Hash: h, Size: int64(len(body)), Captured: int64(len(body)), MIME: "application/json"}
	f2 := mkFlow(2, "a.example.com", "POST", "/y", 200, now)
	f2.ReqBody = f1.ReqBody
	f2.RespBody = f1.ReqBody // same blob on both sides counts twice
	s.Enqueue(done(f1))
	s.Enqueue(done(f2))
	flush(t, s)

	var n, rc int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("blobs = %d, want 1", n)
	}
	if err := s.r.QueryRow("SELECT refcount FROM blobs WHERE hash = ?", h).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 3 {
		t.Fatalf("refcount = %d, want 3", rc)
	}
	got, ok := s.GetBlob(h)
	if !ok || !bytes.Equal(got, body) {
		t.Fatalf("GetBlob = %q, %v", got, ok)
	}
	if _, ok := s.GetBlob("nope"); ok {
		t.Fatal("GetBlob(missing) ok")
	}
	if txt, ok := s.BodyText(context.Background(), h); !ok || txt != string(body) {
		t.Fatalf("BodyText = %q, %v", txt, ok)
	}

	// Re-upserting the same flow keeps the count stable; a body swap moves it.
	f2b := f2.Clone()
	other := []byte("other")
	s.PutBlob(Hash(other), other)
	f2b.RespBody = flow.BodyRef{Hash: Hash(other), Size: 5, Captured: 5}
	s.Enqueue(done(f2b))
	s.Enqueue(done(f1))
	flush(t, s)
	if err := s.r.QueryRow("SELECT refcount FROM blobs WHERE hash = ?", h).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 2 {
		t.Fatalf("refcount after swap = %d, want 2", rc)
	}
	if err := s.r.QueryRow("SELECT refcount FROM blobs WHERE hash = ?", Hash(other)).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Fatalf("refcount(other) = %d, want 1", rc)
	}

	// MemBlobs can front the store.
	mb := NewMemBlobs(1 << 20)
	mb.Persister = s
	if b, ok := mb.Get(h); !ok || !bytes.Equal(b, body) {
		t.Fatalf("MemBlobs.Get via persister = %q, %v", b, ok)
	}
	third := []byte("third body")
	mb.Put(third)
	flush(t, s)
	if _, ok := s.GetBlob(Hash(third)); !ok {
		t.Fatal("MemBlobs.Put did not persist")
	}
}

func TestLateBlobSeedsRefcountAndIndex(t *testing.T) {
	s := openTest(t, nil)
	body := []byte(`{"needle":"latecomer"}`)
	h := Hash(body)
	f := mkFlow(7, "late.example.com", "POST", "/", 200, time.Now())
	f.RespBody = flow.BodyRef{Hash: h, Size: int64(len(body)), Captured: int64(len(body)), MIME: "application/json"}
	s.Enqueue(done(f))
	flush(t, s)
	if l, _ := s.Query(context.Background(), api.FlowFilter{Q: "latecomer"}, 10, time.Now()); len(l.Flows) != 0 {
		t.Fatal("body text found before blob arrived")
	}
	s.PutBlob(h, body)
	flush(t, s)
	var rc int
	if err := s.r.QueryRow("SELECT refcount FROM blobs WHERE hash = ?", h).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Fatalf("late blob refcount = %d, want 1", rc)
	}
	l, err := s.Query(context.Background(), api.FlowFilter{Q: "latecomer"}, 10, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Flows) != 1 || l.Flows[0].ID != 7 {
		t.Fatalf("late blob not re-indexed: %+v", l)
	}
}

// seedQueryFlows inserts a fixed corpus and returns it (also usable for Mem).
func seedQueryFlows(t *testing.T, s *SQLite, now time.Time) []*flow.Flow {
	t.Helper()
	body := []byte(`{"model":"claude-opus","prompt":"tell me about hummingbirds"}`)
	s.PutBlob(Hash(body), body)
	gz := gzipBytes([]byte("<html><body>compressed page about penguins</body></html>"))
	s.PutBlob(Hash(gz), gz)
	bin := append([]byte("zqxbinarytoken "), 0x89, 'P', 'N', 'G', 0, 0, 0xff) // not UTF-8
	s.PutBlob(Hash(bin), bin)

	var fs []*flow.Flow
	add := func(f *flow.Flow) { fs = append(fs, f) }

	f := mkFlow(1, "api.anthropic.com", "POST", "/v1/messages", 200, now.Add(-90*time.Minute))
	f.ReqBody = flow.BodyRef{Hash: Hash(body), Size: int64(len(body)), Captured: int64(len(body)), MIME: "application/json"}
	f.RespHeaders = http.Header{"Content-Type": {"application/json"}, "X-Trace-Id": {"trace-abc123"}}
	f.RespBody.MIME = "application/json"
	f.Tags = []string{"llm"}
	add(f)

	f = mkFlow(2, "www.example.com", "GET", "/index.html", 200, now.Add(-40*time.Minute))
	f.RespHeaders = http.Header{"Content-Type": {"text/html"}, "Content-Encoding": {"gzip"}}
	f.RespBody = flow.BodyRef{Hash: Hash(gz), Size: int64(len(gz)), Captured: int64(len(gz)), Encoding: "gzip", MIME: "text/html"}
	add(f)

	f = mkFlow(3, "cdn.example.com", "GET", "/img/logo.png", 200, now.Add(-30*time.Minute))
	f.RespHeaders = http.Header{"Content-Type": {"image/png"}}
	f.RespBody = flow.BodyRef{Hash: Hash(bin), Size: 7, Captured: 7, MIME: "image/png"}
	add(f)

	f = mkFlow(4, "api.openai.com", "POST", "/v1/chat/completions", 429, now.Add(-10*time.Minute))
	f.Tags = []string{"llm", "ratelimited"}
	f.Rules = []flow.RuleHit{{RuleID: "r-mock", Name: "mock-429", Phase: "request", Action: "mock"}}
	add(f)

	f = mkFlow(5, "api.example.com", "DELETE", "/v2/items/9", 500, now.Add(-5*time.Minute))
	f.Session = "s2"
	add(f)

	f = mkFlow(6, "api.example.com", "GET", "/v2/items", 0, now.Add(-2*time.Minute))
	f.State = flow.StateFailed
	f.Error = "dial tcp: connection refused"
	f.Session = "s2"
	add(f)

	f = mkFlow(7, "ws.example.com", "GET", "/socket", 101, now.Add(-time.Minute))
	f.Kind = flow.KindWebSocket
	add(f)

	f = mkFlow(8, "api.example.com", "GET", "/v2/items", 200, now.Add(-30*time.Second))
	f.Replay = true
	f.ReplayOf = 6
	f.State = flow.StateActive
	add(f)

	for _, f := range fs {
		s.Enqueue(done(f))
	}
	flush(t, s)
	return fs
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func TestQueryMatchesMemSemantics(t *testing.T) {
	s := openTest(t, nil)
	now := time.Now()
	fs := seedQueryFlows(t, s, now)
	mem := NewMem(100)
	for _, f := range fs {
		mem.Upsert(f)
	}
	filters := []api.FlowFilter{
		{},
		{Host: "*.example.com"},
		{Host: "API.example.com"},
		{Host: "api.*"},
		{Method: []string{"get|post"}},
		{Method: []string{"DELETE"}},
		{Status: "5xx"},
		{Status: "!2xx"},
		{Status: "200|429"},
		{Status: "400-499"},
		{Status: "garbage"},
		{Since: "15m"},
		{Since: "1h", Until: "3m"},
		{Since: now.Add(-35 * time.Minute).Format(time.RFC3339Nano)},
		{Since: flow.ID(4).Short()},
		{ContentType: "json"},
		{ContentType: "image/"},
		{ContentType: "html"},
		{HasError: true},
		{MinBytes: 8},
		{Tag: "llm"},
		{Tag: "ratelimited"},
		{Rule: "mock-429"},
		{Rule: "r-mock"},
		{State: "failed"},
		{State: "active"},
		{State: "replayed"},
		{State: "mocked"},
		{State: "blocked"},
		{State: "all"},
		{Kind: "websocket"},
		{Session: "s2"},
		{Path: "/v2/items"},
		{Path: "/v1/*"},
		{Path: "/^v2/"},
		{Host: "api.example.com", Status: "2xx", Method: []string{"GET"}},
		{Cursor: "before:" + flow.ID(5).Short()},
	}
	for _, f := range filters {
		want := ids(Query(mem, f, 50, now))
		got, err := s.Query(context.Background(), f, 50, now)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		if fmt.Sprint(ids(got)) != fmt.Sprint(want) {
			t.Errorf("filter %+v: sqlite %v, mem %v", f, ids(got), want)
		}
		if got.Total != len(want) {
			t.Errorf("filter %+v: total %d, want %d", f, got.Total, len(want))
		}
		if got.LastID != 8 {
			t.Errorf("filter %+v: LastID %d", f, got.LastID)
		}
	}
}

func TestQueryPagination(t *testing.T) {
	s := openTest(t, nil)
	now := time.Now()
	seedQueryFlows(t, s, now)
	ctx := context.Background()
	var all []flow.ID
	f := api.FlowFilter{Host: "*.example.com"}
	for page := 0; page < 10; page++ {
		l, err := s.Query(ctx, f, 2, now)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, ids(l)...)
		// Like Query over Mem, Total counts matches after the cursor.
		if want := 6 - 2*page; l.Total != want {
			t.Fatalf("page %d: Total = %d, want %d", page, l.Total, want)
		}
		if l.Cursor == "" {
			break
		}
		f.Cursor = l.Cursor
	}
	if fmt.Sprint(all) != fmt.Sprint([]flow.ID{8, 7, 6, 5, 3, 2}) {
		t.Fatalf("paginated ids = %v", all)
	}

	// Limit clamps.
	if l, _ := s.Query(ctx, api.FlowFilter{}, 0, now); len(l.Flows) != 8 {
		t.Fatalf("default limit: %d rows", len(l.Flows))
	}
	// Residual path (path prefix needs the Matcher) also paginates. Flows
	// 8, 6, 5 are used because their short ids are not all digits: ParseShort
	// reads "8" (the short form of id 4) as decimal 8.
	f = api.FlowFilter{Path: "/v2/items"}
	var got []flow.ID
	for page := 0; page < 5; page++ {
		l, err := s.Query(ctx, f, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := 3 - page; l.Total != want || len(l.Flows) != 1 {
			t.Fatalf("residual page %d: %+v", page, l)
		}
		got = append(got, l.Flows[0].ID)
		if l.Cursor == "" {
			break
		}
		f.Cursor = l.Cursor
	}
	if fmt.Sprint(got) != fmt.Sprint([]flow.ID{8, 6, 5}) {
		t.Fatalf("residual pages = %v", got)
	}
}

func TestQueryFTS(t *testing.T) {
	s := openTest(t, nil)
	now := time.Now()
	seedQueryFlows(t, s, now)
	ctx := context.Background()
	cases := []struct {
		q    string
		want []flow.ID
	}{
		{"hummingbirds", []flow.ID{1}},           // decoded request body
		{"penguins", []flow.ID{2}},               // gzip-decoded response body
		{"trace-abc123", []flow.ID{1}},           // response header value
		{"x-trace-id", []flow.ID{1}},             // header name
		{"api.example.com", []flow.ID{6, 5}},     // host with punctuation; 8 is still active
		{"/v2/items", []flow.ID{6, 5}},           // path
		{"hummingbirds anthropic", []flow.ID{1}}, // implicit AND
		{"humming*", []flow.ID{1}},               // prefix
		{"png", []flow.ID{3}},                    // path token
		{"zqxbinarytoken", nil},                  // binary bodies are not indexed
		{`"unbalanced`, nil},                     // syntax is neutralised
		{"claude-opus", []flow.ID{1}},            // json body token with dash
		{"user-agent pano-test", []flow.ID{7, 6, 5, 4, 3, 2, 1}},
	}
	for _, c := range cases {
		l, err := s.Query(ctx, api.FlowFilter{Q: c.q}, 50, now)
		if err != nil {
			t.Fatalf("q=%q: %v", c.q, err)
		}
		got := ids(l)
		sort.Slice(got, func(i, j int) bool { return got[i] > got[j] })
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("q=%q: got %v want %v", c.q, got, c.want)
		}
		if l.Total != len(c.want) {
			t.Errorf("q=%q: total %d want %d", c.q, l.Total, len(c.want))
		}
	}
	// FTS combines with SQL predicates and residual filters.
	l, _ := s.Query(ctx, api.FlowFilter{Q: "/v2/items", Status: "5xx"}, 50, now)
	if fmt.Sprint(ids(l)) != fmt.Sprint([]flow.ID{5}) {
		t.Fatalf("q + status: %v", ids(l))
	}
	l, _ = s.Query(ctx, api.FlowFilter{Q: "pano-test", Tag: "llm"}, 50, now)
	got := ids(l)
	sort.Slice(got, func(i, j int) bool { return got[i] > got[j] })
	if fmt.Sprint(got) != fmt.Sprint([]flow.ID{4, 1}) {
		t.Fatalf("q + tag: %v", got)
	}
	// Deleting a flow removes it from the index.
	if _, err := s.deleteTx(ctx, "id = ?", int64(1)); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Query(ctx, api.FlowFilter{Q: "hummingbirds"}, 50, now); len(l.Flows) != 0 {
		t.Fatalf("deleted flow still indexed: %v", ids(l))
	}
	var n int
	_ = s.r.QueryRow("SELECT COUNT(*) FROM flows_fts").Scan(&n)
	if n != 6 {
		t.Fatalf("fts rows = %d, want 6", n)
	}
}

func TestFTSTextCap(t *testing.T) {
	s := openTest(t, func(o *SQLiteOptions) { o.FTSTextCap = 64 })
	body := []byte("alpha " + string(bytes.Repeat([]byte("x"), 100)) + " omega")
	s.PutBlob(Hash(body), body)
	f := mkFlow(1, "cap.example.com", "POST", "/", 200, time.Now())
	f.ReqBody = flow.BodyRef{Hash: Hash(body), Size: int64(len(body)), Captured: int64(len(body)), MIME: "text/plain"}
	s.Enqueue(done(f))
	flush(t, s)
	if l, _ := s.Query(context.Background(), api.FlowFilter{Q: "alpha"}, 10, time.Now()); len(l.Flows) != 1 {
		t.Fatal("prefix of body not indexed")
	}
	if l, _ := s.Query(context.Background(), api.FlowFilter{Q: "omega"}, 10, time.Now()); len(l.Flows) != 0 {
		t.Fatal("text beyond FTSTextCap was indexed")
	}
	// The blob_text cache keeps the full decoded body.
	if txt, ok := s.BodyText(context.Background(), Hash(body)); !ok || txt != string(body) {
		t.Fatalf("BodyText truncated: %d bytes", len(txt))
	}
	if got := truncateUTF8("héllo", 2); got != "h" {
		t.Fatalf("truncateUTF8 = %q", got)
	}
}

func TestDropWhenQueueFull(t *testing.T) {
	s := openTest(t, func(o *SQLiteOptions) {
		o.QueueSize = 1
		o.BatchSize = 2
		o.BatchDelay = 500 * time.Millisecond
	})
	start := time.Now()
	const n = 2000
	for i := 1; i <= n; i++ {
		s.Enqueue(done(mkFlow(flow.ID(i), "h", "GET", "/", 200, start)))
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Enqueue blocked: %v", el)
	}
	if s.Dropped() == 0 {
		t.Fatal("expected drops with a tiny queue")
	}
	flush(t, s)
	st, err := s.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st["flows"]+st["dropped"] < n {
		t.Fatalf("flows %d + dropped %d < %d", st["flows"], st["dropped"], n)
	}
	if st["flows"] == 0 {
		t.Fatal("nothing was written")
	}
}

func TestRestartFailsActiveFlows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pano.db")
	s, err := OpenSQLite(SQLiteOptions{Path: path, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := mkFlow(1, "a", "GET", "/", 0, now)
	a.State = flow.StateActive
	h := mkFlow(2, "b", "GET", "/", 0, now)
	h.State = flow.StateHeld
	d := mkFlow(3, "c", "GET", "/", 200, now)
	s.Enqueue(flow.Event{Type: flow.EvStarted, Flow: a})
	s.Enqueue(flow.Event{Type: flow.EvHeld, Flow: h}) // ignored
	s.Enqueue(flow.Event{Type: flow.EvStarted, Flow: h})
	s.Enqueue(done(d))
	// Close flushes without an explicit Flush.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Flush after Close = %v", err)
	}
	s.Enqueue(done(d)) // must not panic
	if s.Dropped() != 1 {
		t.Fatalf("Dropped after Close = %d", s.Dropped())
	}

	s, err = OpenSQLite(SQLiteOptions{Path: path, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if id, _ := s.MaxID(); id != 3 {
		t.Fatalf("MaxID = %d", id)
	}
	for _, id := range []flow.ID{1, 2} {
		f, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if f.State != flow.StateFailed || f.Error != "daemon restarted" || f.T.End.IsZero() && f.T.Start.IsZero() {
			t.Fatalf("flow %d after restart: %+v", id, f)
		}
	}
	f, _ := s.Get(context.Background(), 3)
	if f.State != flow.StateDone || f.Error != "" {
		t.Fatalf("done flow altered: %+v", f)
	}
	var n int
	_ = s.r.QueryRow("SELECT COUNT(*) FROM flows WHERE state IN ('active','held')").Scan(&n)
	if n != 0 {
		t.Fatalf("%d flows still active", n)
	}
}

func TestMaxIDEmpty(t *testing.T) {
	s := openTest(t, nil)
	if id, err := s.MaxID(); err != nil || id != 0 {
		t.Fatalf("MaxID empty = %d, %v", id, err)
	}
	s.Enqueue(done(mkFlow(17, "h", "GET", "/", 200, time.Now())))
	s.Enqueue(done(mkFlow(5, "h", "GET", "/", 200, time.Now())))
	flush(t, s)
	if id, _ := s.MaxID(); id != 17 {
		t.Fatalf("MaxID = %d", id)
	}
}

func TestWSMessages(t *testing.T) {
	s := openTest(t, nil)
	now := time.Now()
	f := mkFlow(9, "ws.example.com", "GET", "/socket", 101, now)
	f.Kind = flow.KindWebSocket
	f.State = flow.StateActive
	s.Enqueue(flow.Event{Type: flow.EvStarted, Flow: f})
	for i := 0; i < 5; i++ {
		dir := "c2s"
		if i%2 == 1 {
			dir = "s2c"
		}
		s.Enqueue(flow.Event{Type: flow.EvWS, WS: &flow.WSMessage{
			FlowID: 9, Seq: i, TS: now.Add(time.Duration(i) * time.Millisecond), Dir: dir, Opcode: 1,
			Len: 5, Payload: []byte(fmt.Sprintf("msg-%d", i)), Masked: dir == "c2s",
		}})
	}
	// Duplicate seq is ignored.
	s.Enqueue(flow.Event{Type: flow.EvWS, WS: &flow.WSMessage{FlowID: 9, Seq: 0, TS: now, Dir: "c2s", Opcode: 1, Len: 3, Payload: []byte("dup")}})
	flush(t, s)

	ms, err := s.WSMessages(context.Background(), 9, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 5 {
		t.Fatalf("got %d messages", len(ms))
	}
	for i, m := range ms {
		if m.Seq != i || m.FlowID != 9 || string(m.Payload) != fmt.Sprintf("msg-%d", i) || m.Len != 5 || m.Opcode != 1 {
			t.Fatalf("message %d: %+v", i, m)
		}
		if m.Masked != (m.Dir == "c2s") || !m.TS.Equal(now.Add(time.Duration(i)*time.Millisecond).Truncate(time.Microsecond)) {
			t.Fatalf("message %d: %+v", i, m)
		}
	}
	if ms, _ = s.WSMessages(context.Background(), 9, 2); len(ms) != 2 {
		t.Fatalf("limit: %d", len(ms))
	}
	if ms, _ = s.WSMessages(context.Background(), 404, 0); len(ms) != 0 {
		t.Fatalf("unknown flow: %d", len(ms))
	}
	// Deleting the flow removes its messages.
	if _, err := s.deleteTx(context.Background(), "id = ?", int64(9)); err != nil {
		t.Fatal(err)
	}
	if ms, _ = s.WSMessages(context.Background(), 9, 0); len(ms) != 0 {
		t.Fatalf("messages survived flow delete: %d", len(ms))
	}
}

func TestSubscribeBus(t *testing.T) {
	s := openTest(t, nil)
	b := bus.New()
	s.Subscribe(b)
	now := time.Now()
	f := mkFlow(1, "bus.example.com", "GET", "/", 200, now)
	b.Publish(flow.Event{Type: flow.EvStarted, Flow: f})
	b.Publish(flow.Event{Type: flow.EvHeld, Flow: f})
	b.Publish(done(f))
	deadline := time.Now().Add(5 * time.Second)
	for {
		flush(t, s)
		got, err := s.Get(context.Background(), 1)
		if err == nil && got.State == flow.StateDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("flow not persisted via bus: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b.Publish(done(f)) // subscriber is gone; must not block or panic
}

func TestStats(t *testing.T) {
	s := openTest(t, nil)
	seedQueryFlows(t, s, time.Now())
	st, err := s.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"flows", "blobs", "db_bytes", "dropped", "queued", "fts_rows"} {
		if _, ok := st[k]; !ok {
			t.Errorf("missing stat %q", k)
		}
	}
	if st["flows"] != 8 || st["blobs"] != 3 || st["fts_rows"] != 7 || st["db_bytes"] == 0 {
		t.Fatalf("stats: %v", st)
	}
}

func TestHeaderLines(t *testing.T) {
	h := http.Header{"B": {"2"}, "A": {"1", "1b"}}
	if got := headerLines(h); got != "A: 1\nA: 1b\nB: 2\n" {
		t.Fatalf("headerLines = %q", got)
	}
	if headerLines(nil) != "" {
		t.Fatal("nil headers")
	}
}

func TestFilterTranslationHelpers(t *testing.T) {
	if got := globToLike("*.ex_ample.com?"); got != `%.ex\_ample.com_` {
		t.Fatalf("globToLike = %q", got)
	}
	if got := likeEscape("100%_a\\"); got != `100\%\_a\\` {
		t.Fatalf("likeEscape = %q", got)
	}
	if got := ftsQuery(`hello "wor"ld* *`); got != `"hello" """wor""ld"*` {
		t.Fatalf("ftsQuery = %q", got)
	}
	if got := ftsQuery("   "); got != `""` {
		t.Fatalf("ftsQuery(blank) = %q", got)
	}
	if !tagSafeForLike("prod-v1.2:x/y") || tagSafeForLike(`a"b`) || tagSafeForLike("a<b") {
		t.Fatal("tagSafeForLike")
	}
	if c, ok := statusSQL("!4xx|500-503|200"); !ok || c != "(COALESCE(flows.status, 0) > 0 AND NOT (COALESCE(flows.status, 0) / 100 = 4 OR COALESCE(flows.status, 0) BETWEEN 500 AND 503 OR COALESCE(flows.status, 0) = 200))" {
		t.Fatalf("statusSQL = %q %v", c, ok)
	}
	if _, ok := statusSQL("nope"); ok {
		t.Fatal("statusSQL accepted garbage")
	}
}

func BenchmarkIngest(b *testing.B) {
	// Four queue items per flow; the queue is sized so the producer never
	// outruns the writer between the periodic flushes below.
	s := openTest(b, func(o *SQLiteOptions) { o.QueueSize = 1 << 16 })
	body := []byte(`{"model":"claude-opus","messages":[{"role":"user","content":"benchmark payload body text that is a few hundred bytes long so the writer has something realistic to store and index for full text search purposes"}]}`)
	resp := []byte(`{"id":"msg_01","type":"message","content":[{"type":"text","text":"hello from the benchmark response body"}],"usage":{"input_tokens":12,"output_tokens":7}}`)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 1; i <= b.N; i++ {
		id := flow.ID(i)
		reqBody := append([]byte(nil), body...)
		reqBody = append(reqBody, []byte(fmt.Sprintf(`{"i":%d}`, i))...)
		f := mkFlow(id, "api.anthropic.com", "POST", "/v1/messages", 0, now)
		f.State = flow.StateActive
		s.Enqueue(flow.Event{Type: flow.EvStarted, Flow: f})
		s.PutBlob(Hash(reqBody), reqBody)
		f2 := f.Clone()
		f2.Status = 200
		f2.ReqBody = flow.BodyRef{Hash: Hash(reqBody), Size: int64(len(reqBody)), Captured: int64(len(reqBody)), MIME: "application/json"}
		s.Enqueue(flow.Event{Type: flow.EvHeaders, Flow: f2})
		s.PutBlob(Hash(resp), resp)
		f3 := f2.Clone()
		f3.State = flow.StateDone
		f3.RespBody = flow.BodyRef{Hash: Hash(resp), Size: int64(len(resp)), Captured: int64(len(resp)), MIME: "application/json"}
		s.Enqueue(done(f3))
		if i%8192 == 0 {
			flush(b, s) // keep the queue from overflowing; we measure writes, not drops
		}
	}
	flush(b, s)
	b.StopTimer()
	el := b.Elapsed()
	b.ReportMetric(float64(b.N)/el.Seconds(), "flows/s")
	if s.Dropped() != 0 {
		b.Fatalf("dropped %d", s.Dropped())
	}
}
