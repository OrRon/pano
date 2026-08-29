package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/bus"
	"github.com/orron/pano/internal/ca"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/config"
	"github.com/orron/pano/internal/control"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/mcpserver"
	"github.com/orron/pano/internal/mobile"
	"github.com/orron/pano/internal/proxy"
	"github.com/orron/pano/internal/rules"
	"github.com/orron/pano/internal/store"
	"github.com/orron/pano/internal/sysproxy"
	"github.com/orron/pano/internal/view"
	"github.com/orron/pano/internal/watchdog"
)

// Options configure Run.
type Options struct {
	Paths   config.Paths
	Config  config.Config
	Version string
	Port    int // overrides
	MCPPort int
	Bind    string
	Logger  *slog.Logger
}

// Daemon owns every long-lived component.
type Daemon struct {
	opts    Options
	cfg     config.Config
	paths   config.Paths
	log     *slog.Logger
	started time.Time

	ca     *ca.Authority
	trust  ca.TrustStore
	bus    *bus.Bus
	mem    *store.Mem
	blobs  *store.MemBlobs
	db     *store.SQLite
	ids    *flow.IDGen
	rules  *rules.Engine
	proxy  *proxy.Server
	sysp   sysproxy.Manager
	ctl    *control.Server
	mcp    *mcpserver.Server
	mcpLn  net.Listener
	client *client.Client
	site   *mobile.Site

	life       lifecycle
	mobileMu   sync.Mutex
	mobileLn   net.Listener // LAN listener while `pano mobile` is on
	mobileLAN  mobile.LAN
	mobileLast string // address of the last closed listener

	session   atomic.Value // string
	cancel    context.CancelFunc
	ctx       context.Context // run's context; done once shutdown has begun
	wg        sync.WaitGroup
	auditMu   sync.Mutex
	decryptMu sync.Mutex // serialises ChangeDecrypt read-modify-write
	wdMu      sync.Mutex
	wdPID     int
}

// Run starts the daemon and blocks until ctx is cancelled or Shutdown is called.
func Run(ctx context.Context, opts Options) error {
	if os.Geteuid() == 0 {
		return errors.New("pano refuses to run as root")
	}
	d, err := build(opts)
	if err != nil {
		return err
	}
	return d.run(ctx)
}

