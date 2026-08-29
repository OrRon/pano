package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"

	"github.com/orron/pano/internal/flow"
)

// Sink receives capture output. Implementations must not block for long.
type Sink interface {
	// Started is called once request headers are known.
	Started(f *flow.Flow)
	// Updated is called when response headers are known or state changes.
	Updated(f *flow.Flow)
	// Done is called once with the final snapshot.
	Done(f *flow.Flow)
	// Blob stores body bytes and returns their content hash.
	Blob(b []byte) string
	// WS is called per captured WebSocket message.
	WS(m *flow.WSMessage)
}

// Decision is what a hook asks the engine to do instead of forwarding.
type Decision struct {
	// Mock, if set, is written to the client instead of contacting the origin.
	Mock *http.Response
	// Block: "" (none), "reset" (drop the connection), "timeout" (hang until
	// the client gives up or Deadline passes).
	Block string
	// Deadline bounds a "timeout" block.
	Deadline time.Duration
}

// Hooks lets a rules engine observe and mutate exchanges. Hooks may mutate
// the request/response in place (headers, body, URL) and append RuleHits to
// the flow. Both methods run synchronously on the request goroutine.
type Hooks interface {
	Request(ctx context.Context, f *flow.Flow, r *http.Request) Decision
	Response(ctx context.Context, f *flow.Flow, r *http.Request, resp *http.Response) Decision
}

// Options configure a Server.
type Options struct {
	Addr        string      // listen address, e.g. 127.0.0.1:9091
	TLS         *tls.Config // MITM server config (from ca.Authority.TLSConfig)
	Sink        Sink
	Hooks       Hooks             // optional
	Transport   http.RoundTripper // optional; NewTransport(UpstreamTLS) if nil
	UpstreamTLS *tls.Config       // optional client TLS config for origins (tests, custom roots)
	MaxBody     int64             // per-body capture cap; 0 = 4 MiB
	MaxInflight int64             // total in-flight capture budget; 0 = 256 MiB
	MaxConns    int               // concurrent tunnels; 0 = 10000
	Decrypt     DecryptPolicy     // which tunnels are TLS-terminated; zero value = mode all, no lists
	CaptureWS   bool
	Session     func() string // current session id
	IDs         *flow.IDGen
	Logger      *slog.Logger
	CAPEM       []byte // served at /_pano/ca.pem on the proxy port
	DisableH2   bool
}

// Server is the proxy.
type Server struct {
	opts      Options
	front     *http.Server
	mitm      *http.Server // serves decrypted h1 conns
	h2        *http.Server // base config for h2 ServeConn
	h2srv     *http2.Server
	transport http.RoundTripper
	sink      Sink
	hooks     Hooks
	ids       *flow.IDGen
	log       *slog.Logger
	budget    *budget
	policy    atomic.Pointer[DecryptPolicy]
	rejected  *rejectedRing
	capturing atomic.Bool
	active    atomic.Int64
	sem       chan struct{}
	listener  net.Listener
	port      int
	mu        sync.Mutex
	closed    bool
}

// New creates a server (not yet listening).
func New(opts Options) *Server {
	if opts.MaxBody == 0 {
		opts.MaxBody = 4 << 20
	}
	if opts.MaxInflight == 0 {
		opts.MaxInflight = 256 << 20
	}
	if opts.MaxConns == 0 {
		opts.MaxConns = 10000
	}
	if opts.Transport == nil {
		opts.Transport = NewTransport(opts.UpstreamTLS)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.IDs == nil {
		opts.IDs = flow.NewIDGen(0)
	}
	if opts.Session == nil {
		opts.Session = func() string { return "default" }
	}
	s := &Server{
		opts: opts, transport: opts.Transport, sink: opts.Sink, hooks: opts.Hooks,
		ids: opts.IDs, log: opts.Logger, budget: &budget{limit: opts.MaxInflight},
		sem: make(chan struct{}, opts.MaxConns), rejected: newRejectedRing(),
	}
	s.capturing.Store(true)
	if opts.Decrypt.Mode == "" {
		opts.Decrypt.Mode = DecryptAll
	}
	s.SetDecrypt(opts.Decrypt)

	s.front = &http.Server{
		Addr:              opts.Addr,
		Handler:           http.HandlerFunc(s.serveFront),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelDebug),
		ConnContext:       func(ctx context.Context, c net.Conn) context.Context { return withClient(ctx, c.RemoteAddr().String()) },
		Protocols:         h1Only(),
	}
	s.mitm = &http.Server{
		Handler:           http.HandlerFunc(s.serveTunneled),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelDebug),
		ConnContext:       tunnelConnContext,
		Protocols:         h1Only(),
	}
	s.h2 = &http.Server{
		Handler:           http.HandlerFunc(s.serveTunneled),
		ReadHeaderTimeout: 30 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelDebug),
	}
	s.h2srv = &http2.Server{
		MaxConcurrentStreams: 250,
		IdleTimeout:          120 * time.Second,
	}
	return s
}

