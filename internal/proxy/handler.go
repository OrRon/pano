package proxy

import (
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/orron/pano/internal/flow"
)

// Internal headers used by the daemon when replaying through the proxy. They
// are stripped before the request leaves pano.
const (
	HeaderReplayOf = "X-Pano-Replay-Of"
	HeaderNoRules  = "X-Pano-No-Rules"
	HeaderFlowID   = "X-Pano-Flow-Id"
)

// handleExchange is the protocol-agnostic request path shared by plain HTTP,
// HTTP/1.1-in-tunnel and HTTP/2-in-tunnel requests.
func (s *Server) handleExchange(w http.ResponseWriter, r *http.Request, scheme, hostport string, ctx context.Context) {
	if isWebSocket(r) {
		s.handleWebSocket(w, r, scheme, hostport)
		return
	}
	f := s.newFlow(r, scheme, hostport, clientAddr(ctx))
	defer f.release()

	// Outbound request.
	out := r.Clone(ctx)
	out.RequestURI = ""
	out.URL.Scheme = scheme
	out.URL.Host = hostport
	if out.Host == "" {
		out.Host = hostport
	}
	stripHop(out.Header, false)
	out.Close = false

	// Internal control headers set by the daemon's replay path.
	skipHooks := false
	if v := out.Header.Get(HeaderReplayOf); v != "" {
		out.Header.Del(HeaderReplayOf)
		f.ReqHeaders.Del(HeaderReplayOf)
		if id, ok := flow.ParseShort(v); ok {
			f.Replay, f.ReplayOf = true, id
		}
		w.Header().Set(HeaderFlowID, f.ID.Short())
	}
	if out.Header.Get(HeaderNoRules) != "" {
		out.Header.Del(HeaderNoRules)
		f.ReqHeaders.Del(HeaderNoRules)
		skipHooks = true
	}

	// Request body capture. A bodyless request (GET and friends) must go
	// upstream with no body at all: the net/http server hands us a non-nil,
	// non-NoBody sentinel even when there is no body, and wrapping that in a
	// teeReader leaves ContentLength at -1 (unknown), so Go's HTTP/2 transport
	// withholds END_STREAM on HEADERS and emits the request as a half-open
	// bodied stream. Some origins reject a GET that claims a body — LinkedIn's
	// image-resizer Cloudflare Worker throws (HTTP 500). Gate on
	// ContentLength: 0 means no body, non-zero (including -1 for chunked) means
	// there is one to capture.
	if r.ContentLength != 0 && r.Body != nil && r.Body != http.NoBody {
		out.Body = &teeReader{r: r.Body, cap: f.reqCap}
	} else {
		out.Body = http.NoBody
		out.ContentLength = 0
	}
	f.ReqBody.MIME = mediaType(r.Header.Get("Content-Type"))
	f.ReqBody.Encoding = r.Header.Get("Content-Encoding")
	s.emitStarted(f.Flow)

	// Request-phase hooks.
	if s.hooks != nil && !skipHooks {
		d := s.hooks.Request(ctx, f.Flow, out)
		if s.applyDecision(w, r, f, d) {
			return
		}
		// Hooks may have rewritten the URL/host.
		if out.URL.Host != hostport {
			hostport = out.URL.Host
			f.Host, f.Port = splitHostPort(hostport, defaultPort(scheme))
		}
	}
	if s.isSelf(out.URL.Host) {
		f.fail(w, http.StatusForbidden, "pano: refusing to proxy to itself")
		s.finishInflight(f, flow.StateFailed)
		return
	}

	// Upstream round trip with timing. Trace callbacks run on transport
	// goroutines, so they write to a guarded scratch struct that is copied
	// into the flow once RoundTrip returns.
	var tt traceTimes
	trace := &httptrace.ClientTrace{
		DNSDone:     func(httptrace.DNSDoneInfo) { tt.set(func(t *traceTimes) { t.dns = time.Now() }) },
		ConnectDone: func(_, _ string, _ error) { tt.set(func(t *traceTimes) { t.conn = time.Now() }) },
		TLSHandshakeDone: func(cs tlsState, _ error) {
			tt.set(func(t *traceTimes) { t.tls = time.Now(); t.proto = negotiated(cs) })
		},
		GotConn:              func(gi httptrace.GotConnInfo) { tt.set(func(t *traceTimes) { t.reused = gi.Reused }) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { tt.set(func(t *traceTimes) { t.wrote = time.Now() }) },
		GotFirstResponseByte: func() { tt.set(func(t *traceTimes) { t.first = time.Now() }) },
	}
	out = out.WithContext(httptrace.WithClientTrace(ctx, trace))
	resp, err := s.transport.RoundTrip(out)
	tt.apply(f.Flow)
	if err != nil {
		f.Error = upstreamError(err)
		status := http.StatusBadGateway
		if errors.Is(err, context.Canceled) {
			status = 499
		} else if isTimeout(err) {
			status = http.StatusGatewayTimeout
		}
		f.fail(w, status, "pano: "+f.Error)
		s.finishInflight(f, flow.StateFailed)
		return
	}
	defer resp.Body.Close()
	if f.UpProto == "" {
		f.UpProto = resp.Proto
	}
	f.Status = resp.StatusCode
	f.RespHeaders = resp.Header.Clone()
	f.RespBody.MIME = mediaType(resp.Header.Get("Content-Type"))
	f.RespBody.Encoding = resp.Header.Get("Content-Encoding")

	// Response-phase hooks.
	if s.hooks != nil && !skipHooks {
		d := s.hooks.Response(ctx, f.Flow, out, resp)
		if s.applyDecision(w, r, f, d) {
			return
		}
		f.Status = resp.StatusCode
		f.RespHeaders = resp.Header.Clone()
	}
	s.writeResponse(w, r, f, resp)
}

// applyDecision handles mocks and blocks. Returns true if the exchange ended.
func (s *Server) applyDecision(w http.ResponseWriter, r *http.Request, f *inflight, d Decision) bool {
	switch {
	case d.Mock != nil:
		f.Status = d.Mock.StatusCode
		f.RespHeaders = d.Mock.Header.Clone()
		f.RespBody.MIME = mediaType(d.Mock.Header.Get("Content-Type"))
		f.T.FirstByte = time.Now()
		if d.Mock.Body == nil {
			d.Mock.Body = http.NoBody
		}
		s.writeResponse(w, r, f, d.Mock)
		return true
	case d.Block == "reset":
		f.Error = "blocked: connection reset"
		s.finishInflight(f, flow.StateFailed)
		panic(http.ErrAbortHandler)
	case d.Block == "timeout":
		f.Error = "blocked: timeout"
		dl := d.Deadline
		if dl <= 0 {
			dl = 30 * time.Second
		}
		select {
		case <-r.Context().Done():
		case <-time.After(dl):
		}
		s.finishInflight(f, flow.StateFailed)
		panic(http.ErrAbortHandler)
	}
	return false
}

// writeResponse copies headers and streams the body to the client while
// capturing it.
func (s *Server) writeResponse(w http.ResponseWriter, r *http.Request, f *inflight, resp *http.Response) {
	h := w.Header()
	for k, vs := range resp.Header {
		h[k] = vs
	}
	stripHop(h, false)
	if len(resp.Trailer) > 0 {
		names := make([]string, 0, len(resp.Trailer))
		for k := range resp.Trailer {
			names = append(names, k)
		}
		h.Set("Trailer", strings.Join(names, ", "))
	}
	streamy := isStreamy(resp)
	w.WriteHeader(resp.StatusCode)
	f.T.HeadersSent = time.Now()
	s.emitUpdated(f.Flow)

	rc := http.NewResponseController(w)
	if streamy {
		_ = rc.Flush()
	}
	var werr error
	if r.Method != http.MethodHead && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
		werr = copyStream(w, rc, resp.Body, f.respCap, streamy)
	}
	for k, vs := range resp.Trailer {
		h[http.TrailerPrefix+k] = vs
		f.Trailers = resp.Trailer.Clone()
	}
	if werr != nil {
		if errors.Is(werr, context.Canceled) || isClientGone(werr) {
			f.Error = "client disconnected"
		} else {
			f.Error = "body: " + werr.Error()
		}
		s.finishInflight(f, flow.StateFailed)
		return
	}
	s.finishInflight(f, flow.StateDone)
}

