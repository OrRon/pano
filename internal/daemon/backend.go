package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/bus"
	"github.com/orron/pano/internal/config"
	"github.com/orron/pano/internal/explain"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/har"
	"github.com/orron/pano/internal/proxy"
	"github.com/orron/pano/internal/store"
	"github.com/orron/pano/internal/view"
)

// Status implements control.Backend.
func (d *Daemon) Status(ctx context.Context) api.Status {
	trust := d.trust.Status(ctx, d.paths.CACert(), d.ca.Subject())
	rules := d.rules.List()
	enabled := 0
	for _, r := range rules {
		if r.Enabled == nil || *r.Enabled {
			enabled++
		}
	}
	st := api.Status{
		Version: d.opts.Version, PID: os.Getpid(), Uptime: store.FormatDuration(time.Since(d.started).Round(time.Second)),
		ProxyAddr: d.proxy.Addr(), Capturing: d.proxy.Capturing(), Session: d.currentSession(),
		Flows: d.mem.Len(), FlowsTotal: d.mem.Total(), LastFlowID: d.ids.Last(), ActiveConns: d.proxy.ActiveConns(),
		CA: api.CAStatus{
			Path: d.paths.CACert(), Subject: d.ca.Subject(), NotAfter: d.ca.NotAfter(),
			Trusted: trust.Installed, Supported: trust.Supported, Detail: trust.Detail, Warning: d.ca.ExpiryWarning(),
		},
		SystemProxy: d.SysProxy(ctx), Rules: len(rules), RulesEnabled: enabled, Held: len(d.rules.Held()),
		Persist: d.db != nil, Bypass: d.proxy.Bypass(), Redaction: d.cfg.Redaction.Enabled, BusSeq: d.bus.Seq(), StartedAt: d.started,
	}
	if d.mcpLn != nil {
		st.MCPAddr = d.mcpLn.Addr().String()
	}
	if d.db != nil {
		st.Dropped = d.db.Dropped()
		if s, err := d.db.Stats(ctx); err == nil {
			if n, ok := s["flows"]; ok {
				st.FlowsTotal = n
			}
		}
	}
	return st
}

// Stats implements control.Backend.
func (d *Daemon) Stats(ctx context.Context) api.Stats {
	s := api.Stats{"mem_flows": int64(d.mem.Len()), "mem_total": d.mem.Total(), "active_conns": int64(d.proxy.ActiveConns()), "bus_seq": int64(d.bus.Seq())} //nolint:gosec // counters
	if d.db != nil {
		if m, err := d.db.Stats(ctx); err == nil {
			for k, v := range m {
				s["db_"+k] = v
			}
		}
	}
	return s
}

// Bus implements control.Backend.
func (d *Daemon) Bus() *bus.Bus { return d.bus }

func (d *Daemon) currentSession() string {
	s, _ := d.session.Load().(string)
	return s
}

// Capture implements control.Backend.
func (d *Daemon) Capture(ctx context.Context, req api.CaptureRequest) (api.Status, error) {
	switch req.Action {
	case "start":
		d.proxy.SetCapturing(true)
	case "stop":
		d.proxy.SetCapturing(false)
	case "clear":
		d.mem.Clear()
	case "session":
		if req.Name == "" {
			return api.Status{}, api.BadRequest("session name required")
		}
		if _, err := d.StartSession(ctx, req.Name); err != nil {
			return api.Status{}, err
		}
	default:
		return api.Status{}, api.BadRequest("action must be start|stop|clear|session")
	}
	return d.Status(ctx), nil
}

// Sessions implements control.Backend.
func (d *Daemon) Sessions(ctx context.Context) ([]api.Session, error) {
	if d.db == nil {
		return []api.Session{{ID: d.currentSession(), Name: d.currentSession(), StartedAt: d.started, Flows: d.mem.Len(), Current: true}}, nil
	}
	return d.db.Sessions(ctx)
}

