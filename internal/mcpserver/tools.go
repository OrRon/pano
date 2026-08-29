package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/store"
)

func (s *Server) registerTools() {
	m := s.mcp

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_status",
		Description: "Proxy daemon state: capturing?, system proxy on?, proxy address, CA trusted?, flow counts, active rules and held requests. Call this first.",
		Annotations: readOnly("pano status"),
		Meta:        meta("anthropic/alwaysLoad", true),
	}, s.toolStatus)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_capture",
		Description: "Control recording: action=start|stop (pause capture, proxy keeps forwarding), clear (forget in-memory flows), session (switch to a new named session; name required). Does NOT touch macOS proxy settings.",
		Annotations: mutating("pano capture", true),
	}, s.toolCapture)

	mcp.AddTool(m, &mcp.Tool{
		Name: "pano_flows",
		Description: "List/search captured flows, newest first, ONE line each (~40 tokens/row). Filter by host glob, path prefix/glob, method, status (500, 4xx, 400-499, !2xx), since (15m, 2h, RFC3339, or a flow id), content_type (json|sse|html|js|img|bin), min_bytes, has_error, tag, rule, state (held|active|failed|replayed|mocked|blocked), or full-text q. " +
			"Columns: id time meth host path status dur up down type flags (flags: llm stream err held mock replay trunc …). Use before pano_flow.",
		Annotations: readOnly("pano flows"),
		Meta:        meta("anthropic/alwaysLoad", true),
	}, s.toolFlows)

	mcp.AddTool(m, &mcp.Tool{
		Name: "pano_flow",
		Description: "Inspect one flow. view: summary (default — type-aware digest, ~1.5 KB max), schema (inferred JSON shape), truncated (head+tail), pretty, raw (needs explicit max_bytes; hard cap 1 MiB). " +
			"path selects a JSON sub-value (gjson: choices.0.message.content, messages.#.role) so you never fetch a whole body. part: request|response|both. Secrets are redacted unless reveal_secrets=true. Binary bodies are never inlined.",
		Annotations: readOnly("pano flow"),
		Meta:        meta("anthropic/alwaysLoad", true, "anthropic/maxResultSizeChars", 200000),
	}, s.toolFlow)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_flow_diff",
		Description: "Compare two flows: URL/query, headers (noisy ones ignored), status, and a structural JSON body diff (+ added, - removed, ~ changed paths). Use to answer 'why did this one fail when that one worked'.",
		Annotations: readOnly("pano diff"),
	}, s.toolDiff)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_flow_replay",
		Description: "Re-send a captured request with optional overrides (url, method, set_headers, remove_headers, body, body_patch map of json path→value). Returns the new flow id and a summary of the response. Live rules apply unless follow_rules=false.",
		Annotations: mutating("pano replay", false),
	}, s.toolReplay)

	mcp.AddTool(m, &mcp.Tool{
		Name: "pano_flow_explain",
		Description: "LLM-traffic digest. Detects Anthropic / OpenAI (chat + responses) / Gemini requests, reassembles streaming SSE into the final message, tool calls, stop reason and token usage, and summarises the request (system size, message counts, tools, max_tokens). " +
			"Use this INSTEAD of reading LLM bodies. include: final,usage,tools,stop,errors (default) + system,messages,thinking,request. Falls back to a generic summary for non-LLM flows.",
		Annotations: readOnly("pano explain"),
	}, s.toolExplain)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_tail",
		Description: "Poll for flows newer than a cursor: returns immediately if any exist, otherwise waits up to wait_ms (max 25000) for the first one. Same filters as pano_flows. Start with since=\"now\" and loop with the returned cursor while the user reproduces something.",
		Annotations: readOnly("pano tail"),
	}, s.toolTail)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_rules_list",
		Description: "List live traffic rules (delays, mocks, blocks, rewrites, throttles, breakpoints) with ids, enabled flag, match, actions, hits, ttl. Also lists available presets when presets=true.",
		Annotations: readOnly("pano rules"),
	}, s.toolRulesList)

	mcp.AddTool(m, &mcp.Tool{
		Name: "pano_rule_add",
		Description: "Add a live traffic rule that affects matching requests immediately. EITHER preset (slow_network{host,ms,jitter_ms} | fail_rate{host,rate,status} | offline_host{host} | timeout{host,after_ms} | rate_limit{host,every_n,status,retry_after} | hold{host,path,on}) with params, " +
			"OR rule: {match:{host glob, path glob or /regex/, method[], header{}, status (response phase), phase}, actions:[{type: delay{ms,jitter_ms} | set_header{name,value,on} | remove_header{name,on} | set_query{name,value} | rewrite_body{json_patch|regex+replace|template,on} | mock{status,headers,body} | block{mode: reset|timeout|status} | redirect{upstream} | throttle{kbps} | breakpoint{on} | tag{tags}}], probability, max_hits}. " +
			"Set ttl_s (e.g. 600) so the rule expires if you forget to remove it. Returns the rule id.",
		Annotations: mutating("pano add rule", false),
	}, s.toolRuleAdd)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_rule_update",
		Description: "Enable/disable (enabled=true|false), rename, or replace (rule) a rule by id.",
		Annotations: mutating("pano update rule", true),
	}, s.toolRuleUpdate)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_rule_remove",
		Description: "Remove a rule by id, or all=true to clear every rule (restores untouched traffic).",
		Annotations: mutating("pano remove rule", true),
	}, s.toolRuleRemove)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_breakpoint_resume",
		Description: "Act on a request or response currently HELD by a breakpoint rule (list them with pano_flows state=held): action=resume (optionally with edits: url, method, set_headers, remove_headers, body, body_patch, status) or action=drop. Held items auto-continue after the hold timeout.",
		Annotations: mutating("pano resume", false),
	}, s.toolResume)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "pano_har",
		Description: "action=export writes matching flows to a HAR 1.2 file at path (secrets redacted unless reveal_secrets); action=import loads a HAR file into the current session. Never returns file contents — use pano_flows afterwards.",
		Annotations: mutating("pano HAR", false),
		Meta:        meta("anthropic/maxResultSizeChars", 200000),
	}, s.toolHAR)

	mcp.AddTool(m, &mcp.Tool{
		Name: "pano_decrypt",
		Description: "Which HTTPS hosts pano decrypts. Modes: all (decrypt everything except the never list, default), only (decrypt just the only list), off (tunnel everything: hosts, bytes, timing only). The never list wins in every mode; use it for apps that pin certificates. " +
			"action=status shows mode, both lists in full and hosts that refused pano's certificate in the last hour (pinning suspects). action=mode sets mode. action=add / action=remove edit list=only|never with hosts (a bare host covers its subdomains; globs like *.example.com work); hosts=[\"@rejected\"] with list=never adds every rejected host. " +
			"Changes apply immediately, persist to config.toml and are audited. Nothing is ever added automatically.",
		Annotations: mutating("pano decrypt", true),
	}, s.toolDecrypt)

	mcp.AddTool(m, &mcp.Tool{
		Name: "pano_system_proxy",
		Description: "CHANGES macOS SYSTEM SETTINGS. enabled=true routes ALL of this Mac's HTTP/HTTPS traffic through pano (browsers and most apps); enabled=false restores the previous settings. Only call when the user explicitly asks. confirm must be \"yes\". " +
			"For a single command prefer telling the user to run `pano run -- <cmd>` instead. CA install is not available over MCP (terminal: pano ca install).",
		Annotations: &mcp.ToolAnnotations{Title: "pano system proxy", DestructiveHint: boolp(true), OpenWorldHint: boolp(true)},
	}, s.toolSysProxy)
}

