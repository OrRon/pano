package api

import (
	"net/http"
	"time"

	"github.com/orron/pano/internal/flow"
)

// Version of the control API.
const Version = 1

// Envelope wraps every response.
type Envelope[T any] struct {
	OK    bool   `json:"ok"`
	Data  T      `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// Error is a machine-readable failure.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Error codes.
const (
	CodeNotFound    = "not_found"
	CodeBadRequest  = "bad_request"
	CodeConflict    = "conflict"
	CodeUnsupported = "unsupported"
	CodeInternal    = "internal"
	CodeUpstream    = "upstream"
)

// Status is the daemon's state.
type Status struct {
	Version      string    `json:"version"`
	PID          int       `json:"pid"`
	Uptime       string    `json:"uptime"`
	ProxyAddr    string    `json:"proxy_addr"`
	MCPAddr      string    `json:"mcp_addr,omitempty"`
	Capturing    bool      `json:"capturing"`
	Session      string    `json:"session"`
	Flows        int       `json:"flows"`
	FlowsTotal   int64     `json:"flows_total"`
	LastFlowID   flow.ID   `json:"last_flow_id"`
	ActiveConns  int       `json:"active_conns"`
	Dropped      int64     `json:"dropped"`
	CA           CAStatus  `json:"ca"`
	SystemProxy  SysProxy  `json:"system_proxy"`
	Rules        int       `json:"rules"`
	RulesEnabled int       `json:"rules_enabled"`
	Held         int       `json:"held"`
	Persist      bool      `json:"persist"`
	Decrypt      Decrypt   `json:"decrypt"`
	Mobile       Mobile    `json:"mobile"`
	Redaction    bool      `json:"redaction"`
	BusSeq       uint64    `json:"bus_seq"`
	StartedAt    time.Time `json:"started_at"`
}

// CAStatus describes the root certificate.
type CAStatus struct {
	Path      string    `json:"path"`
	Subject   string    `json:"subject"`
	NotAfter  time.Time `json:"not_after"`
	Trusted   bool      `json:"trusted"`
	Supported bool      `json:"supported"`
	Detail    string    `json:"detail,omitempty"`
	// Warning is set when the root is about to expire or was just rotated;
	// front ends print it verbatim.
	Warning string `json:"warning,omitempty"`
}

// SysProxy describes macOS system proxy state.
type SysProxy struct {
	Supported bool     `json:"supported"`
	Enabled   bool     `json:"enabled"` // pano has enabled it
	SetByPano bool     `json:"set_by_pano"`
	Services  []string `json:"services,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

// FlowFilter selects flows. Zero fields match everything.
type FlowFilter struct {
	Q           string   `json:"q,omitempty"`
	Host        string   `json:"host,omitempty"`
	Path        string   `json:"path,omitempty"`
	Method      []string `json:"method,omitempty"`
	Status      string   `json:"status,omitempty"` // "500", "4xx", "400-499", "!2xx"
	Since       string   `json:"since,omitempty"`  // RFC3339, relative "15m", or a flow id
	Until       string   `json:"until,omitempty"`
	ContentType string   `json:"content_type,omitempty"` // json|html|sse|text|image|binary|js|css or MIME prefix
	MinBytes    int64    `json:"min_bytes,omitempty"`
	HasError    bool     `json:"has_error,omitempty"`
	Tag         string   `json:"tag,omitempty"`
	Rule        string   `json:"rule,omitempty"`
	State       string   `json:"state,omitempty"` // all|held|replayed|blocked|mocked|active
	Kind        string   `json:"kind,omitempty"`
	Session     string   `json:"session,omitempty"`
	Client      string   `json:"client,omitempty"` // client IP, or "remote" for every non-loopback client
	Limit       int      `json:"limit,omitempty"`
	Cursor      string   `json:"cursor,omitempty"`
	Fields      []string `json:"fields,omitempty"`
}

// FlowRow is the one-line list representation.
type FlowRow struct {
	ID       flow.ID    `json:"id"`
	Short    string     `json:"short"`
	Time     time.Time  `json:"time"`
	Kind     flow.Kind  `json:"kind"`
	Method   string     `json:"method"`
	Host     string     `json:"host"`
	Path     string     `json:"path"`
	Status   int        `json:"status"`
	Duration string     `json:"dur"`
	DurMS    float64    `json:"dur_ms"`
	Up       int64      `json:"up"`
	Down     int64      `json:"down"`
	Type     string     `json:"type"`
	Flags    []string   `json:"flags,omitempty"`
	Error    string     `json:"error,omitempty"`
	Proto    string     `json:"proto,omitempty"`
	Client   string     `json:"client,omitempty"`
	State    flow.State `json:"state"`
}

// FlowList is a page of rows.
type FlowList struct {
	Flows  []FlowRow `json:"flows"`
	Cursor string    `json:"cursor,omitempty"`
	Total  int       `json:"total"`
	LastID flow.ID   `json:"last_id"`
}

// View modes for bodies.
const (
	ViewSummary   = "summary"
	ViewSchema    = "schema"
	ViewTruncated = "truncated"
	ViewPretty    = "pretty"
	ViewRaw       = "raw"
)

// FlowQuery selects how a flow is rendered.
type FlowQuery struct {
	Part          string `json:"part,omitempty"` // request|response|both
	View          string `json:"view,omitempty"`
	Path          string `json:"path,omitempty"`
	MaxBytes      int    `json:"max_bytes,omitempty"`
	Headers       *bool  `json:"headers,omitempty"`
	RevealSecrets bool   `json:"reveal_secrets,omitempty"`
}

// RenderedPart is one rendered side of an exchange.
type RenderedPart struct {
	Headers  http.Header  `json:"headers,omitempty"`
	Body     flow.BodyRef `json:"body"`
	Rendered string       `json:"rendered"` // text for the agent/human
	Binary   bool         `json:"binary,omitempty"`
	Redacted int          `json:"redacted,omitempty"`
}

// FlowDetail is a flow plus rendered parts.
type FlowDetail struct {
	Flow     *flow.Flow    `json:"flow"`
	Request  *RenderedPart `json:"request,omitempty"`
	Response *RenderedPart `json:"response,omitempty"`
	Text     string        `json:"text"` // full pre-formatted text block
	Next     string        `json:"next,omitempty"`
}

// ReplayRequest re-sends a flow with overrides.
type ReplayRequest struct {
	URL           string            `json:"url,omitempty"`
	Method        string            `json:"method,omitempty"`
	SetHeaders    map[string]string `json:"set_headers,omitempty"`
	RemoveHeaders []string          `json:"remove_headers,omitempty"`
	Body          *string           `json:"body,omitempty"`
	BodyPatch     map[string]any    `json:"body_patch,omitempty"`
	FollowRules   *bool             `json:"follow_rules,omitempty"`
	TimeoutMS     int               `json:"timeout_ms,omitempty"`
}

// ReplayResult is the outcome.
type ReplayResult struct {
	NewID    flow.ID `json:"new_id"`
	Short    string  `json:"short"`
	Status   int     `json:"status"`
	Size     int64   `json:"size"`
	Duration string  `json:"dur"`
	Error    string  `json:"error,omitempty"`
	Summary  string  `json:"summary"`
}

// DiffRequest compares two flows.
type DiffRequest struct {
	A             flow.ID  `json:"a"`
	B             flow.ID  `json:"b"`
	Part          string   `json:"part,omitempty"`
	Path          string   `json:"path,omitempty"`
	IgnoreHeaders []string `json:"ignore_headers,omitempty"`
	MaxChanges    int      `json:"max_changes,omitempty"`
}

// DiffResult is a rendered diff.
type DiffResult struct {
	Text    string `json:"text"`
	Changes int    `json:"changes"`
}

// ExplainRequest digests LLM traffic.
type ExplainRequest struct {
	Include  []string `json:"include,omitempty"`
	MaxChars int      `json:"max_chars,omitempty"`
	Provider string   `json:"provider,omitempty"`
}

// ExplainResult is the digest.
type ExplainResult struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model,omitempty"`
	Stream   bool           `json:"stream"`
	Text     string         `json:"text"`
	Usage    map[string]any `json:"usage,omitempty"`
	Partial  bool           `json:"partial,omitempty"`
}

