package rules

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func TestMock(t *testing.T) {
	e := newEngine(t, Options{})
	rule := mustAdd(t, e, api.Rule{Name: "m", Match: api.Match{Host: "api.test"}, Actions: []api.Action{
		{Type: "mock", Body: `{"ok":true}`},
	}})
	r := newReq(t, "GET", "https://api.test/v1", "", nil)
	f := newFlow(r)
	d := e.Request(context.Background(), f, r)
	if d.Mock == nil {
		t.Fatal("want mock")
	}
	if d.Mock.StatusCode != 200 || d.Mock.Proto != "HTTP/1.1" || d.Mock.ContentLength != 11 {
		t.Errorf("mock: %+v", d.Mock)
	}
	if ct := d.Mock.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type %q", ct)
	}
	if got := readAll(t, d.Mock.Body); got != `{"ok":true}` {
		t.Errorf("body %q", got)
	}
	if len(f.Rules) != 1 || f.Rules[0].RuleID != rule.ID || f.Rules[0].Action != "mock" || f.Rules[0].Phase != "request" || f.Rules[0].Name != "m" {
		t.Errorf("rule hits: %+v", f.Rules)
	}
	if got, _ := e.Get(rule.ID); got.Hits != 1 {
		t.Errorf("hits %d", got.Hits)
	}

	// Plain text body, explicit status and headers.
	e.RemoveAll()
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "mock", Status: 418, Body: "short and stout", Headers: map[string]string{"x-mock": "1"}}}})
	d = e.Request(context.Background(), newFlow(r), r)
	if d.Mock == nil || d.Mock.StatusCode != 418 || d.Mock.Header.Get("Content-Type") != "text/plain; charset=utf-8" || d.Mock.Header.Get("X-Mock") != "1" {
		t.Errorf("mock: %+v", d.Mock)
	}
}

func TestBlockModes(t *testing.T) {
	e := newEngine(t, Options{})
	r := newReq(t, "GET", "https://api.test/", "", nil)

	rule := mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "block"}}})
	d := e.Request(context.Background(), newFlow(r), r)
	if d.Mock == nil || d.Mock.StatusCode != 502 {
		t.Fatalf("status block: %+v", d)
	}
	if body := readAll(t, d.Mock.Body); body != `{"error":"blocked by pano rule `+rule.ID+`"}` {
		t.Errorf("body %q", body)
	}
	if d.Mock.Header.Get("Content-Type") != "application/json" {
		t.Errorf("content-type %q", d.Mock.Header.Get("Content-Type"))
	}

	e.RemoveAll()
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "block", Mode: "reset"}}})
	if d = e.Request(context.Background(), newFlow(r), r); d.Block != "reset" || d.Mock != nil {
		t.Errorf("reset: %+v", d)
	}

	e.RemoveAll()
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "block", Mode: "timeout", MS: 1500}}})
	if d = e.Request(context.Background(), newFlow(r), r); d.Block != "timeout" || d.Deadline != 1500*time.Millisecond {
		t.Errorf("timeout: %+v", d)
	}
	e.RemoveAll()
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "block", Mode: "timeout"}}})
	if d = e.Request(context.Background(), newFlow(r), r); d.Deadline != 30*time.Second {
		t.Errorf("default timeout: %+v", d)
	}

	// Block on the response side.
	e.RemoveAll()
	mustAdd(t, e, api.Rule{Match: api.Match{Status: "5xx"}, Actions: []api.Action{{Type: "block", On: "response", Mode: "reset"}}})
	if d = e.Response(context.Background(), newFlow(r), r, newResp(503, "", nil)); d.Block != "reset" {
		t.Errorf("response block: %+v", d)
	}
}