// ---- inputs ----

type emptyIn struct{}

type captureIn struct {
	Action string `json:"action" jsonschema:"start|stop|clear|session"`
	Name   string `json:"name,omitempty" jsonschema:"session name (for action=session)"`
}

type flowsIn struct {
	Q           string   `json:"q,omitempty" jsonschema:"full-text search over url, headers and text bodies"`
	Host        string   `json:"host,omitempty" jsonschema:"host glob, e.g. api.openai.com or *.googleapis.com"`
	Path        string   `json:"path,omitempty" jsonschema:"path prefix or glob, e.g. /v1/* "`
	Method      []string `json:"method,omitempty" jsonschema:"e.g. [\"POST\"]"`
	Status      string   `json:"status,omitempty" jsonschema:"status or range: 500, 4xx, 400-499, !2xx"`
	Since       string   `json:"since,omitempty" jsonschema:"RFC3339, relative (15m, 2h, 1d), or a flow id/cursor"`
	Until       string   `json:"until,omitempty"`
	ContentType string   `json:"content_type,omitempty" jsonschema:"json|sse|html|js|css|img|bin|text or a MIME prefix"`
	MinBytes    int64    `json:"min_bytes,omitempty"`
	HasError    bool     `json:"has_error,omitempty" jsonschema:"status 400 or higher, or a transport/TLS error"`
	Tag         string   `json:"tag,omitempty"`
	Rule        string   `json:"rule,omitempty" jsonschema:"rule id that matched"`
	State       string   `json:"state,omitempty" jsonschema:"all|held|active|done|failed|replayed|mocked|blocked"`
	Kind        string   `json:"kind,omitempty" jsonschema:"http|websocket|tunnel"`
	Client      string   `json:"client,omitempty" jsonschema:"client IP, e.g. a phone from pano_status devices; 'remote' for every non-loopback client"`
	Limit       int      `json:"limit,omitempty" jsonschema:"default 50, max 200"`
	Cursor      string   `json:"cursor,omitempty" jsonschema:"opaque, from a previous call"`
}

