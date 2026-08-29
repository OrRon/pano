package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/flow"
)

// sampleModel builds a model populated with realistic data and no daemon.
func sampleModel(w, h int) *Model {
	m := New(client.New("/nonexistent"), "v0.1.0")
	m.width, m.height, m.ready, m.dark = w, h, true, true
	m.now = time.Date(2026, 8, 28, 12, 3, 41, 0, time.Local)
	m.status = api.Status{
		Version: "v0.1.0", PID: 4242, Uptime: "2h14m", ProxyAddr: "127.0.0.1:9091", Capturing: true, Session: "checkout-bug",
		Flows: 1318, FlowsTotal: 1318, Rules: 2, RulesEnabled: 1, Held: 1, ActiveConns: 3,
		CA: api.CAStatus{Trusted: true, Supported: true}, SystemProxy: api.SysProxy{Supported: true, Enabled: true, SetByPano: true, Detail: "Wi-Fi"},
		Redaction: true,
		Mobile: api.Mobile{
			Enabled: true, Addr: "192.168.1.23:9091", IP: "192.168.1.23", Port: 9091, Interface: "en0", Network: "Home",
			URL: "http://192.168.1.23:9091", MagicURL: "http://pano.internal",
			Devices: []api.Device{{IP: "192.168.1.40", Name: "iPhone · iOS 17.5", Proxy: true, TLS: true, Requests: 42, Decrypted: 12, LastSeen: m.now}},
		},
		Decrypt: api.Decrypt{
			Mode: "all", Only: []string{},
			Never: []string{"*.push.apple.com", "*.icloud.com", "*.icloud-content.com", "*.apple-cloudkit.com", "*.ls.apple.com", "whatsapp.net", "*.bank.example"},
			Rejected: []api.RejectedHost{
				{Host: "mmg.whatsapp.net", Count: 14, Last: m.now.Add(-2 * time.Minute), Error: "client closed connection during TLS handshake"},
				{Host: "media-mrs2-3.cdn.whatsapp.net", Count: 9, Last: m.now.Add(-7 * time.Minute), Error: "client closed connection during TLS handshake"},
			},
		},
	}
	base := m.now
	rows := []api.FlowRow{
		{ID: 1318, Short: "19a6", Time: base, Kind: flow.KindHTTP, Method: "POST", Host: "api.anthropic.com", Path: "/v1/messages", Status: 0, Duration: "1.2s", Up: 8100, Down: 3400, Type: "sse", Flags: []string{"llm", "stream", "active"}, State: flow.StateActive},
		{ID: 1317, Short: "19a5", Time: base.Add(-2 * time.Second), Kind: flow.KindHTTP, Method: "POST", Host: "api.openai.com", Path: "/v1/chat/completions", Status: 429, Duration: "220ms", Up: 1200, Down: 300, Type: "json", Flags: []string{"llm", "err"}, State: flow.StateDone},
		{ID: 1316, Short: "19a4", Time: base.Add(-3 * time.Second), Kind: flow.KindHTTP, Method: "GET", Host: "cdn.shop.example", Path: "/assets/app.3f2a1c.js", Status: 304, Duration: "18ms", Up: 0, Down: 0, Type: "js", State: flow.StateDone, Client: "192.168.1.40:52011"},
		{ID: 1315, Short: "19a3", Time: base.Add(-5 * time.Second), Kind: flow.KindHTTP, Method: "POST", Host: "api.stripe.com", Path: "/v1/payment_intents", Status: 402, Duration: "310ms", Up: 640, Down: 1100, Type: "json", Flags: []string{"err"}, State: flow.StateDone, Error: ""},
		{ID: 1314, Short: "19a2", Time: base.Add(-6 * time.Second), Kind: flow.KindHTTP, Method: "POST", Host: "api.stripe.com", Path: "/v1/customers", Status: 200, Duration: "3.02s", Up: 640, Down: 2200, Type: "json", Flags: []string{"delay"}, State: flow.StateDone},
		{ID: 1313, Short: "19a1", Time: base.Add(-9 * time.Second), Kind: flow.KindHTTP, Method: "GET", Host: "shop.example", Path: "/checkout?step=payment&cart=8813", Status: 200, Duration: "141ms", Up: 0, Down: 48200, Type: "html", State: flow.StateDone},
		{ID: 1312, Short: "19a0", Time: base.Add(-9 * time.Second), Kind: flow.KindWebSocket, Method: "GET", Host: "realtime.shop.example", Path: "/socket", Status: 101, Duration: "42.1s", Up: 12000, Down: 88000, Type: "ws", State: flow.StateDone},
		{ID: 1311, Short: "199z", Time: base.Add(-12 * time.Second), Kind: flow.KindHTTP, Method: "PUT", Host: "api.shop.example", Path: "/v2/orders/8813", Status: 0, Duration: "31s", Up: 900, Down: 0, Type: "", Flags: []string{"held"}, State: flow.StateHeld},
		{ID: 1310, Short: "199y", Time: base.Add(-15 * time.Second), Kind: flow.KindHTTP, Method: "GET", Host: "api.shop.example", Path: "/v2/orders/8813", Status: 502, Duration: "10.0s", Up: 0, Down: 0, Type: "", Flags: []string{"err"}, State: flow.StateFailed, Error: "upstream timeout: dial tcp 10.0.0.9:443: i/o timeout"},
		{ID: 1309, Short: "199x", Time: base.Add(-18 * time.Second), Kind: flow.KindHTTP, Method: "GET", Host: "cdn.shop.example", Path: "/assets/logo.svg", Status: 200, Duration: "12ms", Up: 0, Down: 4100, Type: "img", State: flow.StateDone},
		{ID: 1308, Short: "199w", Time: base.Add(-20 * time.Second), Kind: flow.KindTunnel, Method: "", Host: "gateway.icloud.com", Path: "", Status: 0, Duration: "5m12s", Up: 30000, Down: 120000, Type: "tunnel", Flags: []string{"never"}, State: flow.StateDone},
		{ID: 1307, Short: "199v", Time: base.Add(-25 * time.Second), Kind: flow.KindHTTP, Method: "DELETE", Host: "api.shop.example", Path: "/v2/cart/items/77", Status: 204, Duration: "88ms", Up: 0, Down: 0, Type: "", State: flow.StateDone},
		{ID: 1306, Short: "199u", Time: base.Add(-40 * time.Second), Kind: flow.KindHTTP, Method: "POST", Host: "api.openai.com", Path: "/v1/chat/completions", Status: 200, Duration: "4.4s", Up: 2200, Down: 5100, Type: "sse", Flags: []string{"llm", "stream", "replay"}, State: flow.StateDone},
	}
	for i := 14; i < 40; i++ {
		rows = append(rows, api.FlowRow{ID: flow.ID(1319 - i), Short: flow.ID(1319 - i).Short(), Time: base.Add(-time.Duration(i) * 3 * time.Second), Kind: flow.KindHTTP, Method: "GET", Host: "api.shop.example", Path: "/v2/products?page=" + itoa(i), Status: 200, Duration: "64ms", Up: 0, Down: 9100, Type: "json", State: flow.StateDone})
	}
	m.table.reset(rows)
	m.applyFilter()
	m.table.fresh[1318] = m.now.Add(-300 * time.Millisecond)
	m.rules = []api.Rule{
		{ID: "r_7k3q", Name: "slow stripe", Match: api.Match{Host: "api.stripe.com"}, Actions: []api.Action{{Type: "delay", MS: 3000}}, Hits: 4, TTLSeconds: 600},
		{ID: "r_m2x1", Name: "hold orders", Match: api.Match{Host: "api.shop.example", Path: "/v2/orders/*", Method: []string{"PUT"}}, Actions: []api.Action{{Type: "breakpoint"}}, Hits: 1, Enabled: boolp(false)},
	}
	m.held = []api.Held{{ID: 1311, Short: "199z", Phase: "request", Method: "PUT", URL: "https://api.shop.example/v2/orders/8813", Since: base.Add(-31 * time.Second), Age: "31s", RuleID: "r_m2x1"}}
	m.detailID = 1317
	m.detailQ = api.FlowQuery{Part: "both", View: api.ViewSummary}
	m.detail = api.FlowDetail{Text: sampleDetail}
	m.explain = api.ExplainResult{Provider: "openai-chat", Model: "gpt-5", Text: sampleExplain}
	m.marked = 1306
	m.layoutViewport()
	m.setPaneContent()
	return m
}

