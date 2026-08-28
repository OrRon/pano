package proxy_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/proxy"
	"github.com/orron/pano/internal/testutil"
)

// memSink collects flows.
type memSink struct {
	mu    sync.Mutex
	flows map[flow.ID]*flow.Flow
	blobs map[string][]byte
	ws    []*flow.WSMessage
	done  chan flow.ID
}

func newSink() *memSink {
	return &memSink{flows: map[flow.ID]*flow.Flow{}, blobs: map[string][]byte{}, done: make(chan flow.ID, 100)}
}

func (m *memSink) Started(f *flow.Flow) { m.put(f) }
func (m *memSink) Updated(f *flow.Flow) { m.put(f) }
func (m *memSink) Done(f *flow.Flow)    { m.put(f); m.done <- f.ID }
func (m *memSink) WS(w *flow.WSMessage) { m.mu.Lock(); m.ws = append(m.ws, w); m.mu.Unlock() }
func (m *memSink) Blob(b []byte) string {
	h := fmt.Sprintf("%x", len(b)) + "-" + fmt.Sprintf("%x", b[:min(8, len(b))])
	m.mu.Lock()
	m.blobs[h] = b
	m.mu.Unlock()
	return h
}

func (m *memSink) put(f *flow.Flow) { m.mu.Lock(); m.flows[f.ID] = f; m.mu.Unlock() }

func (m *memSink) waitDone(t *testing.T) *flow.Flow {
	t.Helper()
	select {
	case id := <-m.done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.flows[id]
	case <-time.After(5 * time.Second):
		t.Fatal("no flow completed")
		return nil
	}
}

type env struct {
	srv   *proxy.Server
	sink  *memSink
	addr  string
	pool  *x509.CertPool
	h1cli *http.Client
	h2cli *http.Client
}

func start(t *testing.T, opt func(*proxy.Options)) *env {
	t.Helper()
	a := testutil.TempCA(t)
	sink := newSink()
	opts := proxy.Options{
		Addr: "127.0.0.1:0", TLS: a.TLSConfig(), Sink: sink, CaptureWS: true, MaxBody: 64 << 10,
		UpstreamTLS: &tls.Config{InsecureSkipVerify: true}, // httptest origins are self-signed
	}
	if opt != nil {
		opt(&opts)
	}
	s := proxy.New(opts)
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	pool := testutil.Pool(a)
	return &env{
		srv: s, sink: sink, addr: s.Addr(), pool: pool,
		h1cli: testutil.ProxiedClient(s.Addr(), pool, false),
		h2cli: testutil.ProxiedClient(s.Addr(), pool, true),
	}
}

func originTLS(t *testing.T, h2 bool, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(h)
	ts.EnableHTTP2 = h2
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

func TestPlainHTTP(t *testing.T) {
	e := start(t, nil)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"path":%q,"ua":%q}`, r.URL.Path, r.Header.Get("User-Agent"))
	}))
	defer origin.Close()
	resp, err := e.h1cli.Get(origin.URL + "/hello?x=1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"path":"/hello"`) {
		t.Fatalf("bad response: %d %s", resp.StatusCode, body)
	}
	f := e.sink.waitDone(t)
	if f.Scheme != "http" || f.Method != "GET" || f.Path != "/hello" || f.Query != "x=1" || f.Status != 200 {
		t.Fatalf("flow: %+v", f)
	}
	if f.RespBody.MIME != "application/json" || f.RespBody.Size != int64(len(body)) || f.RespBody.Hash == "" {
		t.Fatalf("resp body ref: %+v", f.RespBody)
	}
	if f.T.TTFB() <= 0 || f.T.Total() < f.T.TTFB() {
		t.Fatalf("timing: %+v", f.T)
	}
}