func (in flowsIn) filter() api.FlowFilter {
	return api.FlowFilter{
		Q: in.Q, Host: in.Host, Path: in.Path, Method: in.Method, Status: in.Status, Since: in.Since, Until: in.Until,
		ContentType: in.ContentType, MinBytes: in.MinBytes, HasError: in.HasError, Tag: in.Tag, Rule: in.Rule,
		State: in.State, Kind: in.Kind, Client: in.Client, Limit: in.Limit, Cursor: in.Cursor,
	}
}

type flowIn struct {
	ID            string `json:"id" jsonschema:"flow id from pano_flows"`
	Part          string `json:"part,omitempty" jsonschema:"request|response|both (default both)"`
	View          string `json:"view,omitempty" jsonschema:"summary|schema|truncated|pretty|raw (default summary)"`
	Path          string `json:"path,omitempty" jsonschema:"gjson path into the JSON body, e.g. choices.0.message.content"`
	MaxBytes      int    `json:"max_bytes,omitempty" jsonschema:"body budget in bytes (default 4096, hard cap 1048576)"`
	Headers       *bool  `json:"headers,omitempty" jsonschema:"include headers (default true)"`
	RevealSecrets bool   `json:"reveal_secrets,omitempty" jsonschema:"show secrets unredacted (audited)"`
}

type diffIn struct {
	A             string   `json:"a" jsonschema:"first flow id"`
	B             string   `json:"b" jsonschema:"second flow id"`
	Part          string   `json:"part,omitempty" jsonschema:"request|response|both (default response)"`
	Path          string   `json:"path,omitempty" jsonschema:"restrict body diff to a JSON path"`
	IgnoreHeaders []string `json:"ignore_headers,omitempty"`
	MaxChanges    int      `json:"max_changes,omitempty" jsonschema:"default 50"`
}

type replayIn struct {
	ID            string            `json:"id"`
	URL           string            `json:"url,omitempty"`
	Method        string            `json:"method,omitempty"`
	SetHeaders    map[string]string `json:"set_headers,omitempty"`
	RemoveHeaders []string          `json:"remove_headers,omitempty"`
	Body          *string           `json:"body,omitempty" jsonschema:"replacement body (string)"`
	BodyPatch     map[string]any    `json:"body_patch,omitempty" jsonschema:"json path → new value"`
	FollowRules   *bool             `json:"follow_rules,omitempty" jsonschema:"apply live rules (default true)"`
	TimeoutMS     int               `json:"timeout_ms,omitempty" jsonschema:"default 30000"`
}

type explainIn struct {
	ID       string   `json:"id"`
	Include  []string `json:"include,omitempty" jsonschema:"final,usage,tools,stop,errors,system,messages,thinking,request"`
	MaxChars int      `json:"max_chars,omitempty" jsonschema:"default 4000"`
	Provider string   `json:"provider,omitempty" jsonschema:"force: anthropic|openai-chat|openai-responses|gemini"`
}

type tailIn struct {
	Since  string `json:"since,omitempty" jsonschema:"cursor from a previous call, or \"now\" (default)"`
	WaitMS int    `json:"wait_ms,omitempty" jsonschema:"long-poll wait, default 10000, max 25000"`
	flowsIn
}

type rulesListIn struct {
	Presets bool `json:"presets,omitempty" jsonschema:"also list presets and their params"`
}

type ruleAddIn struct {
	Rule   *api.Rule      `json:"rule,omitempty"`
	Preset string         `json:"preset,omitempty" jsonschema:"slow_network|fail_rate|offline_host|timeout|rate_limit|hold"`
	Params map[string]any `json:"params,omitempty" jsonschema:"preset parameters, e.g. {\"host\":\"api.openai.com\",\"ms\":3000}"`
	Name   string         `json:"name,omitempty"`
	TTLS   int            `json:"ttl_s,omitempty" jsonschema:"auto-remove after N seconds (recommended: 600)"`
}