func boolp(b bool) *bool { return &b }

const sampleDetail = `19a5 POST https://api.openai.com/v1/chat/completions → 429 Too Many Requests  HTTP/2.0 220ms  client 127.0.0.1:53929
timing: ttfb 214ms total 220ms (conn reused)

== request ==
Authorization: Bearer sk-…a1b2 hash:9f3c
Content-Type: application/json
User-Agent: openai-node/5.12.0
body: application/json 1.2kB sha256:38df86f6
json object (5 keys)
model: str "gpt-5"
messages: array[14] of object{role, content}
tools: array[6] of object{type, function}
max_tokens: int 8192
stream: bool true
notable:
  model: "gpt-5"

== response ==
Content-Type: application/json
Retry-After: 2
X-Request-Id: req_8f3a…
body: application/json 302B sha256:5b0b304f
json object (1 keys)
error: object{4: message, type, param, code}
notable:
  error.message: "Rate limit reached for gpt-5 in organization org-… on tokens per min (TPM): Limit 30000, Used 29812, Requested 1204."
  error.type: "tokens"
  error.code: "rate_limit_exceeded"`

const sampleExplain = `provider: openai-chat  model: gpt-5  stream: no  status: 429
request: system 1.2k chars · 14 messages (u7/a7) · 6 tools (search_products, add_to_cart, checkout, get_order, …(+2)) · max_tokens 8,192 · temperature 0.2
usage: -
final: (no content)
errors:
  rate_limit_exceeded (tokens): Rate limit reached for gpt-5 in organization org-… on tokens per min (TPM): Limit 30000, Used 29812, Requested 1204.`