func isStreamy(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		return true
	}
	if resp.ContentLength < 0 {
		return true
	}
	if strings.EqualFold(resp.Header.Get("X-Accel-Buffering"), "no") {
		return true
	}
	return strings.Contains(resp.Header.Get("Cache-Control"), "no-transform") && strings.Contains(ct, "json")
}

// inflight bundles a flow with its capture state.
type inflight struct {
	*flow.Flow
	reqCap, respCap *capture
	s               *Server
}

func (s *Server) newFlow(r *http.Request, scheme, hostport, client string) *inflight {
	f := &flow.Flow{
		ID: s.ids.Next(), Session: s.opts.Session(), Kind: flow.KindHTTP, Client: client,
		Proto: r.Proto, Scheme: scheme, Method: r.Method, Path: r.URL.Path,
		Query: r.URL.RawQuery, ReqHeaders: r.Header.Clone(), State: flow.StateActive,
		T: flow.Timing{Start: time.Now()},
	}
	if f.Path == "" {
		f.Path = "/"
	}
	f.Host, f.Port = splitHostPort(hostport, defaultPort(scheme))
	in := &inflight{Flow: f, s: s}
	in.reqCap = newCapture(s.opts.MaxBody, s.budget)
	in.respCap = newCapture(s.opts.MaxBody, s.budget)
	if !s.capturing.Load() {
		in.reqCap.off, in.respCap.off = true, true
	}
	return in
}

