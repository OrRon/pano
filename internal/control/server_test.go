package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/bus"
	"github.com/orron/pano/internal/flow"
)

// fakeBackend implements Backend with canned data.
type fakeBackend struct {
	Backend
	b     *bus.Bus
	rules []api.Rule

	mu              sync.Mutex
	attached        int
	owned, released bool
	off             bool
}

func (f *fakeBackend) life() (attached int, owned, released, off bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attached, f.owned, f.released, f.off
}

func (f *fakeBackend) Status(context.Context) api.Status {
	return api.Status{Version: "t", Capturing: true}
}
func (f *fakeBackend) Bus() *bus.Bus { return f.b }
func (f *fakeBackend) Attach(own bool) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached++
	if own {
		f.owned = true
	}
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.attached--
		if own && f.owned {
			f.released = true
		}
	}
}

func (f *fakeBackend) Disown(context.Context) { f.mu.Lock(); f.owned = false; f.mu.Unlock() }

func (f *fakeBackend) Off(context.Context) error {
	f.mu.Lock()
	f.off = true
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) Audit(string) {}
func (f *fakeBackend) ListFlows(_ context.Context, fl api.FlowFilter) (api.FlowList, error) {
	if fl.Host == "boom" {
		return api.FlowList{}, api.BadRequest("boom")
	}
	return api.FlowList{Flows: []api.FlowRow{{ID: 1, Short: "1", Host: fl.Host, Method: "GET"}}, Total: 1}, nil
}

func (f *fakeBackend) GetFlow(_ context.Context, id flow.ID, _ api.FlowQuery) (api.FlowDetail, error) {
	if id != 1 {
		return api.FlowDetail{}, api.NotFound("flow", id.Short())
	}
	return api.FlowDetail{Text: "flow one"}, nil
}

func (f *fakeBackend) Rules(context.Context) []api.Rule { return f.rules }
func (f *fakeBackend) AddRule(_ context.Context, r api.RuleAddRequest) (api.Rule, error) {
	if r.Rule == nil && r.Preset == "" {
		return api.Rule{}, api.BadRequest("rule or preset required")
	}
	rule := api.Rule{ID: "r_1", Name: r.Name}
	f.rules = append(f.rules, rule)
	return rule, nil
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeBackend) {
	t.Helper()
	fb := &fakeBackend{b: bus.New()}
	s := New(fb, nil, "")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, fb
}

func get(t *testing.T, ts *httptest.Server, path string) (int, api.Envelope[json.RawMessage]) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env api.Envelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, env
}

func TestRoutes(t *testing.T) {
	ts, _ := newTestServer(t)
	code, env := get(t, ts, "/v1/status")
	if code != 200 || !env.OK || !strings.Contains(string(env.Data), `"capturing":true`) {
		t.Fatalf("status: %d %s", code, env.Data)
	}
	code, env = get(t, ts, "/v1/flows?host=example.com&method=GET,POST&limit=5")
	if code != 200 || !strings.Contains(string(env.Data), `"host":"example.com"`) {
		t.Fatalf("flows: %d %s", code, env.Data)
	}
	code, env = get(t, ts, "/v1/flows?host=boom")
	if code != 400 || env.OK || env.Error.Code != api.CodeBadRequest {
		t.Fatalf("bad request mapping: %d %+v", code, env.Error)
	}
	code, env = get(t, ts, "/v1/flows/1")
	if code != 200 || !strings.Contains(string(env.Data), "flow one") {
		t.Fatalf("flow: %d %s", code, env.Data)
	}
	code, env = get(t, ts, "/v1/flows/zzz9")
	if code != 404 || env.Error.Code != api.CodeNotFound {
		t.Fatalf("not found mapping: %d %+v", code, env.Error)
	}
	code, env = get(t, ts, "/v1/flows/not-an-id!")
	if code != 400 {
		t.Fatalf("invalid id: %d %+v", code, env.Error)
	}
}

func TestAddRuleJSON(t *testing.T) {
	ts, fb := newTestServer(t)
	resp, err := http.Post(ts.URL+"/v1/rules", "application/json", strings.NewReader(`{"preset":"slow_network","name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(fb.rules) != 1 {
		t.Fatalf("add rule: %d %d", resp.StatusCode, len(fb.rules))
	}
	resp, err = http.Post(ts.URL+"/v1/rules", "application/json", strings.NewReader(`{not json`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("bad json: %d", resp.StatusCode)
	}
}

func TestEventsSSE(t *testing.T) {
	ts, fb := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/events?types=done", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	// Publish after the subscriber is attached.
	time.Sleep(50 * time.Millisecond)
	fb.b.Publish(flow.Event{Type: flow.EvStarted, Flow: &flow.Flow{ID: 7, Host: "a"}})
	fb.b.Publish(flow.Event{Type: flow.EvDone, Flow: &flow.Flow{ID: 7, Host: "a", Status: 200}})
	sc := bufio.NewScanner(resp.Body)
	var got []string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			got = append(got, strings.TrimPrefix(line, "event: "))
			if len(got) == 1 {
				break
			}
		}
	}
	if len(got) != 1 || got[0] != "done" {
		t.Fatalf("events %v (started must be filtered out)", got)
	}
}

func TestFilterFromQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/flows?q=x&host=h&status=4xx&since=15m&has_error=1&min_bytes=10&method=GET,POST&limit=7&cursor=before:z", nil)
	f := FilterFromQuery(r)
	if f.Q != "x" || f.Host != "h" || f.Status != "4xx" || f.Since != "15m" || !f.HasError || f.MinBytes != 10 || len(f.Method) != 2 || f.Limit != 7 || f.Cursor != "before:z" {
		t.Fatalf("%+v", f)
	}
}

// /v1/attach holds the connection and releases the backend when the client
// goes away; /v1/disown and /v1/off are one-shot.
func TestAttachDisownOff(t *testing.T) {
	ts, fb := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/attach?own=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if _, err := resp.Body.Read(buf); err != nil { // ": attached"
		t.Fatal(err)
	}
	if n, owned, _, _ := fb.life(); n != 1 || !owned {
		t.Fatalf("attached=%d owned=%v", n, owned)
	}
	cancel()
	_ = resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n, _, _, _ := fb.life(); n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, _, released, _ := fb.life(); n != 0 || !released {
		t.Fatalf("release not called: attached=%d released=%v", n, released)
	}

	fb.mu.Lock()
	fb.owned = true
	fb.mu.Unlock()
	if resp, err := http.Post(ts.URL+"/v1/disown", "application/json", nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("disown: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	if _, owned, _, _ := fb.life(); owned {
		t.Fatal("disown must clear ownership")
	}
	if resp, err := http.Post(ts.URL+"/v1/off", "application/json", nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("off: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	if _, _, _, off := fb.life(); !off {
		t.Fatal("off not forwarded")
	}
}