const sampleExplainOK = `provider: anthropic  model: claude-opus-5  stream: yes  status: 200
request: system 155 chars · 4 messages (u3/a1) · 6 tools (Read, Edit, Bash, Grep, Glob, …(+1)) · max_tokens 8,192 · temperature 0.2 · stream yes · thinking budget 2,048
usage: in 4,812 (cache_read 4,100, cache_write 0) · out 356 · total 5,168 · stop: tool_use
final:
  [thinking] (154 chars, hidden — include=thinking to show)
  [text] "I'll read the store package first." (34 chars)
  [tool_use] Read {"file_path":"/home/dev/app/internal/store/store.go","limit":120}
errors: none
messages:
  system: "You are a coding agent working in /home/dev/app."
  user: "Fix the flaky TestStorePrune test"
  assistant: "Let me look at the store package."
  user: "go ahead"`

const sampleSchema = `19a5 POST https://api.openai.com/v1/chat/completions → 429 Too Many Requests  HTTP/2.0 220ms  client 127.0.0.1:53929

== response ==
Content-Type: application/json
Retry-After: 2
body: application/json 302B sha256:5b0b304f
{
  error: {
    message: str,
    type: "tokens"|"requests",
    param?: null,
    code: "rate_limit_exceeded",
    request_id: str<uuid>,
    retry_after: int,
    retryable: bool,
    limits: [ { name: str, limit: int, used: int, window: "1m"|"1h" } ]
  }
}`

const samplePretty = `19a5 POST https://api.openai.com/v1/chat/completions → 429 Too Many Requests  HTTP/2.0 220ms  client 127.0.0.1:53929

== response ==
Content-Type: application/json
Retry-After: 2
body: application/json 302B sha256:5b0b304f
{
  "error": {
    "message": "Rate limit reached for gpt-5 in organization org-… on tokens per min (TPM): Limit 30000, Used 29812, Requested 1204.",
    "type": "tokens",
    "param": null,
    "code": "rate_limit_exceeded",
    "retry_after": 2,
    "retryable": true
  }
}`