func (f *inflight) release() {
	f.reqCap.budget.release(f.reqCap.reserved)
	f.respCap.budget.release(f.respCap.reserved)
}

func (f *inflight) fail(w http.ResponseWriter, status int, msg string) {
	f.Status = status
	if f.RespHeaders == nil {
		f.RespHeaders = http.Header{}
	}
	f.RespHeaders.Set("Content-Type", "text/plain; charset=utf-8")
	f.RespHeaders.Set("X-Pano-Error", "1")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Pano-Error", "1")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg+"\n")
}

func defaultPort(scheme string) int {
	if scheme == "http" {
		return 80
	}
	return 443
}

func mediaType(ct string) string {
	if ct == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	}
	return mt
}

func upstreamError(err error) string {
	msg := err.Error()
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "upstream timeout: " + msg
	case strings.Contains(msg, "no such host"):
		return "dns: " + msg
	case strings.Contains(msg, "connection refused"):
		return "connection refused: " + msg
	case strings.Contains(msg, "certificate"):
		return "upstream tls: " + msg
	default:
		return "upstream: " + msg
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, context.DeadlineExceeded)
}

func isClientGone(err error) bool {
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "client disconnected") || strings.Contains(msg, "stream closed")
}

// emit helpers snapshot the flow for the sink.

func (s *Server) emitStarted(f *flow.Flow) {
	if s.sink != nil {
		s.sink.Started(f.Clone())
	}
}

func (s *Server) emitUpdated(f *flow.Flow) {
	if s.sink != nil {
		s.sink.Updated(f.Clone())
	}
}

// finish finalises captures, stores blobs, and publishes the final snapshot.
func (s *Server) finish(f *flow.Flow, st flow.State) {
	f.T.End = time.Now()
	f.State = st
	if s.sink != nil {
		s.sink.Done(f.Clone())
	}
}

// finishInflight stores captured bodies before publishing.
func (s *Server) finishInflight(f *inflight, st flow.State) {
	s.storeBody(&f.ReqBody, f.reqCap)
	s.storeBody(&f.RespBody, f.respCap)
	s.finish(f.Flow, st)
}

func (s *Server) storeBody(ref *flow.BodyRef, c *capture) {
	ref.Size = c.size
	ref.Truncated = c.truncated
	b := c.bytesAndRelease()
	ref.Captured = int64(len(b))
	if len(b) > 0 && s.sink != nil {
		ref.Hash = s.sink.Blob(b)
	}
}

// traceTimes collects httptrace timestamps safely.
type traceTimes struct {
	mu                           sync.Mutex
	dns, conn, tls, wrote, first time.Time
	proto                        string
	reused                       bool
}

func (t *traceTimes) set(fn func(*traceTimes)) {
	t.mu.Lock()
	fn(t)
	t.mu.Unlock()
}

func (t *traceTimes) apply(f *flow.Flow) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f.T.DNSDone, f.T.Connected, f.T.TLSDone = t.dns, t.conn, t.tls
	f.T.WroteReq, f.T.FirstByte, f.T.Reused = t.wrote, t.first, t.reused
	if t.proto != "" {
		f.UpProto = t.proto
	}
}