// CaptureRequest controls recording.
type CaptureRequest struct {
	Action string `json:"action"` // start|stop|clear|session
	Name   string `json:"name,omitempty"`
}

// Session groups flows.
type Session struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Flows     int       `json:"flows"`
	Current   bool      `json:"current"`
}

// Rule is a live traffic rule.
type Rule struct {
	ID          string    `json:"id,omitempty"`
	Name        string    `json:"name,omitempty"`
	Enabled     *bool     `json:"enabled,omitempty"`
	Priority    int       `json:"priority,omitempty"`
	Match       Match     `json:"match"`
	Actions     []Action  `json:"actions"`
	Probability float64   `json:"probability,omitempty"`
	MaxHits     int       `json:"max_hits,omitempty"`
	TTLSeconds  int       `json:"ttl_s,omitempty"`
	Expires     time.Time `json:"expires,omitempty"`
	Hits        int64     `json:"hits,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

// Match selects flows for a rule (all set fields must match).
type Match struct {
	Host   string            `json:"host,omitempty"` // glob
	Path   string            `json:"path,omitempty"` // glob or /regex/
	Method []string          `json:"method,omitempty"`
	Scheme string            `json:"scheme,omitempty"`
	Header map[string]string `json:"header,omitempty"` // name -> glob
	Status string            `json:"status,omitempty"` // response phase
	Phase  string            `json:"phase,omitempty"`  // request|response|both
}

// Action is one rule effect.
type Action struct {
	Type string `json:"type"`         // delay|set_header|remove_header|set_query|rewrite_body|mock|block|redirect|throttle|breakpoint|tag
	On   string `json:"on,omitempty"` // request|response

	// delay
	MS       int `json:"ms,omitempty"`
	JitterMS int `json:"jitter_ms,omitempty"`
	// set_header / remove_header / set_query
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
	// rewrite_body
	JSONPatch map[string]any `json:"json_patch,omitempty"`
	Regex     string         `json:"regex,omitempty"`
	Replace   string         `json:"replace,omitempty"`
	Template  string         `json:"template,omitempty"`
	// mock / block(status)
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	// block
	Mode string `json:"mode,omitempty"` // reset|timeout|status
	// redirect
	Upstream     string `json:"upstream,omitempty"`
	PreserveHost bool   `json:"preserve_host,omitempty"`
	// throttle
	KBps int `json:"kbps,omitempty"`
	// tag
	Tags []string `json:"tags,omitempty"`
}

// RuleAddRequest creates a rule from a spec or a preset.
type RuleAddRequest struct {
	Rule   *Rule          `json:"rule,omitempty"`
	Preset string         `json:"preset,omitempty"`
	Params map[string]any `json:"params,omitempty"`
	Name   string         `json:"name,omitempty"`
	TTLS   int            `json:"ttl_s,omitempty"`
}

// RulePatch updates a rule.
type RulePatch struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Name    string `json:"name,omitempty"`
	Rule    *Rule  `json:"rule,omitempty"`
}

// Held is a request/response parked on a breakpoint.
type Held struct {
	ID     flow.ID   `json:"id"`
	Short  string    `json:"short"`
	Phase  string    `json:"phase"`
	Method string    `json:"method"`
	URL    string    `json:"url"`
	Status int       `json:"status,omitempty"`
	Since  time.Time `json:"since"`
	Age    string    `json:"age"`
	RuleID string    `json:"rule_id"`
}

// ResumeRequest releases a held item with optional edits.
type ResumeRequest struct {
	Action        string            `json:"action"` // resume|drop
	URL           string            `json:"url,omitempty"`
	Method        string            `json:"method,omitempty"`
	SetHeaders    map[string]string `json:"set_headers,omitempty"`
	RemoveHeaders []string          `json:"remove_headers,omitempty"`
	Body          *string           `json:"body,omitempty"`
	BodyPatch     map[string]any    `json:"body_patch,omitempty"`
	Status        int               `json:"status,omitempty"`
}

// HARRequest exports/imports.
type HARRequest struct {
	Action        string     `json:"action"` // export|import
	Path          string     `json:"path"`
	Filter        FlowFilter `json:"filter,omitempty"`
	RevealSecrets bool       `json:"reveal_secrets,omitempty"`
}

// HARResult reports the file written/read.
type HARResult struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

// Decrypt is the HTTPS decryption policy plus the hosts that recently
// refused pano's certificate. Every list is always complete: front ends print
// entries, never counts.
type Decrypt struct {
	// Mode is all (decrypt everything except Never), only (decrypt just Only)
	// or off (tunnel everything, record hosts and bytes only).
	Mode string `json:"mode"`
	// Only is decrypted when Mode is "only". Hosts cover their subdomains;
	// globs (*, ?) are accepted.
	Only []string `json:"only"`
	// Never is never decrypted, in every mode (pinned apps).
	Never []string `json:"never"`
	// Rejected lists hosts whose client refused pano's certificate in the last
	// hour — the usual sign of pinning. Suggestions only; never auto-applied.
	Rejected []RejectedHost `json:"rejected,omitempty"`
}

// RejectedHost is one entry of Decrypt.Rejected.
type RejectedHost struct {
	Host  string    `json:"host"`
	Count int       `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	Error string    `json:"error"`
}

