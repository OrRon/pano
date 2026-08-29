package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

// Server is the control API server.
type Server struct {
	b    Backend
	log  *slog.Logger
	mux  *http.ServeMux
	http *http.Server
	ln   net.Listener
	tok  string
}

// New builds a server for the backend. token, if non-empty, is required as a
// bearer token on TCP listeners (Unix sockets rely on file permissions).
func New(b Backend, logger *slog.Logger, token string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{b: b, log: logger, mux: http.NewServeMux(), tok: token}
	s.routes()
	s.http = &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
	}
	return s
}

// Handler returns the raw handler (for tests and the MCP HTTP mount).
func (s *Server) Handler() http.Handler { return s.mux }

// ListenUnix binds a Unix socket with mode 0600, removing a stale one.
func (s *Server) ListenUnix(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		// Stale? Try to connect; if it fails, remove.
		if c, err := net.DialTimeout("unix", path, 300*time.Millisecond); err == nil {
			_ = c.Close()
			return fmt.Errorf("control: %s is in use by another daemon", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("control: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	s.ln = ln
	return nil
}

// Serve runs on the bound listener.
func (s *Server) Serve() error {
	err := s.http.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeListener serves on an additional listener (e.g. loopback TCP).
func (s *Server) ServeListener(ln net.Listener) error {
	srv := &http.Server{Handler: s.auth(s.mux), ReadHeaderTimeout: 10 * time.Second}
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.tok != "" {
			if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != s.tok {
				writeErr(w, &api.Error{Code: "unauthorized", Message: "missing or bad bearer token", Hint: "read ~/.pano/token"}, 401)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /v1/status", s.hStatus)
	m.HandleFunc("GET /v1/stats", s.hStats)
	m.HandleFunc("POST /v1/capture", s.hCapture)
	m.HandleFunc("GET /v1/sessions", s.hSessions)
	m.HandleFunc("POST /v1/sessions", s.hStartSession)
	m.HandleFunc("DELETE /v1/sessions/{id}", s.hDeleteSession)
	m.HandleFunc("GET /v1/flows", s.hFlows)
	m.HandleFunc("GET /v1/flows/diff", s.hDiff)
	m.HandleFunc("GET /v1/flows/{id}", s.hFlow)
	m.HandleFunc("GET /v1/flows/{id}/raw", s.hFlowRaw)
	m.HandleFunc("GET /v1/flows/{id}/body/{part}", s.hBody)
	m.HandleFunc("GET /v1/flows/{id}/ws", s.hWS)
	m.HandleFunc("POST /v1/flows/{id}/replay", s.hReplay)
	m.HandleFunc("GET /v1/flows/{id}/explain", s.hExplain)
	m.HandleFunc("POST /v1/tail", s.hTail)
	m.HandleFunc("GET /v1/events", s.hEvents)
	m.HandleFunc("GET /v1/rules", s.hRules)
	m.HandleFunc("GET /v1/rules/presets", s.hPresets)
	m.HandleFunc("POST /v1/rules", s.hAddRule)
	m.HandleFunc("PATCH /v1/rules/{id}", s.hUpdateRule)
	m.HandleFunc("DELETE /v1/rules/{id}", s.hRemoveRule)
	m.HandleFunc("DELETE /v1/rules", s.hRemoveAllRules)
	m.HandleFunc("GET /v1/held", s.hHeld)
	m.HandleFunc("POST /v1/held/{id}", s.hResume)
	m.HandleFunc("GET /v1/decrypt", s.hDecrypt)
	m.HandleFunc("PATCH /v1/decrypt", s.hChangeDecrypt)
	m.HandleFunc("POST /v1/har", s.hHAR)
	m.HandleFunc("GET /v1/ca.pem", s.hCA)
	m.HandleFunc("GET /v1/sysproxy", s.hSysProxy)
	m.HandleFunc("POST /v1/sysproxy", s.hSetSysProxy)
	m.HandleFunc("GET /v1/mobile", s.hMobile)
	m.HandleFunc("POST /v1/mobile", s.hSetMobile)
	m.HandleFunc("GET /v1/config", s.hConfig)
	m.HandleFunc("POST /v1/shutdown", s.hShutdown)
	m.HandleFunc("/debug/pprof/", pprof.Index)
	m.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	m.HandleFunc("/debug/pprof/profile", pprof.Profile)
	m.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	m.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Pano-Api-Version", strconv.Itoa(api.Version))
	_ = json.NewEncoder(w).Encode(api.Envelope[any]{OK: true, Data: v})
}

func writeErr(w http.ResponseWriter, e *api.Error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Envelope[any]{OK: false, Error: e})
}

func fail(w http.ResponseWriter, err error) {
	var ae *api.Error
	if errors.As(err, &ae) {
		_, st := api.CodeOf(err)
		writeErr(w, ae, st)
		return
	}
	code, st := api.CodeOf(err)
	msg := err.Error()
	for _, p := range []string{"not found: ", "bad request: ", "conflict: ", "unsupported: "} {
		msg = strings.TrimPrefix(msg, p)
	}
	writeErr(w, &api.Error{Code: code, Message: msg}, st)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 32<<20))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return api.BadRequest("invalid JSON body: %v", err)
	}
	return nil
}

func flowID(r *http.Request) (flow.ID, error) {
	id, ok := flow.ParseShort(r.PathValue("id"))
	if !ok || id == 0 {
		return 0, api.BadRequest("invalid flow id %q", r.PathValue("id"))
	}
	return id, nil
}

func qInt(r *http.Request, k string, def int) int {
	if v := r.URL.Query().Get(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func qBool(r *http.Request, k string) bool {
	v := strings.ToLower(r.URL.Query().Get(k))
	return v == "1" || v == "true" || v == "yes"
}

// FilterFromQuery parses list filters from URL query parameters.
func FilterFromQuery(r *http.Request) api.FlowFilter {
	q := r.URL.Query()
	f := api.FlowFilter{
		Q: q.Get("q"), Host: q.Get("host"), Path: q.Get("path"), Status: q.Get("status"),
		Since: q.Get("since"), Until: q.Get("until"), ContentType: q.Get("content_type"),
		HasError: qBool(r, "has_error"), Tag: q.Get("tag"), Rule: q.Get("rule"), State: q.Get("state"),
		Kind: q.Get("kind"), Session: q.Get("session"), Client: q.Get("client"), Limit: qInt(r, "limit", 0), Cursor: q.Get("cursor"),
	}
	if v := q.Get("min_bytes"); v != "" {
		f.MinBytes, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := q.Get("method"); v != "" {
		f.Method = strings.Split(v, ",")
	}
	if v := q.Get("fields"); v != "" {
		f.Fields = strings.Split(v, ",")
	}
	return f
}

// --- handlers ---

func (s *Server) hStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.b.Status(r.Context()))
}

func (s *Server) hStats(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.b.Stats(r.Context())) }

func (s *Server) hCapture(w http.ResponseWriter, r *http.Request) {
	var req api.CaptureRequest
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	st, err := s.b.Capture(r.Context(), req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, st)
}

func (s *Server) hSessions(w http.ResponseWriter, r *http.Request) {
	ss, err := s.b.Sessions(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, ss)
}

func (s *Server) hStartSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	sess, err := s.b.StartSession(r.Context(), req.Name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, sess)
}

func (s *Server) hDeleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.b.DeleteSession(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"deleted": r.PathValue("id")})
}

