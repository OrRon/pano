package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

// Client talks to the daemon.
type Client struct {
	http  *http.Client
	base  string
	token string
	sock  string
}

// ErrNotRunning is returned when the daemon socket cannot be reached.
var ErrNotRunning = errors.New("pano daemon is not running (start it with `pano start`)")

// New returns a client for the Unix socket at path.
func New(sock string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns:        4,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 4,
	}
	return &Client{http: &http.Client{Transport: tr}, base: "http://pano", sock: sock}
}

// NewTCP returns a client for a loopback TCP address with a bearer token.
func NewTCP(addr, token string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{DisableCompression: true}}, base: "http://" + addr, token: token}
}

// Ping reports whether the daemon answers.
func (c *Client) Ping(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := c.Status(ctx)
	return err == nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) || strings.Contains(err.Error(), "connect:") || strings.Contains(err.Error(), "no such file") {
			return ErrNotRunning
		}
		return err
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if w, ok := out.(*[]byte); ok {
		// Raw endpoints (bodies, ca.pem) return bytes; errors still use the envelope.
		if resp.StatusCode < 400 {
			b, err := io.ReadAll(resp.Body)
			*w = b
			return err
		}
	}
	if !strings.HasPrefix(ct, "application/json") {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode >= 400 {
			return fmt.Errorf("pano: %s: %s", resp.Status, strings.TrimSpace(string(b)))
		}
		return fmt.Errorf("pano: unexpected content-type %q", ct)
	}
	var env api.Envelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("pano: decode response: %w", err)
	}
	if !env.OK {
		if env.Error == nil {
			return fmt.Errorf("pano: request failed (%s)", resp.Status)
		}
		return env.Error
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// Status returns daemon status.
func (c *Client) Status(ctx context.Context) (api.Status, error) {
	var s api.Status
	return s, c.do(ctx, "GET", "/v1/status", nil, &s)
}

// Stats returns counters.
func (c *Client) Stats(ctx context.Context) (api.Stats, error) {
	var s api.Stats
	return s, c.do(ctx, "GET", "/v1/stats", nil, &s)
}

// Capture controls recording.
func (c *Client) Capture(ctx context.Context, req api.CaptureRequest) (api.Status, error) {
	var s api.Status
	return s, c.do(ctx, "POST", "/v1/capture", req, &s)
}

// Sessions lists sessions.
func (c *Client) Sessions(ctx context.Context) ([]api.Session, error) {
	var s []api.Session
	return s, c.do(ctx, "GET", "/v1/sessions", nil, &s)
}

// StartSession begins a new named session.
func (c *Client) StartSession(ctx context.Context, name string) (api.Session, error) {
	var s api.Session
	return s, c.do(ctx, "POST", "/v1/sessions", map[string]string{"name": name}, &s)
}

// DeleteSession removes a session and its flows.
func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/v1/sessions/"+url.PathEscape(id), nil, nil)
}