// DecryptChange is a partial update to the decrypt policy (PATCH
// /v1/decrypt). Empty fields are left alone. Hosts are normalised (lowercase,
// trailing dot and :port stripped) and deduplicated. The literal host
// "@rejected" in AddNever expands to every currently rejected host.
type DecryptChange struct {
	Mode        string   `json:"mode,omitempty"`
	AddOnly     []string `json:"add_only,omitempty"`
	RemoveOnly  []string `json:"remove_only,omitempty"`
	AddNever    []string `json:"add_never,omitempty"`
	RemoveNever []string `json:"remove_never,omitempty"`
	// Source names the front end making the change (cli, mcp, tui) for the
	// audit log.
	Source string `json:"source,omitempty"`
}

// RejectedAlias expands to every rejected host inside DecryptChange.AddNever.
const RejectedAlias = "@rejected"

// SysProxyRequest toggles the system proxy.
type SysProxyRequest struct {
	Enabled bool   `json:"enabled"`
	Confirm string `json:"confirm,omitempty"`
}

// Mobile describes the LAN listener that phones and other devices on the
// same network use (`pano mobile`). Off by default: the proxy only listens on
// loopback until asked. Devices lists every remote client the proxy has seen,
// most recent first — the full list, always.
type Mobile struct {
	Enabled   bool     `json:"enabled"`
	Addr      string   `json:"addr,omitempty"` // ip:port the LAN listener is bound to
	IP        string   `json:"ip,omitempty"`
	Port      int      `json:"port,omitempty"`
	URL       string   `json:"url,omitempty"`       // setup page, e.g. http://192.168.1.23:9091
	Interface string   `json:"interface,omitempty"` // en0
	Network   string   `json:"network,omitempty"`   // Wi-Fi SSID when known
	MagicURL  string   `json:"magic_url,omitempty"` // http://pano.internal — same page once the proxy is set
	Warning   string   `json:"warning,omitempty"`   // e.g. the Mac's LAN address changed since enabling
	LastAddr  string   `json:"last_addr,omitempty"` // where the listener was, once it has been closed — a phone may still point there
	Devices   []Device `json:"devices"`
}