// TestSnapshots renders the UI at several sizes and modes into the scratch
// dir so the look can be reviewed. It also asserts basic layout invariants.
func TestSnapshots(t *testing.T) {
	dir := os.Getenv("PANO_SNAPSHOT_DIR")
	sizes := [][2]int{{80, 24}, {120, 40}, {200, 60}}
	modes := []struct {
		name string
		set  func(m *Model)
	}{
		{"list", func(m *Model) { m.mode = modeList }},
		{"detail", func(m *Model) { m.mode = modeDetail; m.pane, m.tab = paneBody, tabSummary; m.setPaneContent() }},
		{"explain", func(m *Model) { m.mode = modeDetail; m.pane, m.tab = paneExplain, tabExplain; m.setPaneContent() }},
		{"explain-ok", func(m *Model) {
			m.mode = modeDetail
			m.detailID = 1306
			m.explain = api.ExplainResult{Provider: "anthropic", Model: "claude-opus-5", Text: sampleExplainOK}
			m.pane, m.tab = paneExplain, tabExplain
			m.setPaneContent()
		}},
		{"schema", func(m *Model) {
			m.mode = modeDetail
			m.detailQ = api.FlowQuery{Part: "response", View: api.ViewSchema}
			m.detail = api.FlowDetail{Text: sampleSchema}
			m.pane, m.tab = paneBody, tabResponse
			m.setPaneContent()
		}},
		{"pretty", func(m *Model) {
			m.mode = modeDetail
			m.detailQ = api.FlowQuery{Part: "response", View: api.ViewPretty}
			m.detail = api.FlowDetail{Text: samplePretty}
			m.pane, m.tab = paneBody, tabResponse
			m.setPaneContent()
		}},
		{"rules", func(m *Model) { m.mode = modeRules }},
		{"held", func(m *Model) { m.mode = modeHeld }},
		{"decrypt", func(m *Model) { m.mode = modeDecrypt; m.drawerIx = 5 }},
		{"mobile", func(m *Model) { m.mode = modeMobile }},
		{"mobile-off", func(m *Model) {
			m.mode = modeMobile
			m.status.Mobile.Enabled = false
			m.status.Mobile.LastAddr = m.status.Mobile.Addr
			m.status.Mobile.Addr = ""
		}},
		{"actions", func(m *Model) { m.mode = modeActions; m.prevMode = modeList; m.cursor = 1 }},
		{"decrypt-only", func(m *Model) {
			m.mode = modeList
			m.status.Decrypt.Mode = "only"
			m.status.Decrypt.Only = []string{"api.anthropic.com", "localhost"}
			m.status.Decrypt.Rejected = nil
		}},
		{"filter", func(m *Model) {
			m.mode = modeFilter
			m.input.SetValue("host=api.stripe.com status=!2xx")
			m.input.Focus()
		}},
		{"help", func(m *Model) { m.mode = modeHelp; m.prevMode = modeList }},
		{"paused", func(m *Model) {
			m.mode = modeList
			m.paused = true
			m.status.Capturing = false
			m.status.SystemProxy.Enabled = false
			m.status.CA.Trusted = false
		}},
		{"empty", func(m *Model) { m.mode = modeList; m.table.reset(nil); m.applyFilter() }},
	}
	for _, sz := range sizes {
		for _, md := range modes {
			m := sampleModel(sz[0], sz[1])
			md.set(m)
			out := m.View().Content
			lines := strings.Split(out, "\n")
			if len(lines) > sz[1] {
				t.Errorf("%s %dx%d: rendered %d lines (> height)", md.name, sz[0], sz[1], len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > sz[0] {
					t.Errorf("%s %dx%d: line %d is %d cells wide (> width): %q", md.name, sz[0], sz[1], i, w, ansi.Strip(l))
				}
			}
			if dir != "" {
				name := filepath.Join(dir, md.name+"-"+itoa(sz[0])+"x"+itoa(sz[1])+".txt")
				_ = os.WriteFile(name, []byte(out), 0o600)
			}
		}
	}
	_ = lipgloss.NewStyle()
}