func h1Only() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	return p
}

// Listen binds the address.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("proxy: listen %s: %w", s.opts.Addr, err)
	}
	s.listener = ln
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}
	return nil
}

// Addr returns the bound address (after Listen).
func (s *Server) Addr() string {
	if s.listener == nil {
		return s.opts.Addr
	}
	return s.listener.Addr().String()
}

// Serve runs until Shutdown.
func (s *Server) Serve() error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	err := s.front.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops accepting and waits for in-flight exchanges up to ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	err := s.front.Shutdown(ctx)
	_ = s.mitm.Shutdown(ctx)
	_ = s.h2.Shutdown(ctx)
	if t, ok := s.transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	return err
}

// SetCapturing toggles recording (the proxy keeps forwarding).
func (s *Server) SetCapturing(on bool) { s.capturing.Store(on) }

// Capturing reports whether recording is on.
func (s *Server) Capturing() bool { return s.capturing.Load() }

// SetDecrypt replaces the decrypt policy. Takes effect for the next CONNECT;
// open tunnels are unaffected. Hosts now covered by the never list are dropped
// from the rejected-host suggestions.
func (s *Server) SetDecrypt(p DecryptPolicy) {
	cp := p.Clone()
	if cp.Mode == "" {
		cp.Mode = DecryptAll
	}
	s.policy.Store(&cp)
	s.rejected.forget(cp.Never)
}

// Decrypt returns a copy of the current policy.
func (s *Server) Decrypt() DecryptPolicy { return s.policy.Load().Clone() }

// Rejected lists hosts whose clients refused pano's certificate in the last
// hour, most frequent first — candidates for the never list.
func (s *Server) Rejected() []RejectedHost { return s.rejected.list() }

// ActiveConns is the number of open tunnels/requests.
func (s *Server) ActiveConns() int { return int(s.active.Load()) }

// serveFront handles connections on the proxy port: CONNECT tunnels, absolute
// URI plain-HTTP proxying, and a tiny local site for the CA download.
func (s *Server) serveFront(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		s.handleConnect(w, r)
	case r.URL.IsAbs():
		s.active.Add(1)
		defer s.active.Add(-1)
		s.handleExchange(w, r, r.URL.Scheme, r.URL.Host, r.Context())
	default:
		s.serveLocal(w, r)
	}
}

func (s *Server) serveLocal(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_pano/ca.pem", "/ca.pem":
		if len(s.opts.CAPEM) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Content-Disposition", `attachment; filename="pano-ca.pem"`)
		_, _ = w.Write(s.opts.CAPEM)
	case "/", "/_pano", "/_pano/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>pano</title><h1>pano</h1><p>This is the pano proxy port. Configure it as an HTTP/HTTPS proxy.</p><p><a href="/_pano/ca.pem">Download CA certificate</a></p>`)
	default:
		http.Error(w, "pano: not a proxy request (use absolute URI or CONNECT)", http.StatusBadRequest)
	}
}

// serveTunneled handles requests arriving on a decrypted tunnel connection.
func (s *Server) serveTunneled(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	target := tunnelTarget(ctx)
	host := r.Host
	if host == "" {
		host = target
	}
	if r.URL.Host == "" && host != "" && target != "" && !sameHostPort(host, target) {
		// Host header disagrees with the tunnel: trust the tunnel destination
		// for routing but keep the Host header as sent.
		host = target
	}
	s.handleExchange(w, r, "https", host, ctx)
}

func sameHostPort(a, b string) bool {
	ah, ap, _ := net.SplitHostPort(a)
	bh, bp, _ := net.SplitHostPort(b)
	if ah == "" {
		ah, ap = a, "443"
	}
	if bh == "" {
		bh, bp = b, "443"
	}
	return strings.EqualFold(ah, bh) && ap == bp
}

// isSelf reports whether host:port would loop back into this proxy.
func (s *Server) isSelf(hostport string) bool {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	port, _ := strconv.Atoi(p)
	if port != s.port {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// context plumbing

type ctxKey int

const (
	ctxClient ctxKey = iota
	ctxTarget
	ctxProto
)

func withClient(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, ctxClient, addr)
}

func clientAddr(ctx context.Context) string {
	v, _ := ctx.Value(ctxClient).(string)
	return v
}

func withTunnel(ctx context.Context, target string) context.Context {
	return context.WithValue(ctx, ctxTarget, target)
}

func tunnelTarget(ctx context.Context) string {
	v, _ := ctx.Value(ctxTarget).(string)
	return v
}

func tunnelConnContext(ctx context.Context, c net.Conn) context.Context {
	if tc, ok := c.(*tls.Conn); ok {
		if m, ok := tc.NetConn().(*mitmConn); ok {
			ctx = withTunnel(ctx, m.target)
			ctx = withClient(ctx, m.RemoteAddr().String())
		}
	}
	return ctx
}