func TestHTTPSInterceptH1AndH2(t *testing.T) {
	e := start(t, nil)
	for _, tc := range []struct {
		name  string
		h2    bool
		cli   *http.Client
		proto string
	}{
		{"h1", false, e.h1cli, "HTTP/1.1"},
		{"h2", true, e.h2cli, "HTTP/2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := originTLS(t, tc.h2, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Origin-Proto", r.Proto)
				w.Header().Set("Content-Type", "text/plain")
				b, _ := io.ReadAll(r.Body)
				fmt.Fprintf(w, "echo:%s", b)
			})
			req, _ := http.NewRequest("POST", origin.URL+"/api", strings.NewReader("payload-123"))
			req.Header.Set("Content-Type", "text/plain")
			req.Header.Set("Authorization", "Bearer sk-test")
			resp, err := tc.cli.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "echo:payload-123" {
				t.Fatalf("body %q", body)
			}
			if resp.Proto != tc.proto {
				t.Fatalf("client proto %s want %s", resp.Proto, tc.proto)
			}
			f := e.sink.waitDone(t)
			if f.Scheme != "https" || f.Proto != tc.proto || f.Method != "POST" {
				t.Fatalf("flow %+v", f)
			}
			if f.ReqBody.Size != 11 || f.ReqBody.Hash == "" {
				t.Fatalf("req body not captured: %+v", f.ReqBody)
			}
			if string(e.sink.blobs[f.ReqBody.Hash]) != "payload-123" {
				t.Fatalf("req blob %q", e.sink.blobs[f.ReqBody.Hash])
			}
			if f.ReqHeaders.Get("Authorization") != "Bearer sk-test" {
				t.Fatal("headers not captured")
			}
		})
	}
}

func TestGzipStoredRaw(t *testing.T) {
	e := start(t, nil)
	origin := originTLS(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		gz := gzip.NewWriter(w)
		gz.Write([]byte(`{"compressed":true}`))
		gz.Close()
	})
	resp, err := e.h1cli.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("encoding header lost")
	}
	f := e.sink.waitDone(t)
	if f.RespBody.Encoding != "gzip" {
		t.Fatalf("encoding %q", f.RespBody.Encoding)
	}
	if !bytes.Equal(e.sink.blobs[f.RespBody.Hash], raw) {
		t.Fatal("stored blob differs from wire bytes")
	}
	gr, _ := gzip.NewReader(bytes.NewReader(raw))
	dec, _ := io.ReadAll(gr)
	if string(dec) != `{"compressed":true}` {
		t.Fatalf("decoded %q", dec)
	}
}

func TestSSEStreamsLive(t *testing.T) {
	e := start(t, nil)
	origin := originTLS(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		rc := http.NewResponseController(w)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			rc.Flush()
			time.Sleep(60 * time.Millisecond)
		}
	})
	req, _ := http.NewRequest("GET", origin.URL+"/stream", nil)
	startAt := time.Now()
	resp, err := e.h2cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var firstAt, lastAt time.Time
	n := 0
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data:") {
			n++
			if firstAt.IsZero() {
				firstAt = time.Now()
			}
			lastAt = time.Now()
		}
	}
	if n != 5 {
		t.Fatalf("got %d chunks", n)
	}
	// If the proxy buffered, first and last chunk would arrive together.
	if lastAt.Sub(firstAt) < 150*time.Millisecond {
		t.Fatalf("SSE was buffered: first→last gap %v (start %v)", lastAt.Sub(firstAt), firstAt.Sub(startAt))
	}
	f := e.sink.waitDone(t)
	if f.RespBody.MIME != "text/event-stream" || !strings.Contains(string(e.sink.blobs[f.RespBody.Hash]), "chunk-4") {
		t.Fatalf("sse capture: %+v", f.RespBody)
	}
}

func TestBypassSplice(t *testing.T) {
	origin := originTLS(t, false, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	e := start(t, func(o *proxy.Options) { o.Bypass = []string{"127.0.0.1"} })
	// Client trusts only the origin's own cert: if pano intercepted, this would fail.
	tr := &http.Transport{
		Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: e.addr}),
		TLSClientConfig: &tls.Config{RootCAs: certPoolOf(origin), MinVersion: tls.VersionTLS12},
	}
	cli := &http.Client{Transport: tr}
	resp, err := cli.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	tr.CloseIdleConnections()
	f := e.sink.waitDone(t)
	if f.Kind != flow.KindTunnel || f.RespBody.Size == 0 {
		t.Fatalf("tunnel flow: %+v", f)
	}
}