func TestDelayRespectsContext(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "delay", MS: 5000}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	e.Request(ctx, f, r)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("delay ignored cancellation: %v", el)
	}
	if len(f.Rules) != 1 || !strings.HasPrefix(f.Rules[0].Note, "cancelled") {
		t.Errorf("hits %+v", f.Rules)
	}

	e.RemoveAll()
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "delay", MS: 30, JitterMS: 10}}})
	f = newFlow(r)
	start = time.Now()
	e.Request(context.Background(), f, r)
	if el := time.Since(start); el < 30*time.Millisecond {
		t.Errorf("delay too short: %v", el)
	}
	if f.Rules[0].Note == "" {
		t.Error("want note")
	}
}

func TestSetRemoveHeader(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Match: api.Match{Phase: "both"}, Actions: []api.Action{
		{Type: "set_header", Name: "x-pano", Value: "1"},
		{Type: "remove_header", Name: "authorization"},
	}})
	r := newReq(t, "GET", "https://api.test/", "", map[string]string{"Authorization": "secret"})
	f := newFlow(r)
	e.Request(context.Background(), f, r)
	if r.Header.Get("X-Pano") != "1" || r.Header.Get("Authorization") != "" {
		t.Errorf("request headers %v", r.Header)
	}
	if f.ReqHeaders.Get("X-Pano") != "1" || f.ReqHeaders.Get("Authorization") != "" {
		t.Errorf("flow headers not mirrored: %v", f.ReqHeaders)
	}
	resp := newResp(200, "", map[string]string{"Authorization": "x"})
	e.Response(context.Background(), f, r, resp)
	if resp.Header.Get("X-Pano") != "1" || resp.Header.Get("Authorization") != "" {
		t.Errorf("response headers %v", resp.Header)
	}
	if len(f.Rules) != 4 || f.Rules[2].Phase != "response" {
		t.Errorf("hits %+v", f.Rules)
	}
}

func TestSetQuery(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "set_query", Name: "debug", Value: "1"}}})
	r := newReq(t, "GET", "https://api.test/v1?a=b", "", nil)
	f := newFlow(r)
	e.Request(context.Background(), f, r)
	if r.URL.Query().Get("debug") != "1" || r.URL.Query().Get("a") != "b" {
		t.Errorf("query %q", r.URL.RawQuery)
	}
	if f.Query != r.URL.RawQuery {
		t.Errorf("flow query %q", f.Query)
	}
}

func TestRewriteBodyJSONPatch(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "rewrite_body", JSONPatch: map[string]any{
		"model":              "gpt-4o-mini",
		"messages.0.content": "hi",
		"messages.2":         map[string]any{"role": "user", "content": "new"},
		"meta.trace.id":      7,
		"tools.0":            "t",
	}}}})
	body := `{"model":"gpt-4o","messages":[{"role":"system","content":"x"},{"role":"user","content":"y"}]}`
	r := newReq(t, "POST", "https://api.test/v1/chat", body, map[string]string{"Content-Type": "application/json"})
	f := newFlow(r)
	e.Request(context.Background(), f, r)
	got := readAll(t, r.Body)
	want := `{"messages":[{"content":"hi","role":"system"},{"content":"y","role":"user"},{"content":"new","role":"user"}],"meta":{"trace":{"id":7}},"model":"gpt-4o-mini","tools":["t"]}`
	if got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
	if r.ContentLength != int64(len(want)) || r.Header.Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Errorf("length %d header %q", r.ContentLength, r.Header.Get("Content-Length"))
	}
	if rc, err := r.GetBody(); err != nil || readAll(t, rc) != want {
		t.Error("GetBody not refreshed")
	}
	if f.Rules[0].Note != fmt.Sprintf("%d -> %d bytes", len(body), len(want)) {
		t.Errorf("note %q", f.Rules[0].Note)
	}

	// Invalid JSON is skipped with a note and the body left intact.
	r = newReq(t, "POST", "https://api.test/v1/chat", "not json", nil)
	f = newFlow(r)
	e.Request(context.Background(), f, r)
	if readAll(t, r.Body) != "not json" || !strings.HasPrefix(f.Rules[0].Note, "skipped: body is not JSON") {
		t.Errorf("note %q", f.Rules[0].Note)
	}

	// Bad array index is an error.
	if _, err := jsonPatch([]byte(`{"a":[1]}`), map[string]any{"a.5": 1}); err == nil {
		t.Error("want index error")
	}
	if _, err := jsonPatch([]byte(`{"a":"s"}`), map[string]any{"a.b": 1}); err == nil {
		t.Error("want descend error")
	}
}