type ruleUpdateIn struct {
	ID      string    `json:"id"`
	Enabled *bool     `json:"enabled,omitempty"`
	Name    string    `json:"name,omitempty"`
	Rule    *api.Rule `json:"rule,omitempty" jsonschema:"full replacement"`
}

type ruleRemoveIn struct {
	ID  string `json:"id,omitempty"`
	All bool   `json:"all,omitempty"`
}

type resumeIn struct {
	ID            string            `json:"id" jsonschema:"held flow id"`
	Action        string            `json:"action,omitempty" jsonschema:"resume|drop (default resume)"`
	URL           string            `json:"url,omitempty"`
	Method        string            `json:"method,omitempty"`
	SetHeaders    map[string]string `json:"set_headers,omitempty"`
	RemoveHeaders []string          `json:"remove_headers,omitempty"`
	Body          *string           `json:"body,omitempty"`
	BodyPatch     map[string]any    `json:"body_patch,omitempty"`
	Status        int               `json:"status,omitempty" jsonschema:"response phase only"`
}

type harIn struct {
	Action        string `json:"action" jsonschema:"export|import"`
	Path          string `json:"path" jsonschema:"absolute file path"`
	RevealSecrets bool   `json:"reveal_secrets,omitempty"`
	flowsIn
}

type decryptIn struct {
	Action string   `json:"action" jsonschema:"status|mode|add|remove"`
	Mode   string   `json:"mode,omitempty" jsonschema:"all|only|off (for action=mode)"`
	List   string   `json:"list,omitempty" jsonschema:"only|never (for action=add|remove)"`
	Hosts  []string `json:"hosts,omitempty" jsonschema:"hosts or globs (for action=add|remove); \"@rejected\" expands to every host that recently refused the certificate (list=never only)"`
}

type sysProxyIn struct {
	Enabled bool   `json:"enabled"`
	Confirm string `json:"confirm" jsonschema:"must be \"yes\""`
}

// ---- handlers ----

func parseID(s string) (flow.ID, error) {
	id, ok := flow.ParseShort(s)
	if !ok || id == 0 {
		return 0, fmt.Errorf("invalid flow id %q", s)
	}
	return id, nil
}

func (s *Server) toolStatus(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
	st, err := s.c.Status(ctx)
	if err != nil {
		return errResult(err, "")
	}
	return ok(withNext(FormatStatus(st), "pano_flows limit=20"))
}

// FormatStatus renders status compactly.
func FormatStatus(st api.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pano %s pid %d up %s\nproxy: %s", st.Version, st.PID, st.Uptime, st.ProxyAddr)
	if st.MCPAddr != "" {
		fmt.Fprintf(&b, "  mcp-http: http://%s/mcp", st.MCPAddr)
	}
	fmt.Fprintf(&b, "\nsystem proxy: %v", st.SystemProxy.Enabled)
	if st.SystemProxy.Detail != "" {
		fmt.Fprintf(&b, " (%s)", st.SystemProxy.Detail)
	}
	if st.Lifecycle.Mode == "app" {
		fmt.Fprintf(&b, "\nlifecycle: app — a `pano on` window owns the daemon; closing it turns pano off")
	} else {
		fmt.Fprintf(&b, "\nlifecycle: background — runs until the user runs `pano off`")
	}
	if st.Lifecycle.UIs > 0 {
		fmt.Fprintf(&b, " (%d ui attached)", st.Lifecycle.UIs)
	}
	fmt.Fprintf(&b, "\nca trusted: %v", st.CA.Trusted)
	if !st.CA.Trusted {
		fmt.Fprintf(&b, " — user must run `pano ca install` in a terminal")
	}
	if st.CA.Warning != "" {
		fmt.Fprintf(&b, "\nca warning: %s", st.CA.Warning)
	}
	fmt.Fprintf(&b, "\ncapturing: %v  session: %s  flows: %d in memory, %d total, last id %s", st.Capturing, st.Session, st.Flows, st.FlowsTotal, st.LastFlowID.Short())
	fmt.Fprintf(&b, "\nrules: %d (%d enabled)  held: %d  active conns: %d  redaction: %v", st.Rules, st.RulesEnabled, st.Held, st.ActiveConns, st.Redaction)
	if st.Dropped > 0 {
		fmt.Fprintf(&b, "\nWARNING: %d events dropped by the store", st.Dropped)
	}
	b.WriteString("\n" + FormatDecrypt(st.Decrypt))
	b.WriteString("\n" + FormatMobile(st.Mobile))
	return b.String()
}