// StartSession implements control.Backend.
func (d *Daemon) StartSession(ctx context.Context, name string) (api.Session, error) {
	if d.db == nil {
		d.session.Store(name)
		return api.Session{ID: name, Name: name, StartedAt: time.Now(), Current: true}, nil
	}
	s, err := d.db.StartSession(ctx, name)
	if err != nil {
		return s, err
	}
	d.session.Store(s.ID)
	return s, nil
}

// DeleteSession implements control.Backend.
func (d *Daemon) DeleteSession(ctx context.Context, id string) error {
	if d.db == nil {
		return api.ErrUnsupported
	}
	if id == d.currentSession() {
		return api.BadRequest("cannot delete the current session")
	}
	return d.db.DeleteSession(ctx, id)
}

// ListFlows implements control.Backend.
func (d *Daemon) ListFlows(ctx context.Context, f api.FlowFilter) (api.FlowList, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = d.cfg.Views.ListPageSize
	}
	useDB := d.db != nil && (f.Q != "" || f.Session != "" && f.Session != d.currentSession())
	if !useDB {
		list := store.Query(d.mem, f, limit, time.Now())
		if list.Total > 0 || d.db == nil {
			return list, nil
		}
		useDB = true
	}
	if useDB {
		return d.db.Query(ctx, f, limit, time.Now())
	}
	return api.FlowList{}, nil
}

func (d *Daemon) getFlow(ctx context.Context, id flow.ID) (*flow.Flow, error) {
	if f, ok := d.mem.Get(id); ok {
		return f, nil
	}
	if d.db != nil {
		f, err := d.db.Get(ctx, id)
		if err == nil {
			return f, nil
		}
	}
	return nil, api.NotFound("flow", id.Short())
}

// GetFlowRaw implements control.Backend.
func (d *Daemon) GetFlowRaw(ctx context.Context, id flow.ID) (*flow.Flow, error) {
	return d.getFlow(ctx, id)
}

func (d *Daemon) body(ref flow.BodyRef) ([]byte, bool) {
	if ref.Hash == "" {
		return nil, false
	}
	return d.blobs.Get(ref.Hash)
}

func (d *Daemon) decodedBody(ref flow.BodyRef) []byte {
	b, ok := d.body(ref)
	if !ok {
		return nil
	}
	dec, err := view.Decode(ref.Encoding, b, 0)
	if err != nil {
		return b
	}
	return dec
}

func (d *Daemon) viewOpts(q api.FlowQuery) view.Options {
	o := view.Options{
		MaxBytes: q.MaxBytes, StringTruncate: d.cfg.Views.StringTruncate, Redact: d.cfg.Redaction.Enabled, RevealSecrets: q.RevealSecrets,
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = d.cfg.Views.DefaultMaxBytes
	}
	return o
}

