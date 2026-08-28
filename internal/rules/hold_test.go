package rules

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/proxy"
)

type eventLog struct {
	mu     sync.Mutex
	events []flow.Event
}

func (l *eventLog) publish(ev flow.Event) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.mu.Unlock()
}

func (l *eventLog) list() []flow.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]flow.Event(nil), l.events...)
}

// runHeld evaluates the given phase in the background and waits until the
// flow is parked.
func runHeld(t *testing.T, e *Engine, ctx context.Context, f *flow.Flow, r *http.Request, resp *http.Response) <-chan proxy.Decision {
	t.Helper()
	out := make(chan proxy.Decision, 1)
	go func() {
		if resp == nil {
			out <- e.Request(ctx, f, r)
		} else {
			out <- e.Response(ctx, f, r, resp)
		}
	}()
	waitFor(t, "held entry", func() bool { return len(e.Held()) == 1 })
	return out
}

func TestHoldResumeWithRequestEdits(t *testing.T) {
	log := &eventLog{}
	e := newEngine(t, Options{Publish: log.publish})
	rule := mustAdd(t, e, api.Rule{Match: api.Match{Host: "api.test"}, Actions: []api.Action{{Type: "breakpoint"}, {Type: "tag", Tags: []string{"after"}}}})
	r := newReq(t, "POST", "https://api.test/v1/chat", `{"model":"gpt-4o","n":1}`, map[string]string{"Content-Type": "application/json", "Authorization": "secret"})
	f := newFlow(r)
	done := runHeld(t, e, context.Background(), f, r, nil)

	held := e.Held()[0]
	if held.ID != f.ID || held.Phase != "request" || held.Method != "POST" || held.URL != "https://api.test/v1/chat" || held.RuleID != rule.ID || held.Short != f.ID.Short() || held.Age == "" {
		t.Errorf("held %+v", held)
	}
	evs := log.list()
	if len(evs) != 1 || evs[0].Type != flow.EvHeld || evs[0].Phase != "request" || evs[0].Flow.State != flow.StateHeld || evs[0].Flow.ID != f.ID {
		t.Errorf("events %+v", evs)
	}

	body := `{"model":"gpt-4o-mini"}`
	err := e.Resume(f.ID, api.ResumeRequest{
		URL: "https://other.test/v2/chat?x=1", Method: "put",
		SetHeaders: map[string]string{"X-Edit": "1"}, RemoveHeaders: []string{"authorization"},
		Body: &body, BodyPatch: map[string]any{"n": 2},
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	d := <-done
	if d.Mock != nil || d.Block != "" {
		t.Errorf("decision %+v", d)
	}
	if r.URL.String() != "https://other.test/v2/chat?x=1" || r.Host != "other.test" || r.Method != "PUT" {
		t.Errorf("request %s %s %s", r.Method, r.URL, r.Host)
	}
	if r.Header.Get("X-Edit") != "1" || r.Header.Get("Authorization") != "" || f.ReqHeaders.Get("Authorization") != "" {
		t.Errorf("headers %v", r.Header)
	}
	if got := readAll(t, r.Body); got != `{"model":"gpt-4o-mini","n":2}` {
		t.Errorf("body %q", got)
	}
	if r.ContentLength != 29 || f.Method != "PUT" || f.Path != "/v2/chat" || f.Query != "x=1" {
		t.Errorf("flow %+v", f)
	}
	if f.State != flow.StateActive || len(e.Held()) != 0 {
		t.Errorf("state %s held %d", f.State, len(e.Held()))
	}
	if len(f.Rules) != 2 || f.Rules[0].Action != "breakpoint" || !strings.HasPrefix(f.Rules[0].Note, "resumed with edits: url, method") || f.Tags[0] != "after" {
		t.Errorf("hits %+v tags %v", f.Rules, f.Tags)
	}
	if err := e.Resume(f.ID, api.ResumeRequest{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("second resume: %v", err)
	}
}

func TestHoldResumePlainAndRelativeURL(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "hold"}}})
	r := newReq(t, "GET", "https://api.test/v1?a=1", "", nil)
	f := newFlow(r)
	done := runHeld(t, e, context.Background(), f, r, nil)
	if err := e.Resume(f.ID, api.ResumeRequest{Action: "resume", URL: "/v9?b=2"}); err != nil {
		t.Fatal(err)
	}
	<-done
	if r.URL.String() != "https://api.test/v9?b=2" || r.Body != http.NoBody && r.Body != nil {
		t.Errorf("url %s body %v", r.URL, r.Body)
	}

	f = newFlow(r)
	done = runHeld(t, e, context.Background(), f, r, nil)
	if err := e.Resume(f.ID, api.ResumeRequest{}); err != nil {
		t.Fatal(err)
	}
	<-done
	if f.Rules[len(f.Rules)-1].Note != "resumed" {
		t.Errorf("note %q", f.Rules[len(f.Rules)-1].Note)
	}
}