// FormatMobile renders the LAN listener and every device seen, in full.
func FormatMobile(m api.Mobile) string {
	var b strings.Builder
	if m.Enabled {
		fmt.Fprintf(&b, "mobile: on at %s (%s", m.Addr, m.Interface)
		if m.Network != "" {
			fmt.Fprintf(&b, " · %s", m.Network)
		}
		fmt.Fprintf(&b, ") — phones open %s or %s", m.URL, m.MagicURL)
		if m.Warning != "" {
			fmt.Fprintf(&b, "\n  warning: %s", m.Warning)
		}
	} else {
		b.WriteString("mobile: off — the user can run `pano mobile` in a terminal to put a phone behind pano (not available over MCP)")
		if m.LastAddr != "" && len(m.Devices) > 0 {
			fmt.Fprintf(&b, "\n  was at %s — a device may still point there (no internet on it until its Wi-Fi proxy is turned off or `pano mobile` runs again)", m.LastAddr)
		}
	}
	if len(m.Devices) == 0 {
		return b.String()
	}
	if !m.Enabled {
		b.WriteString("\n  devices seen while it was on:")
		for _, d := range m.Devices {
			fmt.Fprintf(&b, "\n    %s %s — %d requests, last %s", d.IP, d.Label(), d.Requests, d.LastSeen.Format("15:04:05"))
		}
		return b.String()
	}
	b.WriteString("\n  devices:")
	for _, d := range m.Devices {
		state := "proxy ✓"
		switch {
		case d.TLS:
			state += ", https ✓"
		case d.Rejected > 0:
			state += fmt.Sprintf(", https ✕ (certificate refused ×%d — not trusted yet?)", d.Rejected)
		case !d.Proxy:
			state = "no proxied request yet"
		default:
			state += ", https not tried"
		}
		fmt.Fprintf(&b, "\n    %s %s — %s, %d requests, last %s", d.IP, d.Label(), state, d.Requests, d.LastSeen.Format("15:04:05"))
	}
	b.WriteString("\n  → pano_flows client=<ip> lists one device's traffic")
	return b.String()
}

// FormatDecrypt renders the decrypt policy with every list in full.
func FormatDecrypt(d api.Decrypt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "decrypt: %s", d.Mode)
	switch d.Mode {
	case "all":
		b.WriteString(" (every host except never)")
	case "only":
		b.WriteString(" (just the only list)")
	case "off":
		b.WriteString(" (nothing is decrypted; tunnels record host, bytes, timing)")
	}
	fmt.Fprintf(&b, "\n  only: %s", orNone(d.Only))
	fmt.Fprintf(&b, "\n  never: %s", orNone(d.Never))
	if len(d.Rejected) > 0 {
		parts := make([]string, len(d.Rejected))
		for i, r := range d.Rejected {
			parts[i] = fmt.Sprintf("%s ×%d", r.Host, r.Count)
		}
		fmt.Fprintf(&b, "\n  rejected recently (client refused pano's certificate — pinning?): %s\n  → pano_decrypt action=add list=never hosts=[\"@rejected\"] if the user wants them left alone", strings.Join(parts, ", "))
	}
	return b.String()
}

func orNone(hosts []string) string {
	if len(hosts) == 0 {
		return "(none)"
	}
	return strings.Join(hosts, " ")
}

func (s *Server) toolDecrypt(ctx context.Context, _ *mcp.CallToolRequest, in decryptIn) (*mcp.CallToolResult, any, error) {
	var (
		d   api.Decrypt
		err error
	)
	switch in.Action {
	case "status", "":
		d, err = s.c.Decrypt(ctx)
	case "mode":
		if in.Mode == "" {
			return errResult(fmt.Errorf("action=mode needs mode=all|only|off"), "")
		}
		d, err = s.c.ChangeDecrypt(ctx, api.DecryptChange{Mode: in.Mode, Source: "mcp"})
	case "add", "remove":
		if len(in.Hosts) == 0 {
			return errResult(fmt.Errorf("action=%s needs hosts", in.Action), "")
		}
		ch := api.DecryptChange{Source: "mcp"}
		switch {
		case in.List == "only" && in.Action == "add":
			ch.AddOnly = in.Hosts
		case in.List == "only":
			ch.RemoveOnly = in.Hosts
		case in.List == "never" && in.Action == "add":
			ch.AddNever = in.Hosts
		case in.List == "never":
			ch.RemoveNever = in.Hosts
		default:
			return errResult(fmt.Errorf("action=%s needs list=only|never", in.Action), "")
		}
		d, err = s.c.ChangeDecrypt(ctx, ch)
	default:
		return errResult(fmt.Errorf("unknown action %q (status|mode|add|remove)", in.Action), "")
	}
	if err != nil {
		return errResult(err, "pano_decrypt action=status")
	}
	next := "pano_flows kind=tunnel has_error=true to see what is not being decrypted"
	if in.Action != "status" && in.Action != "" {
		next = "pano_tail since=now to watch the effect (open tunnels keep their old policy)"
	}
	return ok(withNext(FormatDecrypt(d), next))
}

