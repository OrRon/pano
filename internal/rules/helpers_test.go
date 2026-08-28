package rules

import (
	"bytes"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func newEngine(t testing.TB, opts Options) *Engine {
	t.Helper()
	e, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func mustAdd(t testing.TB, e *Engine, r api.Rule) api.Rule {
	t.Helper()
	out, err := e.Add(api.RuleAddRequest{Rule: &r})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return out
}

func newReq(t testing.TB, method, rawURL, body string, hdr map[string]string) *http.Request {
	t.Helper()
	var rc io.Reader
	if body != "" {
		rc = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, rawURL, rc)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func newFlow(r *http.Request) *flow.Flow {
	host, port := r.URL.Hostname(), 443
	if r.URL.Scheme == "http" {
		port = 80
	}
	if p := r.URL.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	return &flow.Flow{
		ID: 42, Kind: flow.KindHTTP, Scheme: r.URL.Scheme, Host: host, Port: port,
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, ReqHeaders: r.Header.Clone(),
		State: flow.StateActive, T: flow.Timing{Start: time.Now()},
	}
}

func newResp(status int, body string, hdr map[string]string) *http.Response {
	resp := &http.Response{
		Status: strconv.Itoa(status) + " " + http.StatusText(status), StatusCode: status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)),
	}
	for k, v := range hdr {
		resp.Header.Set(k, v)
	}
	return resp
}

func readAll(t testing.TB, rc io.Reader) string {
	t.Helper()
	if rc == nil {
		return ""
	}
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func gzipString(s string) string {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return buf.String()
}

func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func hostPort(r *http.Request) string {
	h, p, err := net.SplitHostPort(r.URL.Host)
	if err != nil {
		return r.URL.Host
	}
	return h + ":" + p
}