func TestHoldDrop(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "breakpoint"}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	done := runHeld(t, e, context.Background(), f, r, nil)
	if err := e.Resume(f.ID, api.ResumeRequest{Action: "DROP"}); err != nil {
		t.Fatal(err)
	}
	if d := <-done; d.Block != "reset" {
		t.Errorf("decision %+v", d)
	}
	if f.Rules[0].Note != "dropped" || f.State != flow.StateActive {
		t.Errorf("hit %+v state %s", f.Rules, f.State)
	}
}

func TestHoldTimeout(t *testing.T) {
	e := newEngine(t, Options{HoldTimeout: 30 * time.Millisecond})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "breakpoint"}, {Type: "tag", Tags: []string{"went-on"}}}})
	r := newReq(t, "GET", "https://api.test/", "hello", nil)
	f := newFlow(r)
	start := time.Now()
	d := e.Request(context.Background(), f, r)
	if time.Since(start) > 5*time.Second || d.Block != "" || d.Mock != nil {
		t.Fatalf("decision %+v after %v", d, time.Since(start))
	}
	if f.Rules[0].Note != "hold timeout" || len(f.Tags) != 1 || len(e.Held()) != 0 {
		t.Errorf("hits %+v tags %v", f.Rules, f.Tags)
	}
	if readAll(t, r.Body) != "hello" {
		t.Error("body lost after timeout")
	}
}

func TestHoldClientGone(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "breakpoint"}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	ctx, cancel := context.WithCancel(context.Background())
	done := runHeld(t, e, ctx, f, r, nil)
	cancel()
	if d := <-done; d.Block != "reset" {
		t.Errorf("decision %+v", d)
	}
	if !strings.Contains(f.Rules[0].Note, "client disconnected") || len(e.Held()) != 0 {
		t.Errorf("hits %+v", f.Rules)
	}
}

func TestHoldResponseEdits(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Match: api.Match{Status: "2xx"}, Actions: []api.Action{{Type: "breakpoint", On: "response"}}})
	r := newReq(t, "GET", "https://api.test/v1", "", nil)
	f := newFlow(r)
	resp := newResp(200, gzipString(`{"choices":[{"text":"a"}]}`), map[string]string{"Content-Encoding": "gzip", "X-Old": "1"})
	f.RespBody.Encoding = "gzip"
	done := runHeld(t, e, context.Background(), f, r, resp)
	if h := e.Held()[0]; h.Phase != "response" || h.Status != 200 {
		t.Errorf("held %+v", h)
	}
	err := e.Resume(f.ID, api.ResumeRequest{
		Status: 503, SetHeaders: map[string]string{"X-New": "1"}, RemoveHeaders: []string{"X-Old"},
		BodyPatch: map[string]any{"choices.0.text": "edited"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if resp.StatusCode != 503 || resp.Status != "503 Service Unavailable" || f.Status != 503 {
		t.Errorf("status %d %q", resp.StatusCode, resp.Status)
	}
	if resp.Header.Get("X-New") != "1" || resp.Header.Get("X-Old") != "" || resp.Header.Get("Content-Encoding") != "" || f.RespBody.Encoding != "" {
		t.Errorf("headers %v", resp.Header)
	}
	if got := readAll(t, resp.Body); got != `{"choices":[{"text":"edited"}]}` {
		t.Errorf("body %q", got)
	}
	if !strings.Contains(f.Rules[0].Note, "status") || !strings.Contains(f.Rules[0].Note, "body") {
		t.Errorf("note %q", f.Rules[0].Note)
	}
}

func TestResumeValidation(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Resume(1, api.ResumeRequest{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if err := e.Resume(1, api.ResumeRequest{Action: "explode"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("action: %v", err)
	}
	if err := e.Resume(1, api.ResumeRequest{URL: "http://"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("url: %v", err)
	}
	if err := e.Resume(1, api.ResumeRequest{Status: 42}); !errors.Is(err, ErrInvalid) {
		t.Errorf("status: %v", err)
	}
}

func TestCloseReleasesHeld(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "breakpoint"}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	done := runHeld(t, e, context.Background(), f, r, nil)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if d := <-done; d.Block != "" {
		t.Errorf("decision %+v", d)
	}
	if len(e.Held()) != 0 || f.State != flow.StateActive {
		t.Error("not released")
	}
}