func TestCertRejectedRecorded(t *testing.T) {
	origin := originTLS(t, false, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	e := start(t, nil)
	tr := &http.Transport{
		Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: e.addr}),
		TLSClientConfig: &tls.Config{RootCAs: certPoolOf(origin), MinVersion: tls.VersionTLS12},
	}
	_, err := (&http.Client{Transport: tr}).Get(origin.URL)
	if err == nil {
		t.Fatal("expected TLS failure")
	}
	f := e.sink.waitDone(t)
	if f.State != flow.StateFailed || !strings.Contains(f.Error, "rejected") && !strings.Contains(f.Error, "handshake") {
		t.Fatalf("flow %+v", f)
	}
}

func TestSelfLoopGuard(t *testing.T) {
	e := start(t, nil)
	resp, err := e.h1cli.Get("http://" + e.addr + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestUpstreamError(t *testing.T) {
	e := start(t, nil)
	resp, err := e.h1cli.Get("http://127.0.0.1:1/nothing")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
	f := e.sink.waitDone(t)
	if f.State != flow.StateFailed || f.Error == "" {
		t.Fatalf("flow %+v", f)
	}
}

func wsAccept(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

func certPoolOf(ts *httptest.Server) *x509.CertPool {
	return ts.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
}

func TestWebSocketSpliceAndCapture(t *testing.T) {
	e := start(t, nil)
	origin := originTLS(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "no", 400)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", wsAccept(key))
		brw.Flush()
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(brw, hdr); err != nil {
			return
		}
		plen := int(hdr[1] & 0x7f)
		mask := make([]byte, 4)
		io.ReadFull(brw, mask)
		payload := make([]byte, plen)
		io.ReadFull(brw, payload)
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		out := append([]byte{0x81, byte(len(payload))}, payload...)
		conn.Write(out)
		time.Sleep(50 * time.Millisecond)
	})
	pc, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	ou, _ := url.Parse(origin.URL)
	fmt.Fprintf(pc, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", ou.Host, ou.Host)
	br := bufio.NewReader(pc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("connect: %v %v", err, resp)
	}
	tc := tls.Client(pc, &tls.Config{ServerName: "127.0.0.1", RootCAs: e.pool, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(tc, "GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", ou.Host)
	tbr := bufio.NewReader(tc)
	up, err := http.ReadResponse(tbr, nil)
	if err != nil || up.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", err, up)
	}
	msg := []byte("hello-ws")
	frame := []byte{0x81, 0x80 | byte(len(msg)), 1, 2, 3, 4}
	for i, b := range msg {
		frame = append(frame, b^frame[2+i%4])
	}
	tc.Write(frame)
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(tbr, hdr); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, int(hdr[1]&0x7f))
	io.ReadFull(tbr, echo)
	if string(echo) != "hello-ws" {
		t.Fatalf("echo %q", echo)
	}
	tc.Close()
	f := e.sink.waitDone(t)
	if f.Kind != flow.KindWebSocket || f.Status != 101 {
		t.Fatalf("ws flow %+v", f)
	}
	e.sink.mu.Lock()
	defer e.sink.mu.Unlock()
	var c2s, s2c bool
	for _, m := range e.sink.ws {
		if string(m.Payload) == "hello-ws" {
			if m.Dir == "c2s" {
				c2s = true
			} else {
				s2c = true
			}
		}
	}
	if !c2s || !s2c {
		t.Fatalf("ws frames not captured both ways: %d msgs", len(e.sink.ws))
	}
}

// TestWebSocketHostWithoutPort covers the dial path when the Host header has
// no port (the common browser case): pano must add the scheme default.
func TestWebSocketHostWithoutPort(t *testing.T) {
	e := start(t, nil)
	// Plain-HTTP origin so the request arrives as an absolute URI with no port only if we strip it.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			http.Error(w, "no ws here", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()
	ou, _ := url.Parse(origin.URL)
	// Send an upgrade request through the proxy with the Host header lacking a port,
	// but the URL carrying it; handleExchange receives hostport from the URL.
	req, _ := http.NewRequest("GET", origin.URL+"/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Host = ou.Hostname() // no port
	resp, err := e.h1cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	f := e.sink.waitDone(t)
	if strings.Contains(f.Error, "missing port") {
		t.Fatalf("dial without default port: %s", f.Error)
	}
}

// TestBodylessRequestSetsEndStream guards against forwarding a GET as a
// half-open bodied HTTP/2 stream. The net/http server hands the proxy a
// non-nil body sentinel even for bodyless requests; if that is wrapped in a
// capture reader the upstream HEADERS frame loses END_STREAM and the request
// arrives claiming an unknown-length body, which some origins (LinkedIn's
// image Worker) reject with a 500. The origin here records what it saw.
func TestBodylessRequestSetsEndStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		h2   bool
		cli  func(e *env) *http.Client
	}{
		{"h2", true, func(e *env) *http.Client { return e.h2cli }},
		{"h1", false, func(e *env) *http.Client { return e.h1cli }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotLen int64 = -999
			var gotBody int
			ts := originTLS(t, tc.h2, func(w http.ResponseWriter, r *http.Request) {
				gotLen = r.ContentLength
				b, _ := io.ReadAll(r.Body)
				gotBody = len(b)
				w.WriteHeader(200)
			})
			e := start(t, nil)
			resp, err := tc.cli(e).Get(ts.URL + "/img")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if gotLen != 0 {
				t.Errorf("origin saw ContentLength=%d, want 0 (a bodyless GET must arrive with END_STREAM, not a half-open body)", gotLen)
			}
			if gotBody != 0 {
				t.Errorf("origin read %d body bytes on a bodyless GET", gotBody)
			}
			f := e.sink.waitDone(t)
			if f.Status != 200 {
				t.Fatalf("status %d", f.Status)
			}
			if f.ReqBody.Size != 0 {
				t.Errorf("captured request body size %d, want 0", f.ReqBody.Size)
			}
		})
	}
}