// GetFlow implements control.Backend.
func (d *Daemon) GetFlow(ctx context.Context, id flow.ID, q api.FlowQuery) (api.FlowDetail, error) {
	f, err := d.getFlow(ctx, id)
	if err != nil {
		return api.FlowDetail{}, err
	}
	if q.View == "" {
		q.View = api.ViewSummary
	}
	if q.Part == "" {
		q.Part = "both"
	}
	showHeaders := q.Headers == nil || *q.Headers
	opts := d.viewOpts(q)
	det := api.FlowDetail{Flow: f}
	var sb strings.Builder
	col := f.Status
	fmt.Fprintf(&sb, "%s %s %s → %d %s  %s %s  client %s", f.ID.Short(), f.Method, f.URL(), col, http.StatusText(f.Status), f.Proto, store.FormatDuration(f.T.Total()), f.Client)
	if f.Kind != flow.KindHTTP {
		fmt.Fprintf(&sb, "  kind=%s", f.Kind)
	}
	if f.State != flow.StateDone {
		fmt.Fprintf(&sb, "  state=%s", f.State)
	}
	if f.Error != "" {
		fmt.Fprintf(&sb, "\nerror: %s", f.Error)
	}
	if len(f.Rules) > 0 {
		var hits []string
		for _, h := range f.Rules {
			hits = append(hits, h.RuleID+":"+h.Action)
		}
		fmt.Fprintf(&sb, "\nrules: %s", strings.Join(hits, " "))
	}
	if !f.T.FirstByte.IsZero() {
		fmt.Fprintf(&sb, "\ntiming: ttfb %s total %s", store.FormatDuration(f.T.TTFB()), store.FormatDuration(f.T.Total()))
		if f.T.Reused {
			sb.WriteString(" (conn reused)")
		}
	}
	render := func(label string, hdr http.Header, ref flow.BodyRef) *api.RenderedPart {
		p := &api.RenderedPart{Body: ref}
		fmt.Fprintf(&sb, "\n\n== %s ==\n", label)
		if showHeaders {
			h, n := view.RedactHeaders(hdr, opts.RevealSecrets || !opts.Redact)
			p.Headers = h
			p.Redacted += n
			sb.WriteString(view.FormatHeaders(h))
			sb.WriteString("\n")
		}
		raw, ok := d.body(ref)
		if !ok {
			if ref.Size > 0 {
				fmt.Fprintf(&sb, "body: %s %dB (not captured)\n", ref.MIME, ref.Size)
			} else {
				sb.WriteString("body: none\n")
			}
			p.Rendered = ""
			return p
		}
		text, n, binary, err := view.Render(q.View, raw, ref.Encoding, ref.MIME, q.Path, opts)
		if err != nil {
			text = "body: " + err.Error()
		}
		p.Rendered, p.Binary = text, binary
		p.Redacted += n
		sb.WriteString(text)
		sb.WriteString("\n")
		return p
	}
	if q.Part == "request" || q.Part == "both" {
		det.Request = render("request", f.ReqHeaders, f.ReqBody)
	}
	if q.Part == "response" || q.Part == "both" {
		det.Response = render("response", f.RespHeaders, f.RespBody)
	}
	det.Text = strings.TrimRight(sb.String(), "\n")
	switch {
	case store.IsLLMHost(f.Host):
		det.Next = fmt.Sprintf("pano_flow_explain id=%s", f.ID.Short())
	case q.View == api.ViewSummary && f.RespBody.Captured > 0:
		det.Next = fmt.Sprintf("pano_flow id=%s view=schema  |  view=pretty path=<key>", f.ID.Short())
	case q.View != api.ViewRaw && f.RespBody.Truncated:
		det.Next = fmt.Sprintf("pano_flow id=%s view=raw max_bytes=%d", f.ID.Short(), min64(f.RespBody.Captured, 1<<20))
	}
	return det, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Body implements control.Backend.
func (d *Daemon) Body(ctx context.Context, id flow.ID, part string, decode bool) ([]byte, string, error) {
	f, err := d.getFlow(ctx, id)
	if err != nil {
		return nil, "", err
	}
	var ref flow.BodyRef
	switch part {
	case "request", "req":
		ref = f.ReqBody
	case "response", "resp":
		ref = f.RespBody
	default:
		return nil, "", api.BadRequest("part must be request or response")
	}
	b, ok := d.body(ref)
	if !ok {
		return nil, "", api.NotFound("body for flow", id.Short())
	}
	if decode && ref.Encoding != "" {
		if dec, err := view.Decode(ref.Encoding, b, 0); err == nil {
			b = dec
		}
	}
	mime := ref.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	return b, mime, nil
}

// WSMessages implements control.Backend.
func (d *Daemon) WSMessages(ctx context.Context, id flow.ID, limit int) ([]flow.WSMessage, error) {
	if d.db == nil {
		return nil, api.ErrUnsupported
	}
	return d.db.WSMessages(ctx, id, limit)
}

// Replay implements control.Backend. The request is sent through the proxy
// itself so it is captured and rules apply.
func (d *Daemon) Replay(ctx context.Context, id flow.ID, req api.ReplayRequest) (api.ReplayResult, error) {
	f, err := d.getFlow(ctx, id)
	if err != nil {
		return api.ReplayResult{}, err
	}
	if f.Kind != flow.KindHTTP {
		return api.ReplayResult{}, api.BadRequest("only HTTP flows can be replayed")
	}
	target := f.URL()
	if req.URL != "" {
		target = req.URL
	}
	method := f.Method
	if req.Method != "" {
		method = strings.ToUpper(req.Method)
	}
	body := d.decodedBody(f.ReqBody)
	if req.Body != nil {
		body = []byte(*req.Body)
	}
	if len(req.BodyPatch) > 0 {
		body, err = patchJSON(body, req.BodyPatch)
		if err != nil {
			return api.ReplayResult{}, api.BadRequest("body_patch: %v", err)
		}
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := http.NewRequestWithContext(cctx, method, target, bytes.NewReader(body))
	if err != nil {
		return api.ReplayResult{}, api.BadRequest("%v", err)
	}
	for k, vs := range f.ReqHeaders {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Content-Encoding") || strings.EqualFold(k, "Host") {
			continue
		}
		r.Header[k] = append([]string(nil), vs...)
	}
	for _, k := range req.RemoveHeaders {
		r.Header.Del(k)
	}
	for k, v := range req.SetHeaders {
		r.Header.Set(k, v)
	}
	r.Header.Set(proxy.HeaderReplayOf, id.Short())
	if req.FollowRules != nil && !*req.FollowRules {
		r.Header.Set(proxy.HeaderNoRules, "1")
	}
	if len(body) > 0 && r.Header.Get("Content-Type") == "" && f.ReqBody.MIME != "" {
		r.Header.Set("Content-Type", f.ReqBody.MIME)
	}
	pu, _ := url.Parse("http://" + d.proxy.Addr())
	tr := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: d.ca.TLSConfigForClient(), DisableCompression: true}
	defer tr.CloseIdleConnections()
	start := time.Now()
	resp, err := (&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(r)
	res := api.ReplayResult{}
	if err != nil {
		res.Error = err.Error()
		return res, nil
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	res.Status, res.Size, res.Duration = resp.StatusCode, n, store.FormatDuration(time.Since(start))
	if v := resp.Header.Get(proxy.HeaderFlowID); v != "" {
		if nid, ok := flow.ParseShort(v); ok {
			res.NewID, res.Short = nid, nid.Short()
			// Give the sink a moment to publish the final snapshot.
			for i := 0; i < 20; i++ {
				if nf, ok := d.mem.Get(nid); ok && nf.State != flow.StateActive {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if det, err := d.GetFlow(ctx, nid, api.FlowQuery{Part: "response", View: api.ViewSummary}); err == nil {
				res.Summary = det.Text
			}
		}
	}
	return res, nil
}

// patchJSON sets dotted paths in a JSON document.
func patchJSON(body []byte, patch map[string]any) ([]byte, error) {
	var doc any
	if len(bytes.TrimSpace(body)) == 0 {
		doc = map[string]any{}
	} else if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("body is not JSON: %w", err)
	}
	for path, val := range patch {
		if s, ok := val.(string); ok {
			// Allow JSON literals in string values ("123", "true", "{...}").
			var v any
			if json.Unmarshal([]byte(s), &v) == nil && !strings.HasPrefix(s, "\"") && (strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") || s == "true" || s == "false" || s == "null" || isNumber(s)) {
				val = v
			}
		}
		var err error
		doc, err = setPath(doc, strings.Split(view.NormalizePath(path), "."), val)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(doc)
}

func isNumber(s string) bool {
	return gjson.Valid(s) && gjson.Parse(s).Type == gjson.Number
}

func setPath(doc any, segs []string, val any) (any, error) {
	if len(segs) == 0 {
		return val, nil
	}
	seg := segs[0]
	switch node := doc.(type) {
	case map[string]any:
		child, _ := node[seg]
		nv, err := setPath(child, segs[1:], val)
		if err != nil {
			return nil, err
		}
		node[seg] = nv
		return node, nil
	case []any:
		var idx int
		if _, err := fmt.Sscanf(seg, "%d", &idx); err != nil || idx < 0 {
			return nil, fmt.Errorf("array index expected at %q", seg)
		}
		for len(node) <= idx {
			node = append(node, nil)
		}
		nv, err := setPath(node[idx], segs[1:], val)
		if err != nil {
			return nil, err
		}
		node[idx] = nv
		return node, nil
	case nil:
		if _, err := fmt.Sscanf(seg, "%d", new(int)); err == nil {
			return setPath([]any{}, segs, val)
		}
		return setPath(map[string]any{}, segs, val)
	default:
		return nil, fmt.Errorf("cannot descend into %T at %q", doc, seg)
	}
}

// Diff implements control.Backend.
func (d *Daemon) Diff(ctx context.Context, req api.DiffRequest) (api.DiffResult, error) {
	a, err := d.getFlow(ctx, req.A)
	if err != nil {
		return api.DiffResult{}, err
	}
	b, err := d.getFlow(ctx, req.B)
	if err != nil {
		return api.DiffResult{}, err
	}
	part := req.Part
	if part == "" {
		part = "response"
	}
	var sb strings.Builder
	total := 0
	fmt.Fprintf(&sb, "a: %s %s %s → %d\nb: %s %s %s → %d\n", a.ID.Short(), a.Method, a.URL(), a.Status, b.ID.Short(), b.Method, b.URL(), b.Status)
	if a.Status != b.Status {
		fmt.Fprintf(&sb, "~ status: %d → %d\n", a.Status, b.Status)
		total++
	}
	if a.URL() != b.URL() {
		fmt.Fprintf(&sb, "~ url: %s → %s\n", a.URL(), b.URL())
		total++
	}
	diffPart := func(label string, ha, hb http.Header, ra, rb flow.BodyRef) {
		fmt.Fprintf(&sb, "\n== %s headers ==\n", label)
		ra1, n1 := view.RedactHeaders(ha, !d.cfg.Redaction.Enabled)
		rb1, n2 := view.RedactHeaders(hb, !d.cfg.Redaction.Enabled)
		_ = n1 + n2
		t, n := view.DiffHeaders(ra1, rb1, req.IgnoreHeaders)
		total += n
		if n == 0 {
			sb.WriteString("(no changes)\n")
		} else {
			sb.WriteString(t)
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "\n== %s body ==\n", label)
		ba, bb := d.decodedBody(ra), d.decodedBody(rb)
		if req.Path != "" {
			ba = []byte(gjson.GetBytes(ba, view.NormalizePath(req.Path)).Raw)
			bb = []byte(gjson.GetBytes(bb, view.NormalizePath(req.Path)).Raw)
		}
		if bytes.Equal(ba, bb) {
			sb.WriteString("(identical)\n")
			return
		}
		if json.Valid(ba) && json.Valid(bb) {
			t, n := view.DiffJSON(ba, bb, req.MaxChanges)
			total += n
			sb.WriteString(t)
		} else {
			sa, sb2 := string(ba), string(bb)
			if len(sa) > 4096 {
				sa = sa[:4096]
			}
			if len(sb2) > 4096 {
				sb2 = sb2[:4096]
			}
			t, n := view.DiffText(sa, sb2, 60)
			total += n
			sb.WriteString(t)
		}
		sb.WriteString("\n")
	}
	if part == "request" || part == "both" {
		diffPart("request", a.ReqHeaders, b.ReqHeaders, a.ReqBody, b.ReqBody)
	}
	if part == "response" || part == "both" {
		diffPart("response", a.RespHeaders, b.RespHeaders, a.RespBody, b.RespBody)
	}
	text := sb.String()
	if d.cfg.Redaction.Enabled {
		text, _ = view.RedactText(text)
	}
	return api.DiffResult{Text: strings.TrimRight(text, "\n"), Changes: total}, nil
}

// Explain implements control.Backend.
func (d *Daemon) Explain(ctx context.Context, id flow.ID, req api.ExplainRequest) (api.ExplainResult, error) {
	f, err := d.getFlow(ctx, id)
	if err != nil {
		return api.ExplainResult{}, err
	}
	reqBody, respBody := d.decodedBody(f.ReqBody), d.decodedBody(f.RespBody)
	res, err := explain.Explain(f.Host, f.Path, f.Status, f.ReqHeaders, reqBody, f.RespHeaders, respBody, explain.Options{
		Include: req.Include, MaxChars: req.MaxChars, Provider: req.Provider,
	})
	if err != nil {
		if errors.Is(err, explain.ErrNotLLM) {
			det, gerr := d.GetFlow(ctx, id, api.FlowQuery{View: api.ViewSummary})
			if gerr != nil {
				return api.ExplainResult{}, gerr
			}
			return api.ExplainResult{Provider: "none", Text: "not LLM traffic; generic summary:\n" + det.Text}, nil
		}
		return api.ExplainResult{}, err
	}
	text := res.Text
	if d.cfg.Redaction.Enabled {
		text, _ = view.RedactText(text)
	}
	return api.ExplainResult{Provider: res.Provider, Model: res.Model, Stream: res.Stream, Text: text, Usage: res.Usage, Partial: res.Partial}, nil
}

// Tail implements control.Backend: returns flows completed after the cursor,
// waiting up to WaitMS for the first one.
func (d *Daemon) Tail(ctx context.Context, req api.TailRequest) (api.TailResult, error) {
	var since flow.ID
	switch req.Since {
	case "", "now":
		since = d.ids.Last()
	default:
		id, ok := flow.ParseShort(req.Since)
		if !ok {
			return api.TailResult{}, api.BadRequest("since must be \"now\" or a flow id/cursor")
		}
		since = id
	}
	wait := time.Duration(req.WaitMS) * time.Millisecond
	if wait <= 0 {
		wait = 10 * time.Second
	}
	if wait > 25*time.Second {
		wait = 25 * time.Second
	}
	filter := req.Filter
	filter.Cursor = ""
	filter.Since = ""
	matcher := store.Compile(filter, time.Now())
	collect := func() []api.FlowRow {
		var rows []api.FlowRow
		d.mem.Each(func(f *flow.Flow) bool {
			if f.ID <= since {
				return false
			}
			if f.State == flow.StateActive {
				return true
			}
			if matcher.Match(f) {
				rows = append(rows, store.Row(f))
			}
			return true
		})
		// newest-first → oldest-first for a tail
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		return rows
	}
	rows := collect()
	if len(rows) == 0 {
		sub := d.bus.Subscribe(256, func(ev flow.Event) bool { return ev.Type == flow.EvDone || ev.Type == flow.EvHeld })
		defer sub.Close()
		timer := time.NewTimer(wait)
		defer timer.Stop()
	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case <-timer.C:
				break loop
			case ev := <-sub.C:
				if ev.Flow != nil && ev.Flow.ID > since && matcher.Match(ev.Flow) {
					// Small settle so a burst is returned together.
					time.Sleep(50 * time.Millisecond)
					rows = collect()
					break loop
				}
			}
		}
	}
	cursor := since
	for _, r := range rows {
		if r.ID > cursor {
			cursor = r.ID
		}
	}
	if cursor == 0 {
		cursor = d.ids.Last()
	}
	return api.TailResult{Flows: rows, Cursor: cursor.Short(), Held: d.rules.Held()}, nil
}

// Rules implements control.Backend.
func (d *Daemon) Rules(context.Context) []api.Rule { return d.rules.List() }

// AddRule implements control.Backend.
func (d *Daemon) AddRule(_ context.Context, req api.RuleAddRequest) (api.Rule, error) {
	r, err := d.rules.Add(req)
	if err != nil {
		return r, api.BadRequest("%v", err)
	}
	return r, nil
}

// UpdateRule implements control.Backend.
func (d *Daemon) UpdateRule(_ context.Context, id string, p api.RulePatch) (api.Rule, error) {
	r, err := d.rules.Update(id, p)
	if err != nil {
		if _, ok := d.rules.Get(id); !ok {
			return r, api.NotFound("rule", id)
		}
		return r, api.BadRequest("%v", err)
	}
	return r, nil
}

// RemoveRule implements control.Backend.
func (d *Daemon) RemoveRule(_ context.Context, id string) error {
	if err := d.rules.Remove(id); err != nil {
		return api.NotFound("rule", id)
	}
	return nil
}

// RemoveAllRules implements control.Backend.
func (d *Daemon) RemoveAllRules(context.Context) int { return d.rules.RemoveAll() }

// Presets implements control.Backend.
func (d *Daemon) Presets(context.Context) []api.Preset {
	var out []api.Preset
	for _, p := range d.rules.Presets() {
		params := map[string]any{}
		for _, pp := range p.Params {
			params[pp.Name] = pp.Default
		}
		out = append(out, api.Preset{Name: p.Name, Description: p.Description, Params: params})
	}
	return out
}

// Held implements control.Backend.
func (d *Daemon) Held(context.Context) []api.Held { return d.rules.Held() }

// Resume implements control.Backend.
func (d *Daemon) Resume(_ context.Context, id flow.ID, req api.ResumeRequest) error {
	if err := d.rules.Resume(id, req); err != nil {
		return api.NotFound("held request", id.Short())
	}
	return nil
}

// Bypass implements control.Backend.
func (d *Daemon) Bypass(context.Context) []string { return d.proxy.Bypass() }

// SetBypass implements control.Backend.
func (d *Daemon) SetBypass(_ context.Context, globs []string) error {
	d.proxy.SetBypass(globs)
	cfg, err := config.Load(d.paths)
	if err == nil {
		cfg.Proxy.Bypass = globs
		if err := config.Save(d.paths, cfg); err != nil {
			d.log.Warn("save config", "err", err)
		}
	}
	return nil
}

// HAR implements control.Backend.
func (d *Daemon) HAR(ctx context.Context, req api.HARRequest) (api.HARResult, error) {
	switch req.Action {
	case "export":
		if req.Path == "" {
			return api.HARResult{}, api.BadRequest("path required")
		}
		matcher := store.Compile(req.Filter, time.Now())
		var flows []*flow.Flow
		d.mem.Each(func(f *flow.Flow) bool {
			if matcher.Match(f) {
				flows = append(flows, f)
			}
			return true
		})
		// oldest first
		for i, j := 0, len(flows)-1; i < j; i, j = i+1, j-1 {
			flows[i], flows[j] = flows[j], flows[i]
		}
		fh, err := os.OpenFile(req.Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return api.HARResult{}, api.BadRequest("%v", err)
		}
		defer fh.Close()
		opts := har.ExportOptions{
			Creator: "pano", Version: d.opts.Version,
			Body: func(hash string) ([]byte, bool) {
				b, ok := d.blobs.Get(hash)
				return b, ok
			},
		}
		if d.cfg.Redaction.Enabled && !req.RevealSecrets {
			opts.Redact = &har.Redactor{
				Headers: func(h http.Header) http.Header { out, _ := view.RedactHeaders(h, false); return out },
				Text:    func(s string) string { out, _ := view.RedactText(s); return out },
			}
		}
		// Bodies must be handed over decoded.
		bodyDecoded := func(hash string) ([]byte, bool) {
			b, ok := d.blobs.Get(hash)
			if !ok {
				return nil, false
			}
			enc := encodingOf(flows, hash)
			if dec, err := view.Decode(enc, b, 0); err == nil {
				return dec, true
			}
			return b, true
		}
		opts.Body = bodyDecoded
		n, err := har.Export(fh, flows, opts)
		if err != nil {
			return api.HARResult{}, err
		}
		st, _ := fh.Stat()
		var size int64
		if st != nil {
			size = st.Size()
		}
		return api.HARResult{Path: req.Path, Count: n, Bytes: size}, nil
	case "import":
		fh, err := os.Open(req.Path)
		if err != nil {
			return api.HARResult{}, api.BadRequest("%v", err)
		}
		defer fh.Close()
		items, err := har.Import(fh)
		if err != nil {
			return api.HARResult{}, api.BadRequest("parse HAR: %v", err)
		}
		for _, it := range items {
			f := it.Flow
			f.ID = d.ids.Next()
			f.Session = d.currentSession()
			f.State = flow.StateDone
			f.Tags = append(f.Tags, "imported")
			if len(it.ReqBody) > 0 {
				f.ReqBody.Hash = d.blobs.Put(it.ReqBody)
				f.ReqBody.Captured = int64(len(it.ReqBody))
				f.ReqBody.Encoding = ""
			}
			if len(it.RespBody) > 0 {
				f.RespBody.Hash = d.blobs.Put(it.RespBody)
				f.RespBody.Captured = int64(len(it.RespBody))
				f.RespBody.Encoding = ""
			}
			d.Done(f)
		}
		st, _ := fh.Stat()
		var size int64
		if st != nil {
			size = st.Size()
		}
		return api.HARResult{Path: req.Path, Count: len(items), Bytes: size}, nil
	}
	return api.HARResult{}, api.BadRequest("action must be export or import")
}

func encodingOf(flows []*flow.Flow, hash string) string {
	for _, f := range flows {
		if f.ReqBody.Hash == hash {
			return f.ReqBody.Encoding
		}
		if f.RespBody.Hash == hash {
			return f.RespBody.Encoding
		}
	}
	return ""
}

// CAPEM implements control.Backend.
func (d *Daemon) CAPEM(context.Context) []byte { return d.ca.CertPEM() }

// SysProxy implements control.Backend.
func (d *Daemon) SysProxy(ctx context.Context) api.SysProxy {
	host, port := hostPort(d.proxy.Addr())
	st, err := d.sysp.Status(ctx, host, port)
	if err != nil {
		st.Detail = err.Error()
	}
	return st
}

// SetSysProxy implements control.Backend.
func (d *Daemon) SetSysProxy(ctx context.Context, req api.SysProxyRequest) (api.SysProxy, error) {
	if req.Confirm != "yes" {
		return api.SysProxy{}, api.BadRequest("confirm must be \"yes\"")
	}
	if !d.sysp.Supported() {
		return api.SysProxy{}, fmt.Errorf("%w: system proxy is not supported on this OS; use `pano run -- <cmd>`", api.ErrUnsupported)
	}
	host, port := hostPort(d.proxy.Addr())
	if req.Enabled {
		bypass := []string{"localhost", "127.0.0.1", "*.local", "169.254/16"}
		if err := d.sysp.Enable(ctx, host, port, bypass); err != nil {
			return api.SysProxy{}, err
		}
		d.spawnWatchdog()
	} else if err := d.sysp.Disable(ctx); err != nil {
		return api.SysProxy{}, err
	}
	return d.SysProxy(ctx), nil
}

func hostPort(addr string) (string, int) {
	h, p, err := netSplit(addr)
	if err != nil {
		return "127.0.0.1", 9091
	}
	return h, p
}

// Config implements control.Backend.
func (d *Daemon) Config(context.Context) any { return d.cfg }

// Shutdown implements control.Backend.
func (d *Daemon) Shutdown(context.Context) error {
	if d.cancel != nil {
		d.cancel()
	}
	return nil
}