func (s *Server) toolCapture(ctx context.Context, _ *mcp.CallToolRequest, in captureIn) (*mcp.CallToolResult, any, error) {
	st, err := s.c.Capture(ctx, api.CaptureRequest{Action: in.Action, Name: in.Name})
	if err != nil {
		return errResult(err, "")
	}
	return ok(text(fmt.Sprintf("capture %s → capturing=%v session=%s flows=%d", in.Action, st.Capturing, st.Session, st.Flows)))
}

func (s *Server) toolFlows(ctx context.Context, _ *mcp.CallToolRequest, in flowsIn) (*mcp.CallToolResult, any, error) {
	list, err := s.c.Flows(ctx, in.filter())
	if err != nil {
		return errResult(err, "pano_status")
	}
	if len(list.Flows) == 0 {
		return ok(withNext("no flows match", "pano_flows with fewer filters, or pano_tail since=now to wait for traffic"))
	}
	body := FormatRows(list.Flows)
	footer := fmt.Sprintf("%d of %d flows", len(list.Flows), list.Total)
	if list.Cursor != "" {
		footer += " · cursor=" + list.Cursor
	}
	next := fmt.Sprintf("pano_flow id=%s view=summary", list.Flows[0].Short)
	if store.IsLLMHost(list.Flows[0].Host) {
		next = fmt.Sprintf("pano_flow_explain id=%s", list.Flows[0].Short)
	}
	return ok(withNext(body+"\n"+footer, next))
}