// TestRequestBodyStillCaptured makes sure the bodyless fix does not suppress
// real request bodies: a POST body must reach the origin and be captured.
func TestRequestBodyStillCaptured(t *testing.T) {
	const payload = `{"hello":"world"}`
	var got string
	ts := originTLS(t, true, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(200)
	})
	e := start(t, nil)
	resp, err := e.h2cli.Post(ts.URL+"/x", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got != payload {
		t.Fatalf("origin got %q, want %q", got, payload)
	}
	f := e.sink.waitDone(t)
	if f.ReqBody.Size != int64(len(payload)) {
		t.Errorf("captured request body size %d, want %d", f.ReqBody.Size, len(payload))
	}
}

// TestChunkedRequestBodyForwarded guards the other side of the bodyless fix:
// a real request body with unknown length (ContentLength == -1, i.e. chunked)
// must still be streamed upstream and captured. The fix gates body capture on
// ContentLength != 0, and -1 must count as "has a body".
func TestChunkedRequestBodyForwarded(t *testing.T) {
	const payload = `{"chunked":true,"n":42}`
	var got string
	ts := originTLS(t, true, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(200)
	})
	e := start(t, nil)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/x", io.NopCloser(strings.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1 // force unknown length → chunked upload
	resp, err := e.h2cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got != payload {
		t.Fatalf("origin got %q, want %q", got, payload)
	}
	f := e.sink.waitDone(t)
	if f.ReqBody.Size != int64(len(payload)) {
		t.Errorf("captured chunked request body size %d, want %d", f.ReqBody.Size, len(payload))
	}
}