func (s *Server) hFlows(w http.ResponseWriter, r *http.Request) {
	list, err := s.b.ListFlows(r.Context(), FilterFromQuery(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, list)
}

func (s *Server) hFlow(w http.ResponseWriter, r *http.Request) {
	id, err := flowID(r)
	if err != nil {
		fail(w, err)
		return
	}
	q := r.URL.Query()
	fq := api.FlowQuery{Part: q.Get("part"), View: q.Get("view"), Path: q.Get("path"), MaxBytes: qInt(r, "max_bytes", 0), RevealSecrets: qBool(r, "reveal")}
	if v := q.Get("headers"); v != "" {
		b := v == "1" || v == "true"
		fq.Headers = &b
	}
	d, err := s.b.GetFlow(r.Context(), id, fq)
	if err != nil {
		fail(w, err)
		return
	}
	if fq.RevealSecrets {
		s.b.Audit(fmt.Sprintf("reveal_secrets flow=%s", id.Short()))
	}
	writeJSON(w, d)
}

func (s *Server) hFlowRaw(w http.ResponseWriter, r *http.Request) {
	id, err := flowID(r)
	if err != nil {
		fail(w, err)
		return
	}
	f, err := s.b.GetFlowRaw(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, f)
}

func (s *Server) hBody(w http.ResponseWriter, r *http.Request) {
	id, err := flowID(r)
	if err != nil {
		fail(w, err)
		return
	}
	data, mime, err := s.b.Body(r.Context(), id, r.PathValue("part"), qBool(r, "decode"))
	if err != nil {
		fail(w, err)
		return
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) hWS(w http.ResponseWriter, r *http.Request) {
	id, err := flowID(r)
	if err != nil {
		fail(w, err)
		return
	}
	ms, err := s.b.WSMessages(r.Context(), id, qInt(r, "limit", 200))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, ms)
}

func (s *Server) hReplay(w http.ResponseWriter, r *http.Request) {
	id, err := flowID(r)
	if err != nil {
		fail(w, err)
		return
	}
	var req api.ReplayRequest
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	res, err := s.b.Replay(r.Context(), id, req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a, ok1 := flow.ParseShort(q.Get("a"))
	b, ok2 := flow.ParseShort(q.Get("b"))
	if !ok1 || !ok2 {
		fail(w, api.BadRequest("a and b flow ids required"))
		return
	}
	req := api.DiffRequest{A: a, B: b, Part: q.Get("part"), Path: q.Get("path"), MaxChanges: qInt(r, "max_changes", 0)}
	if v := q.Get("ignore_headers"); v != "" {
		req.IgnoreHeaders = strings.Split(v, ",")
	}
	res, err := s.b.Diff(r.Context(), req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hExplain(w http.ResponseWriter, r *http.Request) {
	id, err := flowID(r)
	if err != nil {
		fail(w, err)
		return
	}
	q := r.URL.Query()
	req := api.ExplainRequest{MaxChars: qInt(r, "max_chars", 0), Provider: q.Get("provider")}
	if v := q.Get("include"); v != "" {
		req.Include = strings.Split(v, ",")
	}
	res, err := s.b.Explain(r.Context(), id, req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hTail(w http.ResponseWriter, r *http.Request) {
	var req api.TailRequest
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	res, err := s.b.Tail(r.Context(), req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, res)
}

// hEvents streams bus events as SSE. Query: types=started,done,... host=...
func (s *Server) hEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()

	types := map[flow.EventType]bool{}
	if v := r.URL.Query().Get("types"); v != "" {
		for _, t := range strings.Split(v, ",") {
			types[flow.EventType(strings.TrimSpace(t))] = true
		}
	}
	host := r.URL.Query().Get("host")
	sub := s.b.Bus().Subscribe(1024, func(ev flow.Event) bool {
		if len(types) > 0 && !types[ev.Type] {
			return ev.Type == flow.EvDropped
		}
		if host != "" && ev.Flow != nil && !strings.EqualFold(ev.Flow.Host, host) {
			return false
		}
		return true
	})
	defer sub.Close()

	enc := json.NewEncoder(w)
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_ = rc.Flush()
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: ", ev.Seq, ev.Type)
			_ = enc.Encode(ev)
			_, _ = io.WriteString(w, "\n")
			_ = rc.Flush()
		}
	}
}

func (s *Server) hRules(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.b.Rules(r.Context())) }

func (s *Server) hPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.b.Presets(r.Context()))
}