// FormatRows renders flows as a fixed-column text table.
func FormatRows(rows []api.FlowRow) string {
	var b strings.Builder
	b.WriteString("id     time     meth host                       path                                  st  dur     up     down   type flags\n")
	for _, r := range rows {
		meth := r.Method
		switch r.Kind {
		case flow.KindTunnel:
			meth = "TUN"
		case flow.KindWebSocket:
			meth = "WS"
		case flow.KindHTTP:
		}
		st := "-"
		if r.Status > 0 {
			st = fmt.Sprint(r.Status)
		}
		if r.State == flow.StateActive {
			st = "…"
		}
		fmt.Fprintf(&b, "%-6s %-8s %-4s %-26s %-37s %-3s %-7s %-6s %-6s %-4s %s\n",
			r.Short, r.Time.Local().Format("15:04:05"), trunc(meth, 4), trunc(r.Host, 26), trunc(r.Path, 37), st,
			r.Duration, store.FormatBytes(r.Up), store.FormatBytes(r.Down), typeShort(r.Type), strings.Join(r.Flags, ","))
		if r.Error != "" {
			fmt.Fprintf(&b, "       ↳ %s\n", trunc(r.Error, 110))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func typeShort(t string) string {
	if t == "tunnel" {
		return "tun"
	}
	return trunc(t, 4)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (s *Server) toolFlow(ctx context.Context, _ *mcp.CallToolRequest, in flowIn) (*mcp.CallToolResult, any, error) {
	id, err := parseID(in.ID)
	if err != nil {
		return errResult(err, "pano_flows")
	}
	q := api.FlowQuery{Part: in.Part, View: in.View, Path: in.Path, MaxBytes: in.MaxBytes, Headers: in.Headers, RevealSecrets: in.RevealSecrets}
	d, err := s.c.Flow(ctx, id, q)
	if err != nil {
		return errResult(err, "pano_flows limit=20")
	}
	return ok(withNext(d.Text, d.Next))
}

func (s *Server) toolDiff(ctx context.Context, _ *mcp.CallToolRequest, in diffIn) (*mcp.CallToolResult, any, error) {
	a, err := parseID(in.A)
	if err != nil {
		return errResult(err, "")
	}
	b, err := parseID(in.B)
	if err != nil {
		return errResult(err, "")
	}
	r, err := s.c.Diff(ctx, api.DiffRequest{A: a, B: b, Part: in.Part, Path: in.Path, IgnoreHeaders: in.IgnoreHeaders, MaxChanges: in.MaxChanges})
	if err != nil {
		return errResult(err, "")
	}
	return ok(withNext(r.Text, fmt.Sprintf("pano_flow id=%s view=summary", in.B)))
}

func (s *Server) toolReplay(ctx context.Context, _ *mcp.CallToolRequest, in replayIn) (*mcp.CallToolResult, any, error) {
	id, err := parseID(in.ID)
	if err != nil {
		return errResult(err, "")
	}
	r, err := s.c.Replay(ctx, id, api.ReplayRequest{
		URL: in.URL, Method: in.Method, SetHeaders: in.SetHeaders, RemoveHeaders: in.RemoveHeaders,
		Body: in.Body, BodyPatch: in.BodyPatch, FollowRules: in.FollowRules, TimeoutMS: in.TimeoutMS,
	})
	if err != nil {
		return errResult(err, "")
	}
	head := fmt.Sprintf("replayed %s → new flow %s: status %d, %s, %s", in.ID, r.Short, r.Status, store.FormatBytes(r.Size), r.Duration)
	if r.Error != "" {
		head += "\nerror: " + r.Error
	}
	return ok(withNext(head+"\n"+r.Summary, fmt.Sprintf("pano_flow_diff a=%s b=%s", in.ID, r.Short)))
}

func (s *Server) toolExplain(ctx context.Context, _ *mcp.CallToolRequest, in explainIn) (*mcp.CallToolResult, any, error) {
	id, err := parseID(in.ID)
	if err != nil {
		return errResult(err, "")
	}
	r, err := s.c.Explain(ctx, id, api.ExplainRequest{Include: in.Include, MaxChars: in.MaxChars, Provider: in.Provider})
	if err != nil {
		return errResult(err, fmt.Sprintf("pano_flow id=%s view=summary", in.ID))
	}
	return ok(withNext(r.Text, fmt.Sprintf("pano_flow id=%s part=request view=summary  (or include=messages)", in.ID)))
}

func (s *Server) toolTail(ctx context.Context, _ *mcp.CallToolRequest, in tailIn) (*mcp.CallToolResult, any, error) {
	wait := in.WaitMS
	if wait <= 0 {
		wait = 10000
	}
	if wait > 25000 {
		wait = 25000
	}
	since := in.Since
	if since == "" {
		since = "now"
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(wait)*time.Millisecond+5*time.Second)
	defer cancel()
	r, err := s.c.Tail(cctx, api.TailRequest{Since: since, WaitMS: wait, Filter: in.filter()})
	if err != nil {
		return errResult(err, "")
	}
	var body string
	if len(r.Flows) == 0 {
		body = "no new flows"
	} else {
		body = FormatRows(r.Flows)
	}
	if len(r.Held) > 0 {
		body += fmt.Sprintf("\nHELD: %d request(s) waiting on a breakpoint — pano_breakpoint_resume id=%s", len(r.Held), r.Held[0].Short)
	}
	return ok(withNext(body+"\ncursor="+r.Cursor, fmt.Sprintf("pano_tail since=%s", r.Cursor)))
}

func (s *Server) toolRulesList(ctx context.Context, _ *mcp.CallToolRequest, in rulesListIn) (*mcp.CallToolResult, any, error) {
	rules, err := s.c.Rules(ctx)
	if err != nil {
		return errResult(err, "")
	}
	var b strings.Builder
	if len(rules) == 0 {
		b.WriteString("no rules")
	}
	for _, r := range rules {
		fmt.Fprintf(&b, "%s\n", FormatRule(r))
	}
	if in.Presets {
		ps, err := s.c.Presets(ctx)
		if err == nil {
			b.WriteString("\npresets:\n")
			for _, p := range ps {
				pj, _ := json.Marshal(p.Params)
				fmt.Fprintf(&b, "  %s — %s  params=%s\n", p.Name, p.Description, pj)
			}
		}
	}
	return ok(withNext(strings.TrimRight(b.String(), "\n"), "pano_rule_add preset=slow_network params={host:…} ttl_s=600"))
}

// FormatRule renders one rule line.
func FormatRule(r api.Rule) string {
	en := "on "
	if r.Enabled != nil && !*r.Enabled {
		en = "off"
	}
	mj, _ := json.Marshal(r.Match)
	var acts []string
	for _, a := range r.Actions {
		acts = append(acts, a.Type)
	}
	extra := ""
	if r.Probability > 0 && r.Probability < 1 {
		extra += fmt.Sprintf(" p=%.2f", r.Probability)
	}
	if r.TTLSeconds > 0 {
		extra += fmt.Sprintf(" ttl=%ds", r.TTLSeconds)
	}
	return fmt.Sprintf("%s %s %q match=%s actions=%s hits=%d%s", en, r.ID, r.Name, mj, strings.Join(acts, ","), r.Hits, extra)
}

func (s *Server) toolRuleAdd(ctx context.Context, _ *mcp.CallToolRequest, in ruleAddIn) (*mcp.CallToolResult, any, error) {
	r, err := s.c.AddRule(ctx, api.RuleAddRequest{Rule: in.Rule, Preset: in.Preset, Params: in.Params, Name: in.Name, TTLS: in.TTLS})
	if err != nil {
		return errResult(err, "pano_rules_list presets=true")
	}
	rj, _ := json.Marshal(r)
	return ok(withNext("added "+FormatRule(r)+"\n"+string(rj), fmt.Sprintf("pano_rule_remove id=%s when done", r.ID)))
}

func (s *Server) toolRuleUpdate(ctx context.Context, _ *mcp.CallToolRequest, in ruleUpdateIn) (*mcp.CallToolResult, any, error) {
	r, err := s.c.UpdateRule(ctx, in.ID, api.RulePatch{Enabled: in.Enabled, Name: in.Name, Rule: in.Rule})
	if err != nil {
		return errResult(err, "pano_rules_list")
	}
	return ok(text("updated " + FormatRule(r)))
}

func (s *Server) toolRuleRemove(ctx context.Context, _ *mcp.CallToolRequest, in ruleRemoveIn) (*mcp.CallToolResult, any, error) {
	if in.All {
		n, err := s.c.RemoveAllRules(ctx)
		if err != nil {
			return errResult(err, "")
		}
		return ok(text(fmt.Sprintf("removed %d rules", n)))
	}
	if in.ID == "" {
		return errResult(fmt.Errorf("id required (or all=true)"), "pano_rules_list")
	}
	if err := s.c.RemoveRule(ctx, in.ID); err != nil {
		return errResult(err, "pano_rules_list")
	}
	return ok(text("removed " + in.ID))
}

func (s *Server) toolResume(ctx context.Context, _ *mcp.CallToolRequest, in resumeIn) (*mcp.CallToolResult, any, error) {
	id, err := parseID(in.ID)
	if err != nil {
		return errResult(err, "pano_flows state=held")
	}
	action := in.Action
	if action == "" {
		action = "resume"
	}
	err = s.c.Resume(ctx, id, api.ResumeRequest{
		Action: action, URL: in.URL, Method: in.Method, SetHeaders: in.SetHeaders, RemoveHeaders: in.RemoveHeaders,
		Body: in.Body, BodyPatch: in.BodyPatch, Status: in.Status,
	})
	if err != nil {
		return errResult(err, "pano_flows state=held")
	}
	return ok(withNext(fmt.Sprintf("%s %s", action, in.ID), fmt.Sprintf("pano_flow id=%s view=summary", in.ID)))
}

func (s *Server) toolHAR(ctx context.Context, _ *mcp.CallToolRequest, in harIn) (*mcp.CallToolResult, any, error) {
	r, err := s.c.HAR(ctx, api.HARRequest{Action: in.Action, Path: in.Path, Filter: in.filter(), RevealSecrets: in.RevealSecrets})
	if err != nil {
		return errResult(err, "")
	}
	return ok(text(fmt.Sprintf("%s %d flows (%d bytes) %s", in.Action, r.Count, r.Bytes, r.Path)))
}

func (s *Server) toolSysProxy(ctx context.Context, _ *mcp.CallToolRequest, in sysProxyIn) (*mcp.CallToolResult, any, error) {
	if in.Confirm != "yes" {
		return errResult(fmt.Errorf("refusing to change macOS system proxy without confirm=\"yes\" — ask the user first"), "")
	}
	sp, err := s.c.SetSysProxy(ctx, api.SysProxyRequest{Enabled: in.Enabled, Confirm: in.Confirm})
	if err != nil {
		return errResult(err, "tell the user to run `pano on` / `pano off` in a terminal")
	}
	return ok(text(fmt.Sprintf("system proxy enabled=%v set_by_pano=%v %s", sp.Enabled, sp.SetByPano, sp.Detail)))
}