// FilterQuery encodes a filter as URL query parameters.
func FilterQuery(f api.FlowFilter) url.Values {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("q", f.Q)
	set("host", f.Host)
	set("path", f.Path)
	set("status", f.Status)
	set("since", f.Since)
	set("until", f.Until)
	set("content_type", f.ContentType)
	set("tag", f.Tag)
	set("rule", f.Rule)
	set("state", f.State)
	set("kind", f.Kind)
	set("session", f.Session)
	set("cursor", f.Cursor)
	if len(f.Method) > 0 {
		q.Set("method", strings.Join(f.Method, ","))
	}
	if len(f.Fields) > 0 {
		q.Set("fields", strings.Join(f.Fields, ","))
	}
	if f.MinBytes > 0 {
		q.Set("min_bytes", strconv.FormatInt(f.MinBytes, 10))
	}
	if f.HasError {
		q.Set("has_error", "1")
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	return q
}

// Flows lists flows.
func (c *Client) Flows(ctx context.Context, f api.FlowFilter) (api.FlowList, error) {
	var l api.FlowList
	return l, c.do(ctx, "GET", "/v1/flows?"+FilterQuery(f).Encode(), nil, &l)
}

// Flow returns a rendered flow.
func (c *Client) Flow(ctx context.Context, id flow.ID, q api.FlowQuery) (api.FlowDetail, error) {
	v := url.Values{}
	if q.Part != "" {
		v.Set("part", q.Part)
	}
	if q.View != "" {
		v.Set("view", q.View)
	}
	if q.Path != "" {
		v.Set("path", q.Path)
	}
	if q.MaxBytes > 0 {
		v.Set("max_bytes", strconv.Itoa(q.MaxBytes))
	}
	if q.Headers != nil {
		v.Set("headers", strconv.FormatBool(*q.Headers))
	}
	if q.RevealSecrets {
		v.Set("reveal", "1")
	}
	var d api.FlowDetail
	return d, c.do(ctx, "GET", "/v1/flows/"+id.Short()+"?"+v.Encode(), nil, &d)
}

// FlowRaw returns the stored flow record.
func (c *Client) FlowRaw(ctx context.Context, id flow.ID) (*flow.Flow, error) {
	var f flow.Flow
	if err := c.do(ctx, "GET", "/v1/flows/"+id.Short()+"/raw", nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Body returns raw (or decoded) body bytes.
func (c *Client) Body(ctx context.Context, id flow.ID, part string, decode bool) ([]byte, error) {
	var b []byte
	p := "/v1/flows/" + id.Short() + "/body/" + part
	if decode {
		p += "?decode=1"
	}
	return b, c.do(ctx, "GET", p, nil, &b)
}

// WSMessages returns captured WebSocket messages.
func (c *Client) WSMessages(ctx context.Context, id flow.ID, limit int) ([]flow.WSMessage, error) {
	var ms []flow.WSMessage
	return ms, c.do(ctx, "GET", fmt.Sprintf("/v1/flows/%s/ws?limit=%d", id.Short(), limit), nil, &ms)
}

// Replay re-sends a flow.
func (c *Client) Replay(ctx context.Context, id flow.ID, req api.ReplayRequest) (api.ReplayResult, error) {
	var r api.ReplayResult
	return r, c.do(ctx, "POST", "/v1/flows/"+id.Short()+"/replay", req, &r)
}

// Diff compares flows.
func (c *Client) Diff(ctx context.Context, req api.DiffRequest) (api.DiffResult, error) {
	v := url.Values{"a": {req.A.Short()}, "b": {req.B.Short()}}
	if req.Part != "" {
		v.Set("part", req.Part)
	}
	if req.Path != "" {
		v.Set("path", req.Path)
	}
	if req.MaxChanges > 0 {
		v.Set("max_changes", strconv.Itoa(req.MaxChanges))
	}
	if len(req.IgnoreHeaders) > 0 {
		v.Set("ignore_headers", strings.Join(req.IgnoreHeaders, ","))
	}
	var r api.DiffResult
	return r, c.do(ctx, "GET", "/v1/flows/diff?"+v.Encode(), nil, &r)
}

// Explain digests LLM traffic.
func (c *Client) Explain(ctx context.Context, id flow.ID, req api.ExplainRequest) (api.ExplainResult, error) {
	v := url.Values{}
	if len(req.Include) > 0 {
		v.Set("include", strings.Join(req.Include, ","))
	}
	if req.MaxChars > 0 {
		v.Set("max_chars", strconv.Itoa(req.MaxChars))
	}
	if req.Provider != "" {
		v.Set("provider", req.Provider)
	}
	var r api.ExplainResult
	return r, c.do(ctx, "GET", "/v1/flows/"+id.Short()+"/explain?"+v.Encode(), nil, &r)
}

// Tail long-polls for new flows.
func (c *Client) Tail(ctx context.Context, req api.TailRequest) (api.TailResult, error) {
	var r api.TailResult
	return r, c.do(ctx, "POST", "/v1/tail", req, &r)
}

// Events streams bus events until ctx is cancelled. The returned channel is
// closed on disconnect.
func (c *Client) Events(ctx context.Context, types []string, host string) (<-chan flow.Event, error) {
	v := url.Values{}
	if len(types) > 0 {
		v.Set("types", strings.Join(types, ","))
	}
	if host != "" {
		v.Set("host", host)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/v1/events?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req) //nolint:bodyclose // closed by the reader goroutine below
	if err != nil {
		return nil, ErrNotRunning
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("pano: events: %s", resp.Status)
	}
	ch := make(chan flow.Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 32<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev flow.Event
			if json.Unmarshal([]byte(line[6:]), &ev) == nil {
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

// Rules lists rules.
func (c *Client) Rules(ctx context.Context) ([]api.Rule, error) {
	var r []api.Rule
	return r, c.do(ctx, "GET", "/v1/rules", nil, &r)
}

// Presets lists rule presets.
func (c *Client) Presets(ctx context.Context) ([]api.Preset, error) {
	var p []api.Preset
	return p, c.do(ctx, "GET", "/v1/rules/presets", nil, &p)
}

// AddRule creates a rule.
func (c *Client) AddRule(ctx context.Context, req api.RuleAddRequest) (api.Rule, error) {
	var r api.Rule
	return r, c.do(ctx, "POST", "/v1/rules", req, &r)
}

// UpdateRule patches a rule.
func (c *Client) UpdateRule(ctx context.Context, id string, p api.RulePatch) (api.Rule, error) {
	var r api.Rule
	return r, c.do(ctx, "PATCH", "/v1/rules/"+url.PathEscape(id), p, &r)
}

// RemoveRule deletes a rule.
func (c *Client) RemoveRule(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/v1/rules/"+url.PathEscape(id), nil, nil)
}

// RemoveAllRules deletes every rule.
func (c *Client) RemoveAllRules(ctx context.Context) (int, error) {
	var r struct {
		Removed int `json:"removed"`
	}
	return r.Removed, c.do(ctx, "DELETE", "/v1/rules", nil, &r)
}

// Held lists held requests.
func (c *Client) Held(ctx context.Context) ([]api.Held, error) {
	var h []api.Held
	return h, c.do(ctx, "GET", "/v1/held", nil, &h)
}

// Resume releases a held request.
func (c *Client) Resume(ctx context.Context, id flow.ID, req api.ResumeRequest) error {
	return c.do(ctx, "POST", "/v1/held/"+id.Short(), req, nil)
}

// Decrypt returns the decrypt policy and recently rejected hosts.
func (c *Client) Decrypt(ctx context.Context) (api.Decrypt, error) {
	var d api.Decrypt
	return d, c.do(ctx, "GET", "/v1/decrypt", nil, &d)
}

// ChangeDecrypt applies a partial update and returns the new policy.
func (c *Client) ChangeDecrypt(ctx context.Context, ch api.DecryptChange) (api.Decrypt, error) {
	var d api.Decrypt
	return d, c.do(ctx, "PATCH", "/v1/decrypt", ch, &d)
}

// HAR exports or imports.
func (c *Client) HAR(ctx context.Context, req api.HARRequest) (api.HARResult, error) {
	var r api.HARResult
	return r, c.do(ctx, "POST", "/v1/har", req, &r)
}

// CAPEM fetches the root certificate.
func (c *Client) CAPEM(ctx context.Context) ([]byte, error) {
	var b []byte
	return b, c.do(ctx, "GET", "/v1/ca.pem", nil, &b)
}

// SysProxy returns system proxy state.
func (c *Client) SysProxy(ctx context.Context) (api.SysProxy, error) {
	var s api.SysProxy
	return s, c.do(ctx, "GET", "/v1/sysproxy", nil, &s)
}

// SetSysProxy toggles the system proxy.
func (c *Client) SetSysProxy(ctx context.Context, req api.SysProxyRequest) (api.SysProxy, error) {
	var s api.SysProxy
	return s, c.do(ctx, "POST", "/v1/sysproxy", req, &s)
}

// Mobile returns the LAN listener state and the devices seen.
func (c *Client) Mobile(ctx context.Context) (api.Mobile, error) {
	var m api.Mobile
	return m, c.do(ctx, "GET", "/v1/mobile", nil, &m)
}

// SetMobile opens or closes the proxy to the LAN.
func (c *Client) SetMobile(ctx context.Context, req api.MobileRequest) (api.Mobile, error) {
	var m api.Mobile
	return m, c.do(ctx, "POST", "/v1/mobile", req, &m)
}

// Config returns the live configuration.
func (c *Client) Config(ctx context.Context) (json.RawMessage, error) {
	var r json.RawMessage
	return r, c.do(ctx, "GET", "/v1/config", nil, &r)
}

// Attach registers this process as a terminal UI and keeps the connection
// open until ctx ends. The returned channel closes when the daemon closes
// the stream (it stopped, or was turned off elsewhere). With own, the
// daemon turns itself off once the connection drops (app mode).
func (c *Client) Attach(ctx context.Context, own bool) (<-chan struct{}, error) {
	q := ""
	if own {
		q = "?own=1"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/v1/attach"+q, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req) //nolint:bodyclose // closed by the reader goroutine below
	if err != nil {
		return nil, ErrNotRunning
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("pano: attach: %s", resp.Status)
	}
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()
	return gone, nil
}

// Disown tells the daemon to keep running after the owning UI closes.
func (c *Client) Disown(ctx context.Context) (api.Lifecycle, error) {
	var l api.Lifecycle
	return l, c.do(ctx, "POST", "/v1/disown", nil, &l)
}

// Off restores the system proxy and stops the daemon (`pano off`).
func (c *Client) Off(ctx context.Context) error {
	return c.do(ctx, "POST", "/v1/off", nil, nil)
}

// Shutdown asks the daemon to stop.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.do(ctx, "POST", "/v1/shutdown", nil, nil)
}