// Device is one remote client of the proxy and how far it has got through
// setup. Proxy means it has routed something through pano; TLS means it has
// accepted pano's certificate at least once; Rejected counts refused
// handshakes (certificate not trusted yet, or an app that pins).
type Device struct {
	IP        string    `json:"ip"`
	Name      string    `json:"name,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Requests  int       `json:"requests"`
	Decrypted int       `json:"decrypted"`
	Rejected  int       `json:"rejected"`
	Proxy     bool      `json:"proxy"`
	TLS       bool      `json:"tls"`
}

// Label is the device's name, or its IP when the name is unknown.
func (d Device) Label() string {
	if d.Name != "" {
		return d.Name
	}
	return d.IP
}

// MobileRequest turns the LAN listener on or off. IP and Port are optional
// overrides (default: the Mac's Wi-Fi address and the proxy port).
type MobileRequest struct {
	Enabled bool   `json:"enabled"`
	IP      string `json:"ip,omitempty"`
	Port    int    `json:"port,omitempty"`
}

// TailRequest long-polls for new flows.
type TailRequest struct {
	Since  string     `json:"since,omitempty"` // "now", event seq, or flow id
	WaitMS int        `json:"wait_ms,omitempty"`
	Filter FlowFilter `json:"filter,omitempty"`
}

// TailResult is a batch of rows.
type TailResult struct {
	Flows  []FlowRow `json:"flows"`
	Cursor string    `json:"cursor"`
	Held   []Held    `json:"held,omitempty"`
}

// Stats are counters for diagnostics.
type Stats map[string]int64
