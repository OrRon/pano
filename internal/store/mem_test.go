package store

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func mkFlow(id flow.ID, session, host, path string) *flow.Flow {
	return &flow.Flow{
		ID: id, Session: session, Kind: flow.KindHTTP, Scheme: "https", Host: host, Port: 443, Method: "GET", Path: path,
		Status: 200, State: flow.StateDone, T: flow.Timing{Start: time.Now()},
	}
}

func TestMemEvictionAndDelete(t *testing.T) {
	m := NewMem(3)
	var evicted []flow.ID
	m.OnEvict = func(f *flow.Flow) { evicted = append(evicted, f.ID) }
	for i := 1; i <= 4; i++ {
		m.Upsert(mkFlow(flow.ID(i), "a", "h", "/"))
	}
	if m.Len() != 3 || m.Total() != 4 || m.Cap() != 3 {
		t.Fatalf("len=%d total=%d cap=%d", m.Len(), m.Total(), m.Cap())
	}
	if len(evicted) != 1 || evicted[0] != 1 {
		t.Fatalf("evicted = %v, want [1]", evicted)
	}
	if _, ok := m.Get(1); ok {
		t.Fatal("flow 1 should be gone")
	}
	m.Upsert(mkFlow(3, "b", "h", "/updated")) // replace in place
	if f, _ := m.Get(3); f.Path != "/updated" || m.Len() != 3 {
		t.Fatalf("upsert did not replace: %+v len=%d", f, m.Len())
	}
	n := m.Delete(func(f *flow.Flow) bool { return f.Session == "b" })
	if n != 1 || m.Len() != 2 || len(evicted) != 2 || evicted[1] != 3 {
		t.Fatalf("delete: n=%d len=%d evicted=%v", n, m.Len(), evicted)
	}
	var order []flow.ID
	m.Each(func(f *flow.Flow) bool { order = append(order, f.ID); return true })
	if fmt.Sprint(order) != "[4 2]" {
		t.Fatalf("order after delete = %v, want [4 2]", order)
	}
	if m.Newest() != 4 || m.Count(func(*flow.Flow) bool { return true }) != 2 {
		t.Fatalf("newest=%d count=%d", m.Newest(), m.Count(func(*flow.Flow) bool { return true }))
	}
	m.Upsert(mkFlow(5, "a", "h", "/"))
	m.Upsert(mkFlow(6, "a", "h", "/")) // ring full again: 2 evicted
	if len(evicted) != 3 || evicted[2] != 2 {
		t.Fatalf("evicted after refill = %v", evicted)
	}
	m.Clear()
	if m.Len() != 0 || len(evicted) != 6 {
		t.Fatalf("clear: len=%d evicted=%v", m.Len(), evicted)
	}
	if m.Delete(func(*flow.Flow) bool { return true }) != 0 {
		t.Fatal("delete on empty ring removed something")
	}
}

func TestSessionsRegistry(t *testing.T) {
	s := NewSessions()
	cur := s.CurrentID()
	list := s.List(nil)
	if len(list) != 1 || list[0].ID != cur || list[0].Name != DefaultSessionName || !list[0].Current {
		t.Fatalf("initial sessions = %+v", list)
	}
	sec := s.Start("  second ")
	if sec.Name != "second" || !sec.Current || sec.ID == cur || s.CurrentID() != sec.ID || len(sec.ID) != 8 {
		t.Fatalf("second = %+v (cur %s)", sec, s.CurrentID())
	}
	list = s.List(func(id string) int {
		if id == cur {
			return 7
		}
		return 0
	})
	if len(list) != 2 || list[0].ID != sec.ID || list[1].ID != cur || list[1].Current || list[1].EndedAt.IsZero() || list[1].Flows != 7 {
		t.Fatalf("sessions = %+v", list)
	}
	if err := s.Delete(sec.ID); !errors.Is(err, ErrCurrent) {
		t.Fatalf("delete current: %v", err)
	}
	if err := s.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete unknown: %v", err)
	}
	if err := s.Delete(cur); err != nil {
		t.Fatal(err)
	}
	if list = s.List(nil); len(list) != 1 || list[0].ID != sec.ID {
		t.Fatalf("after delete = %+v", list)
	}
	if third := s.Start(""); third.Name != DefaultSessionName {
		t.Fatalf("empty name = %+v", third)
	}
}