func TestRewriteBodyRegex(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "rewrite_body", On: "response", Regex: `"model":"([^"]+)"`, Replace: `"model":"mocked-$1"`}}})
	r := newReq(t, "POST", "https://api.test/v1/chat", "", nil)
	f := newFlow(r)
	resp := newResp(200, `{"model":"gpt-4o","x":1}`, map[string]string{"Content-Type": "application/json"})
	e.Response(context.Background(), f, r, resp)
	if got := readAll(t, resp.Body); got != `{"model":"mocked-gpt-4o","x":1}` {
		t.Errorf("body %q", got)
	}
	if resp.ContentLength != 31 || resp.Header.Get("Content-Length") != "31" {
		t.Errorf("length %d / %q", resp.ContentLength, resp.Header.Get("Content-Length"))
	}
}

func TestRewriteBodyTemplateGzip(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{
		Type: "rewrite_body", On: "response",
		Template: `{{.Method}} {{.Host}}{{.Path}} -> {{.Status}} {{.Header "X-Req"}} | {{.Body}}`,
	}}})
	r := newReq(t, "POST", "https://api.test/v1/chat", "", nil)
	f := newFlow(r)
	f.RespBody.Encoding = "gzip"
	resp := newResp(201, gzipString("hello"), map[string]string{"Content-Encoding": "gzip", "X-Req": "yes"})
	resp.TransferEncoding = []string{"chunked"}
	e.Response(context.Background(), f, r, resp)
	want := "POST api.test/v1/chat -> 201 yes | hello"
	if got := readAll(t, resp.Body); got != want {
		t.Errorf("body %q want %q", got, want)
	}
	if resp.Header.Get("Content-Encoding") != "" || f.RespBody.Encoding != "" || resp.TransferEncoding != nil {
		t.Errorf("encoding not cleared: %v %q", resp.Header, f.RespBody.Encoding)
	}
	if resp.ContentLength != int64(len(want)) {
		t.Errorf("length %d", resp.ContentLength)
	}

	// Unsupported encodings are left alone.
	resp = newResp(200, "brotli-bytes", map[string]string{"Content-Encoding": "br"})
	f = newFlow(r)
	e.Response(context.Background(), f, r, resp)
	if readAll(t, resp.Body) != "brotli-bytes" || resp.Header.Get("Content-Encoding") != "br" {
		t.Error("body touched despite unsupported encoding")
	}
	if !strings.Contains(f.Rules[0].Note, `unsupported content-encoding "br"`) {
		t.Errorf("note %q", f.Rules[0].Note)
	}
}

func TestRewriteBodyTooLarge(t *testing.T) {
	big := strings.Repeat("x", MaxBodyBytes+1)
	r := newReq(t, "POST", "https://api.test/", big, nil)
	f := newFlow(r)
	side := requestSide(f, r)
	note := side.rewrite(func(b []byte) ([]byte, error) { return []byte("replaced"), nil })
	if !strings.Contains(note, "exceeds 8 MiB") {
		t.Errorf("note %q", note)
	}
	if got, _ := io.ReadAll(r.Body); len(got) != len(big) {
		t.Errorf("body lost: %d bytes", len(got))
	}
}

