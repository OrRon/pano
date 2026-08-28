package store

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func TestPruneByAge(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	now := time.Now()
	old := []byte("old body")
	shared := []byte("shared body")
	s.PutBlob(Hash(old), old)
	s.PutBlob(Hash(shared), shared)
	for i := 1; i <= 6; i++ {
		f := mkFlow(flow.ID(i), "h", "GET", "/", 200, now.Add(-time.Duration(i)*time.Hour))
		if i > 3 {
			f.ReqBody = flow.BodyRef{Hash: Hash(old), Size: 8, Captured: 8, MIME: "text/plain"}
		}
		f.RespBody = flow.BodyRef{Hash: Hash(shared), Size: 11, Captured: 11, MIME: "text/plain"}
		s.Enqueue(done(f))
	}
	flush(t, s)

	st, err := s.Prune(ctx, 3*time.Hour+30*time.Minute, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.ByAge != 3 || st.Flows != 3 || st.Blobs != 1 {
		t.Fatalf("stats: %+v", st)
	}
	for id := flow.ID(1); id <= 6; id++ {
		_, err := s.Get(ctx, id)
		if (id <= 3) != (err == nil) {
			t.Fatalf("flow %d: %v", id, err)
		}
	}
	if _, ok := s.GetBlob(Hash(old)); ok {
		t.Fatal("orphan blob survived")
	}
	if _, ok := s.GetBlob(Hash(shared)); !ok {
		t.Fatal("referenced blob was pruned")
	}
	if _, ok := s.BodyText(ctx, Hash(old)); ok {
		t.Fatal("blob_text of orphan survived (cascade)")
	}
	// The FTS index shrinks with the flows.
	l, _ := s.Query(ctx, api.FlowFilter{Q: "old"}, 10, now)
	if len(l.Flows) != 0 {
		t.Fatalf("pruned flows still searchable: %v", ids(l))
	}
	st2, _ := s.Stats(ctx)
	if st2["fts_rows"] != 3 || st2["flows"] != 3 || st2["blobs"] != 1 {
		t.Fatalf("stats after prune: %v", st2)
	}
	// Idempotent.
	st, _ = s.Prune(ctx, 3*time.Hour+30*time.Minute, 0, 0)
	if st.Flows != 0 || st.Blobs != 0 {
		t.Fatalf("second prune: %+v", st)
	}
}

func TestPruneByCount(t *testing.T) {
	s := openTest(t, func(o *SQLiteOptions) { o.BatchSize = 64 })
	ctx := context.Background()
	now := time.Now()
	const n = 4500 // > pruneChunk so chunking is exercised
	for i := 1; i <= n; i++ {
		s.Enqueue(done(mkFlow(flow.ID(i), "h", "GET", "/", 200, now.Add(time.Duration(i)*time.Millisecond))))
		if i%1000 == 0 {
			flush(t, s)
		}
	}
	flush(t, s)
	st, err := s.Prune(ctx, 0, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.ByCount != n-100 || st.Flows != n-100 {
		t.Fatalf("stats: %+v", st)
	}
	if id, _ := s.MaxID(); id != n {
		t.Fatalf("newest flow pruned: max id %d", id)
	}
	if _, err := s.Get(ctx, n-100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("flow %d should be gone: %v", n-100, err)
	}
	if _, err := s.Get(ctx, n-99); err != nil {
		t.Fatalf("flow %d should remain: %v", n-99, err)
	}
	// No-op when under the limit or when limits are zero.
	if st, _ := s.Prune(ctx, 0, 100, 0); st.Flows != 0 {
		t.Fatalf("under limit: %+v", st)
	}
	if st, _ := s.Prune(ctx, 0, 0, 0); st.Flows != 0 {
		t.Fatalf("no limits: %+v", st)
	}
}

func TestPruneBySize(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	now := time.Now()
	for i := 1; i <= 40; i++ {
		body := make([]byte, 64<<10)
		_, _ = rand.Read(body) // incompressible, unique
		s.PutBlob(Hash(body), body)
		f := mkFlow(flow.ID(i), "h", "GET", "/", 200, now.Add(time.Duration(i)*time.Second))
		f.RespBody = flow.BodyRef{Hash: Hash(body), Size: int64(len(body)), Captured: int64(len(body)), MIME: "application/octet-stream"}
		s.Enqueue(done(f))
	}
	flush(t, s)
	// Make sure the main file (not just the WAL) reflects the data.
	if err := s.vacuum(ctx); err != nil {
		t.Fatal(err)
	}
	before := fileSize(s.opts.Path)
	if before < 40*64<<10 {
		t.Fatalf("db unexpectedly small: %d", before)
	}
	limit := before / 2
	st, err := s.Prune(ctx, 0, 0, limit)
	if err != nil {
		t.Fatal(err)
	}
	if st.BySize == 0 || st.Blobs == 0 {
		t.Fatalf("stats: %+v", st)
	}
	if st.DBBytes > limit {
		t.Fatalf("db still %d > %d after prune (%+v)", st.DBBytes, limit, st)
	}
	if st.DBBytes != fileSize(s.opts.Path) {
		t.Fatalf("DBBytes %d != file %d", st.DBBytes, fileSize(s.opts.Path))
	}
	if id, _ := s.MaxID(); id != 40 {
		t.Fatalf("newest pruned: %d", id)
	}
	if st2, _ := s.Stats(ctx); st2["flows"] != st2["blobs"] {
		t.Fatalf("blob/flow mismatch: %v", st2)
	}
}

func TestStartPruner(t *testing.T) {
	s := openTest(t, nil)
	now := time.Now()
	for i := 1; i <= 10; i++ {
		s.Enqueue(done(mkFlow(flow.ID(i), "h", "GET", "/", 200, now.Add(-time.Duration(i)*time.Hour))))
	}
	flush(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartPruner(ctx, 20*time.Millisecond, 5*time.Hour, 0, 0)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := s.Stats(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st["flows"] == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pruner did not run: %v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	// Closing with a running pruner (ctx not cancelled) must not hang.
	s.StartPruner(context.Background(), time.Hour, 0, 0, 0)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