func TestWSLog(t *testing.T) {
	w := NewWSLog(3, 1<<20)
	for i := 1; i <= 5; i++ {
		w.Add(&flow.WSMessage{FlowID: 9, Seq: i, Dir: "c2s", Payload: []byte(fmt.Sprintf("m%d", i))})
	}
	w.Add(&flow.WSMessage{FlowID: 10, Seq: 1, Dir: "s2c"})
	w.Add(nil)
	ms := w.Messages(9, 0)
	if len(ms) != 3 || ms[0].Seq != 3 || ms[2].Seq != 5 || w.Dropped(9) != 2 {
		t.Fatalf("messages = %+v dropped=%d", ms, w.Dropped(9))
	}
	if ms = w.Messages(9, 2); len(ms) != 2 || ms[1].Seq != 4 {
		t.Fatalf("limited = %+v", ms)
	}
	if w.Messages(404, 0) != nil || w.Dropped(404) != 0 {
		t.Fatal("unknown flow should be empty")
	}
	if fl, n, _ := w.Stats(); fl != 2 || n != 4 {
		t.Fatalf("stats = %d flows %d msgs", fl, n)
	}
	w.Drop(9)
	if w.Messages(9, 0) != nil {
		t.Fatal("drop left messages")
	}
	w.Clear()
	if fl, _, b := w.Stats(); fl != 0 || b != 0 {
		t.Fatalf("clear: %d flows %d bytes", fl, b)
	}
}

func TestWSLogBudgetForgetsOldestFlows(t *testing.T) {
	w := NewWSLog(0, 300)
	big := bytes.Repeat([]byte("x"), 100)
	w.Add(&flow.WSMessage{FlowID: 1, Seq: 1, Payload: big})
	w.Add(&flow.WSMessage{FlowID: 2, Seq: 1, Payload: big})
	w.Add(&flow.WSMessage{FlowID: 3, Seq: 1, Payload: big}) // over budget: flow 1 goes
	if w.Messages(1, 0) != nil || len(w.Messages(3, 0)) != 1 {
		t.Fatal("budget eviction did not drop the oldest flow")
	}
	// The flow being written to is never evicted, even alone over budget.
	for i := 0; i < 10; i++ {
		w.Add(&flow.WSMessage{FlowID: 3, Seq: i + 2, Payload: big})
	}
	if len(w.Messages(3, 0)) != 11 || w.Messages(2, 0) != nil {
		t.Fatalf("flow 3 = %d msgs, flow 2 = %v", len(w.Messages(3, 0)), w.Messages(2, 0))
	}
}