func TestRedirect(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "redirect", Upstream: "http://localhost:3000/base/"}}})
	r := newReq(t, "GET", "https://api.test/v1/x?q=1", "", nil)
	f := newFlow(r)
	e.Request(context.Background(), f, r)
	if r.URL.String() != "http://localhost:3000/base/v1/x?q=1" || r.Host != "localhost:3000" || hostPort(r) != "localhost:3000" {
		t.Errorf("url %s host %s", r.URL, r.Host)
	}
	if f.Scheme != "http" || f.Path != "/base/v1/x" {
		t.Errorf("flow %s %s", f.Scheme, f.Path)
	}
	if f.Rules[0].Note != "api.test -> http://localhost:3000" {
		t.Errorf("note %q", f.Rules[0].Note)
	}

	e.RemoveAll()
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "redirect", Upstream: "https://staging.test", PreserveHost: true}}})
	r = newReq(t, "GET", "https://api.test/v1", "", nil)
	r.Host = "api.test"
	e.Request(context.Background(), newFlow(r), r)
	if r.Host != "api.test" || r.URL.Host != "staging.test" || r.URL.Path != "/v1" {
		t.Errorf("preserve host: %s %s", r.Host, r.URL)
	}
}

func TestThrottle(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "throttle", KBps: 128}}})
	r := newReq(t, "GET", "https://api.test/big", "", nil)
	f := newFlow(r)
	body := strings.Repeat("z", 64<<10)
	resp := newResp(200, body, nil)
	e.Response(context.Background(), f, r, resp)
	start := time.Now()
	got := readAll(t, resp.Body)
	el := time.Since(start)
	if got != body {
		t.Fatalf("body corrupted: %d bytes", len(got))
	}
	// 32 KiB burst is free; the remaining 32 KiB at 128 KiB/s takes 250ms.
	if el < 200*time.Millisecond {
		t.Errorf("throttle too fast: %v", el)
	}
	if f.Rules[0].Note != "128 KB/s" {
		t.Errorf("note %q", f.Rules[0].Note)
	}

	// Cancelling the context unblocks a throttled read.
	ctx, cancel := context.WithCancel(context.Background())
	resp = newResp(200, body, nil)
	e.Response(ctx, newFlow(r), r, resp)
	cancel()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("want context error")
	}
}

func TestTag(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Match: api.Match{Phase: "both"}, Actions: []api.Action{{Type: "tag", Tags: []string{"llm", "slow"}}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	f.Tags = []string{"slow"}
	e.Request(context.Background(), f, r)
	e.Response(context.Background(), f, r, newResp(200, "", nil))
	if strings.Join(f.Tags, ",") != "slow,llm" {
		t.Errorf("tags %v", f.Tags)
	}
}

func TestMockEveryN(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "mock_every_n", Value: "3", Status: 429}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	var fired []int
	for i := 1; i <= 7; i++ {
		if d := e.Request(context.Background(), newFlow(r), r); d.Mock != nil {
			if d.Mock.StatusCode != 429 {
				t.Errorf("status %d", d.Mock.StatusCode)
			}
			fired = append(fired, i)
		}
	}
	if len(fired) != 2 || fired[0] != 3 || fired[1] != 6 {
		t.Errorf("fired on %v", fired)
	}
}

func TestActionOrderStopsAtMock(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Actions: []api.Action{
		{Type: "tag", Tags: []string{"a"}},
		{Type: "mock", Body: "m"},
		{Type: "tag", Tags: []string{"b"}},
	}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	if d := e.Request(context.Background(), f, r); d.Mock == nil {
		t.Fatal("want mock")
	}
	if len(f.Tags) != 1 || len(f.Rules) != 2 {
		t.Errorf("tags %v hits %v", f.Tags, f.Rules)
	}
	if f.State != flow.StateActive {
		t.Errorf("state %s", f.State)
	}
}

func TestNoRulesNoOp(t *testing.T) {
	e := newEngine(t, Options{})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	if d := e.Request(context.Background(), f, r); d.Mock != nil || d.Block != "" {
		t.Error("unexpected decision")
	}
	if d := e.Response(context.Background(), f, r, newResp(200, "", nil)); d.Mock != nil || d.Block != "" {
		t.Error("unexpected decision")
	}
	_ = f.ReqHeaders
}
