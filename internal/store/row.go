package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

// LLMHosts are recognised LLM API endpoints (glob).
var LLMHosts = []string{
	"api.anthropic.com", "api.openai.com", "*.openai.azure.com",
	"generativelanguage.googleapis.com", "openrouter.ai", "api.groq.com",
	"api.mistral.ai", "api.together.xyz", "api.x.ai", "api.deepseek.com",
	"bedrock-runtime.*.amazonaws.com",
}

// IsLLMHost reports whether host is a known LLM API.
func IsLLMHost(host string) bool {
	for _, p := range LLMHosts {
		if matchHostGlob(p, host) {
			return true
		}
	}
	return false
}

func matchHostGlob(p, h string) bool {
	if !strings.ContainsAny(p, "*?") {
		return strings.EqualFold(p, h)
	}
	// reuse glob semantics without importing glob to keep this file cheap
	pp := strings.Split(strings.ToLower(p), "*")
	s := strings.ToLower(h)
	if !strings.HasPrefix(s, pp[0]) {
		return false
	}
	s = s[len(pp[0]):]
	for i := 1; i < len(pp); i++ {
		idx := strings.Index(s, pp[i])
		if idx < 0 {
			return false
		}
		if i == len(pp)-1 && pp[i] != "" && !strings.HasSuffix(s, pp[i]) {
			return false
		}
		s = s[idx+len(pp[i]):]
	}
	return true
}

// Row renders the one-line list representation.
func Row(f *flow.Flow) api.FlowRow {
	r := api.FlowRow{
		ID: f.ID, Short: f.ID.Short(), Time: f.T.Start, Kind: f.Kind, Method: f.Method,
		Host: f.Host, Path: f.Path, Status: f.Status, Up: f.ReqBody.Size, Down: f.RespBody.Size,
		Error: f.Error, Proto: f.Proto, Client: f.Client, State: f.State,
	}
	if f.Query != "" {
		r.Path += "?" + f.Query
	}
	if f.Port != 0 && (f.Scheme != "https" || f.Port != 443) && (f.Scheme != "http" || f.Port != 80) {
		r.Host = fmt.Sprintf("%s:%d", f.Host, f.Port)
	}
	d := f.T.Total()
	r.DurMS = float64(d) / float64(time.Millisecond)
	r.Duration = FormatDuration(d)
	ct := f.RespBody.MIME
	if ct == "" {
		ct = f.RespHeaders.Get("Content-Type")
	}
	r.Type = TypeClass(ct)
	if f.Kind == flow.KindTunnel {
		r.Type = "tunnel"
	}
	if f.Kind == flow.KindWebSocket {
		r.Type = "ws"
	}
	r.Flags = Flags(f)
	return r
}

// Flags computes the marker list shown in rows.
func Flags(f *flow.Flow) []string {
	var flags []string
	if IsLLMHost(f.Host) {
		flags = append(flags, "llm")
	}
	if r := f.RespBody.MIME; r == "text/event-stream" || (r == "" && f.RespHeaders.Get("Content-Type") == "text/event-stream") {
		flags = append(flags, "stream")
	}
	if f.Error != "" || f.Status >= 400 {
		flags = append(flags, "err")
	}
	if f.State == flow.StateHeld {
		flags = append(flags, "held")
	}
	if f.State == flow.StateActive {
		flags = append(flags, "active")
	}
	if f.Replay {
		flags = append(flags, "replay")
	}
	seen := map[string]bool{}
	for _, h := range f.Rules {
		if !seen[h.Action] {
			seen[h.Action] = true
			flags = append(flags, h.Action)
		}
	}
	if f.ReqBody.Truncated || f.RespBody.Truncated {
		flags = append(flags, "trunc")
	}
	flags = append(flags, f.Tags...)
	return flags
}

// FormatDuration renders compactly: 850µs, 18ms, 3.21s, 1m05s.
func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// FormatBytes renders 0, 812, 8.1k, 22.4k, 1.2M.
func FormatBytes(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	default:
		return fmt.Sprintf("%.1fG", float64(n)/1_000_000_000)
	}
}

// Query lists flows from the ring newest-first honouring filter, limit and
// cursor ("before:<id>"). Returns rows, total matches, and the next cursor.
// bodies, when non-nil, lets Q search decoded textual bodies too.
func Query(m *Mem, filter api.FlowFilter, limit int, now time.Time, bodies BodyFunc) api.FlowList {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var before flow.ID
	if strings.HasPrefix(filter.Cursor, "before:") {
		if id, ok := flow.ParseShort(strings.TrimPrefix(filter.Cursor, "before:")); ok {
			before = id
		}
	}
	mt := Compile(filter, now).WithBodies(bodies)
	out := api.FlowList{}
	var last flow.ID
	m.Each(func(f *flow.Flow) bool {
		if out.LastID == 0 {
			out.LastID = f.ID
		}
		if before != 0 && f.ID >= before {
			return true
		}
		if !mt.Match(f) {
			return true
		}
		out.Total++
		if len(out.Flows) < limit {
			out.Flows = append(out.Flows, Row(f))
			last = f.ID
		}
		return true
	})
	if out.Total > len(out.Flows) && last != 0 {
		out.Cursor = "before:" + last.Short()
	}
	return out
}