func build(opts Options) (*Daemon, error) {
	cfg := opts.Config
	if opts.Port > 0 {
		cfg.Proxy.Port = opts.Port
	}
	if opts.MCPPort > 0 {
		cfg.Proxy.MCPPort = opts.MCPPort
	}
	if opts.Bind != "" {
		cfg.Proxy.Bind = opts.Bind
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	if err := opts.Paths.Ensure(); err != nil {
		return nil, err
	}
	d := &Daemon{opts: opts, cfg: cfg, paths: opts.Paths, log: logger, started: time.Now()}
	d.session.Store("default")

	var err error
	d.ca, err = ca.Load(ca.Options{
		CertFile: d.paths.CACert(), KeyFile: d.paths.CAKey(), LeafKeyFile: d.paths.LeafKey(), CacheDir: d.paths.CertCache(),
	})
	if err != nil {
		return nil, err
	}
	if from := d.ca.RotatedFrom(); from != "" {
		logger.Warn("root CA expired; generated a new one — run `pano ca install`", "old", from, "new", d.ca.Subject())
	} else if w := d.ca.ExpiryWarning(); w != "" {
		logger.Warn(w, "not_after", d.ca.NotAfter())
	}
	d.trust = ca.NewTrustStore()
	view.Extra.Headers = append([]string(nil), cfg.Redaction.ExtraHeaders...)
	for _, p := range cfg.Redaction.ExtraPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("config: redaction.extra_patterns %q: %w", p, err)
		}
		view.Extra.Patterns = append(view.Extra.Patterns, re)
	}
	d.bus = bus.New()
	d.mem = store.NewMem(cfg.Capture.RingSize)
	d.blobs = store.NewMemBlobs(256 << 20)

	var lastID flow.ID
	if cfg.Capture.Persist {
		d.db, err = store.OpenSQLite(store.SQLiteOptions{Path: d.paths.DB(), Logger: logger})
		if err != nil {
			return nil, err
		}
		d.blobs.Persister = d.db
		if id, err := d.db.MaxID(); err == nil {
			lastID = id
		}
		if s, err := d.db.CurrentSession(context.Background()); err == nil {
			d.session.Store(s.ID)
		}
	}
	d.ids = flow.NewIDGen(lastID)

	d.rules, err = rules.New(rules.Options{
		PersistPath: d.paths.RulesFile(), HoldTimeout: cfg.Breakpoints.HoldTimeout.Duration,
		Publish: d.bus.Publish, Logger: logger,
	})
	if err != nil {
		return nil, err
	}

	cert, err := mobile.ParseCertificate(d.ca.CertPEM())
	if err != nil {
		return nil, err
	}
	d.site = mobile.NewSite(mobile.SiteOptions{
		Cert: cert, Machine: mobile.MachineName(), Version: opts.Version,
		Mobile: func() api.Mobile { return d.Mobile(context.Background()) },
		Device: func(addr string) (api.Device, bool) {
			dev, ok := d.proxy.Device(addr)
			return apiDevice(dev), ok
		},
	})
	d.proxy = proxy.New(proxy.Options{
		Addr: net.JoinHostPort(cfg.Proxy.Bind, strconv.Itoa(cfg.Proxy.Port)),
		TLS:  d.ca.TLSConfig(), Sink: d, Hooks: d.rules,
		MaxBody: cfg.Capture.MaxBodyBytes, MaxInflight: cfg.Capture.MaxInflightBytes, MaxConns: cfg.Limits.MaxConns,
		Decrypt:   proxy.DecryptPolicy{Mode: proxy.DecryptMode(cfg.Decrypt.Mode), Only: cfg.Decrypt.Only, Never: cfg.Decrypt.Never},
		CaptureWS: cfg.Capture.WebSocketFrames,
		Session:   func() string { s, _ := d.session.Load().(string); return s },
		IDs:       d.ids, Logger: logger, CAPEM: d.ca.CertPEM(), Local: d.site,
	})
	d.proxy.SetCapturing(cfg.Capture.Enabled)
	d.sysp = sysproxy.New(d.paths.SysProxyState(), logger)
	d.ctl = control.New(d, logger, "")
	d.client = client.New(d.paths.Socket())
	d.mcp = mcpserver.New(d.client, opts.Version, logger)
	return d, nil
}

func (d *Daemon) run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	d.ctx, d.cancel = ctx, cancel
	defer cancel()

	// A previous daemon may have died with the system proxy on.
	if restored, err := d.sysp.RestoreStale(ctx); err != nil {
		d.log.Warn("restore stale system proxy", "err", err)
	} else if restored {
		d.log.Info("restored system proxy settings left by a previous daemon")
	}

	if err := d.proxy.Listen(); err != nil {
		return err
	}
	if err := d.ctl.ListenUnix(d.paths.Socket()); err != nil {
		return err
	}
	if d.db != nil {
		d.db.Subscribe(d.bus)
		d.db.StartPruner(ctx, time.Minute, d.cfg.Retention.MaxAge.Duration, d.cfg.Retention.MaxFlows, d.cfg.Retention.MaxDBBytes)
	}
	_ = os.WriteFile(d.paths.PIDFile(), []byte(strconv.Itoa(os.Getpid())), 0o600)

	d.wg.Add(2)
	go func() {
		defer d.wg.Done()
		if err := d.proxy.Serve(); err != nil {
			d.log.Error("proxy", "err", err)
			cancel()
		}
	}()
	go func() {
		defer d.wg.Done()
		if err := d.ctl.Serve(); err != nil {
			d.log.Error("control", "err", err)
			cancel()
		}
	}()
	if d.cfg.MCP.ExposeHTTP && d.cfg.Proxy.MCPPort > 0 {
		addr := net.JoinHostPort(d.cfg.Proxy.Bind, strconv.Itoa(d.cfg.Proxy.MCPPort))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			d.log.Warn("mcp http listen", "addr", addr, "err", err)
		} else {
			d.mcpLn = ln
			mux := http.NewServeMux()
			mux.Handle("/mcp", d.mcp.HTTPHandler())
			mux.Handle("/mcp/", d.mcp.HTTPHandler())
			srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				_ = srv.Serve(ln)
			}()
			go func() {
				<-ctx.Done()
				sctx, c := context.WithTimeout(context.Background(), 2*time.Second)
				defer c()
				_ = srv.Shutdown(sctx)
			}()
		}
	}
	d.log.Info("pano daemon started", "version", d.opts.Version, "proxy", d.proxy.Addr(), "socket", d.paths.Socket(), "pid", os.Getpid())

	<-ctx.Done()
	d.log.Info("shutting down")
	return d.shutdown()
}