func TestQuerySearchesBodies(t *testing.T) {
	blobs := NewMemBlobs(0)
	fetch := func(r flow.BodyRef) ([]byte, bool) { return blobs.Get(r.Hash) }
	plain := blobs.Put([]byte(`{"answer":"needle in json"}`))
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("gzipped haystack with a NEEDLE"))
	_ = zw.Close()
	zipped := blobs.Put(gz.Bytes())
	binary := blobs.Put([]byte("needle in a png"))

	m := NewMem(10)
	f1 := mkFlow(1, "s", "a.example", "/one")
	f1.RespBody = flow.BodyRef{Hash: plain, MIME: "application/json", Size: 27}
	f2 := mkFlow(2, "s", "b.example", "/two")
	f2.RespBody = flow.BodyRef{Hash: zipped, MIME: "text/plain", Encoding: "gzip", Size: int64(gz.Len())}
	f3 := mkFlow(3, "s", "c.example", "/three")
	f3.RespBody = flow.BodyRef{Hash: binary, MIME: "image/png", Size: 15}
	f4 := mkFlow(4, "s", "needle.example", "/four")
	for _, f := range []*flow.Flow{f1, f2, f3, f4} {
		m.Upsert(f)
	}
	now := time.Now()
	got := Query(m, api.FlowFilter{Q: "needle"}, 10, now, fetch)
	if got.Total != 3 || len(got.Flows) != 3 || got.Flows[0].ID != 4 || got.Flows[1].ID != 2 || got.Flows[2].ID != 1 {
		t.Fatalf("with bodies: %+v", got)
	}
	got = Query(m, api.FlowFilter{Q: "needle"}, 10, now, nil)
	if got.Total != 1 || got.Flows[0].ID != 4 {
		t.Fatalf("without bodies: %+v", got)
	}
	got = Query(m, api.FlowFilter{Q: "needle"}, 2, now, fetch)
	if got.Total != 3 || len(got.Flows) != 2 || got.Cursor != "before:"+flow.ID(2).Short() {
		t.Fatalf("paginated: %+v", got)
	}
	got = Query(m, api.FlowFilter{Q: "needle", Cursor: got.Cursor}, 2, now, fetch)
	if got.Total != 1 || got.Flows[0].ID != 1 || got.Cursor != "" {
		t.Fatalf("second page: %+v", got)
	}
	if txt, ok := BodyText(f3.RespBody, fetch); ok {
		t.Fatalf("binary body indexed: %q", txt)
	}
	if _, ok := BodyText(flow.BodyRef{Hash: "missing", MIME: "text/plain"}, fetch); ok {
		t.Fatal("missing blob indexed")
	}
	bad := blobs.Put([]byte("not gzip"))
	if _, ok := BodyText(flow.BodyRef{Hash: bad, MIME: "text/plain", Encoding: "gzip"}, fetch); ok {
		t.Fatal("undecodable body indexed")
	}
	if _, ok := BodyText(flow.BodyRef{Hash: blobs.Put([]byte{0xff, 0xfe, 'a'}), MIME: "text/plain"}, fetch); ok {
		t.Fatal("non-UTF-8 body indexed")
	}
}

func TestBodyTextCap(t *testing.T) {
	blobs := NewMemBlobs(0)
	fetch := func(r flow.BodyRef) ([]byte, bool) { return blobs.Get(r.Hash) }
	huge := append(bytes.Repeat([]byte("é"), SearchTextCap/2), []byte("tail")...)
	h := blobs.Put(huge)
	txt, ok := BodyText(flow.BodyRef{Hash: h, MIME: "text/plain"}, fetch)
	if !ok || len(txt) > SearchTextCap || len(txt) < SearchTextCap-4 {
		t.Fatalf("ok=%v len=%d", ok, len(txt))
	}
	if bytes.Contains([]byte(txt), []byte("tail")) {
		t.Fatal("cap not applied")
	}
}

func TestMemBlobsBudget(t *testing.T) {
	b := NewMemBlobs(250)
	h1 := b.Put(bytes.Repeat([]byte("a"), 100))
	h2 := b.Put(bytes.Repeat([]byte("b"), 100))
	if _, ok := b.Get(h1); !ok { // touch: h1 is now most recent
		t.Fatal("h1 missing")
	}
	h3 := b.Put(bytes.Repeat([]byte("c"), 100)) // over budget: LRU h2 goes
	if _, ok := b.Get(h2); ok {
		t.Fatal("h2 should have been evicted")
	}
	if _, ok := b.Get(h1); !ok {
		t.Fatal("h1 should survive")
	}
	if b.Put(bytes.Repeat([]byte("c"), 100)) != h3 || b.Len() != 2 || b.Bytes() != 200 {
		t.Fatalf("dedupe: len=%d bytes=%d", b.Len(), b.Bytes())
	}
	b.Clear()
	if b.Len() != 0 || b.Bytes() != 0 {
		t.Fatal("clear")
	}
}