func (s *Server) hAddRule(w http.ResponseWriter, r *http.Request) {
	var req api.RuleAddRequest
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	rule, err := s.b.AddRule(r.Context(), req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, rule)
}

func (s *Server) hUpdateRule(w http.ResponseWriter, r *http.Request) {
	var p api.RulePatch
	if err := readJSON(r, &p); err != nil {
		fail(w, err)
		return
	}
	rule, err := s.b.UpdateRule(r.Context(), r.PathValue("id"), p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, rule)
}

func (s *Server) hRemoveRule(w http.ResponseWriter, r *http.Request) {
	if err := s.b.RemoveRule(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"removed": r.PathValue("id")})
}

func (s *Server) hRemoveAllRules(w http.ResponseWriter, r *http.Request) {
	n := s.b.RemoveAllRules(r.Context())
	writeJSON(w, map[string]any{"removed": n})
}

func (s *Server) hHeld(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.b.Held(r.Context())) }

func (s *Server) hResume(w http.ResponseWriter, r *http.Request) {
	id, err := flowID(r)
	if err != nil {
		fail(w, err)
		return
	}
	var req api.ResumeRequest
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	if err := s.b.Resume(r.Context(), id, req); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"id": id, "action": req.Action})
}

func (s *Server) hDecrypt(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.b.Decrypt(r.Context()))
}