// proxyDrain is how long shutdown lets in-flight requests finish before
// closing their connections.
const proxyDrain = 700 * time.Millisecond

func (d *Daemon) shutdown() error {
	sctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if d.cfg.SystemProxy.RestoreOnExit {
		if err := d.sysp.Disable(sctx); err != nil {
			d.log.Warn("restore system proxy", "err", err)
		}
	}
	_ = d.ctl.Shutdown(sctx)
	// Drain briefly, then cut: browsers hold streaming and long-poll
	// requests open for minutes, and stopping must not wait on them. The
	// system proxy is already restored, so new connections go direct.
	dctx, dcancel := context.WithTimeout(sctx, proxyDrain)
	if err := d.proxy.Shutdown(dctx); err != nil {
		_ = d.proxy.Close()
	}
	dcancel()
	if d.db != nil {
		_ = d.db.Flush(sctx)
		_ = d.db.Close()
	}
	_ = d.rules.Close()
	// The control listener unlinks its socket on Close. Only remove the pid
	// file if it is still ours: a replacement daemon may already be running.
	if b, err := os.ReadFile(d.paths.PIDFile()); err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid()) {
		_ = os.Remove(d.paths.PIDFile())
	}
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-sctx.Done():
	}
	return nil
}

// --- proxy.Sink ---

// Started implements proxy.Sink.
func (d *Daemon) Started(f *flow.Flow) {
	d.mem.Upsert(f)
	d.bus.Publish(flow.Event{Type: flow.EvStarted, Flow: f})
}

// Updated implements proxy.Sink.
func (d *Daemon) Updated(f *flow.Flow) {
	d.mem.Upsert(f)
	d.bus.Publish(flow.Event{Type: flow.EvHeaders, Flow: f})
}

// Done implements proxy.Sink.
func (d *Daemon) Done(f *flow.Flow) {
	d.mem.Upsert(f)
	d.bus.Publish(flow.Event{Type: flow.EvDone, Flow: f})
}

// Blob implements proxy.Sink.
func (d *Daemon) Blob(b []byte) string { return d.blobs.Put(b) }

// WS implements proxy.Sink.
func (d *Daemon) WS(m *flow.WSMessage) {
	d.bus.Publish(flow.Event{Type: flow.EvWS, WS: m})
}

// Audit appends a line to the audit log.
func (d *Daemon) Audit(line string) {
	d.auditMu.Lock()
	defer d.auditMu.Unlock()
	f, err := os.OpenFile(d.paths.AuditLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), line)
}

// spawnWatchdog starts the crash-restore helper once.
func (d *Daemon) spawnWatchdog() {
	d.wdMu.Lock()
	defer d.wdMu.Unlock()
	if d.wdPID != 0 {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	pid, err := watchdog.Spawn(self, os.Getpid(), d.paths.SysProxyState(), d.paths.LogFile())
	if err != nil {
		d.log.Warn("watchdog spawn", "err", err)
		return
	}
	d.wdPID = pid
	d.log.Info("watchdog started", "pid", pid)
}