func (s *Server) hChangeDecrypt(w http.ResponseWriter, r *http.Request) {
	var c api.DecryptChange
	if err := readJSON(r, &c); err != nil {
		fail(w, err)
		return
	}
	d, err := s.b.ChangeDecrypt(r.Context(), c)
	if err != nil {
		fail(w, err)
		return
	}
	s.b.Audit(auditDecrypt(c, d))
	writeJSON(w, d)
}

// auditDecrypt renders one audit line for a decrypt change: the source, what
// was asked, and the resulting mode.
func auditDecrypt(c api.DecryptChange, d api.Decrypt) string {
	src := c.Source
	if src == "" {
		src = "unknown"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "decrypt source=%s", src)
	if c.Mode != "" {
		fmt.Fprintf(&b, " mode=%s", c.Mode)
	}
	for _, p := range []struct {
		sign, list string
		hosts      []string
	}{{"+", "only", c.AddOnly}, {"-", "only", c.RemoveOnly}, {"+", "never", c.AddNever}, {"-", "never", c.RemoveNever}} {
		for _, h := range p.hosts {
			fmt.Fprintf(&b, " %s%s=%s", p.sign, p.list, h)
		}
	}
	fmt.Fprintf(&b, " now=%s", d.Mode)
	return b.String()
}

func (s *Server) hHAR(w http.ResponseWriter, r *http.Request) {
	var req api.HARRequest
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	res, err := s.b.HAR(r.Context(), req)
	if err != nil {
		fail(w, err)
		return
	}
	if req.RevealSecrets {
		s.b.Audit("reveal_secrets har=" + req.Path)
	}
	writeJSON(w, res)
}

func (s *Server) hCA(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	_, _ = w.Write(s.b.CAPEM(r.Context()))
}

func (s *Server) hSysProxy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.b.SysProxy(r.Context()))
}

func (s *Server) hSetSysProxy(w http.ResponseWriter, r *http.Request) {
	var req api.SysProxyRequest
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	st, err := s.b.SetSysProxy(r.Context(), req)
	if err != nil {
		fail(w, err)
		return
	}
	s.b.Audit(fmt.Sprintf("sysproxy enabled=%v", req.Enabled))
	writeJSON(w, st)
}

func (s *Server) hMobile(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.b.Mobile(r.Context()))
}

func (s *Server) hSetMobile(w http.ResponseWriter, r *http.Request) {
	var req api.MobileRequest
	if err := readJSON(r, &req); err != nil {
		fail(w, err)
		return
	}
	st, err := s.b.SetMobile(r.Context(), req)
	if err != nil {
		fail(w, err)
		return
	}
	s.b.Audit(fmt.Sprintf("mobile enabled=%v addr=%s", req.Enabled, st.Addr))
	writeJSON(w, st)
}

func (s *Server) hConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.b.Config(r.Context()))
}

func (s *Server) hShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"shutting_down": true})
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = s.b.Shutdown(context.Background())
	}()
}
