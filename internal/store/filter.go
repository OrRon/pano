package store

import (
	"strconv"
	"strings"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/glob"
	"github.com/orron/pano/internal/mimeclass"
)

// Matcher is a compiled FlowFilter.
type Matcher struct {
	f        api.FlowFilter
	q        string
	since    time.Time
	sinceID  flow.ID
	until    time.Time
	statusFn func(int) bool
	methods  map[string]bool
	ctClass  string
}

// Compile prepares a filter. now anchors relative times.
func Compile(f api.FlowFilter, now time.Time) *Matcher {
	m := &Matcher{f: f, q: strings.ToLower(f.Q)}
	if f.Since != "" {
		if t, ok := parseTime(f.Since, now); ok {
			m.since = t
		} else if id, ok := flow.ParseShort(f.Since); ok {
			m.sinceID = id
		}
	}
	if f.Until != "" {
		if t, ok := parseTime(f.Until, now); ok {
			m.until = t
		}
	}
	m.statusFn = StatusMatcher(f.Status)
	if len(f.Method) > 0 {
		m.methods = make(map[string]bool, len(f.Method))
		for _, mm := range f.Method {
			for _, p := range strings.Split(mm, "|") {
				m.methods[strings.ToUpper(strings.TrimSpace(p))] = true
			}
		}
	}
	m.ctClass = strings.ToLower(f.ContentType)
	return m
}

// Match reports whether fl satisfies the filter (excluding full-text search
// over bodies, which the SQLite layer handles; here Q matches URL + headers).
func (m *Matcher) Match(fl *flow.Flow) bool {
	f := &m.f
	if m.sinceID != 0 && fl.ID <= m.sinceID {
		return false
	}
	if !m.since.IsZero() && fl.T.Start.Before(m.since) {
		return false
	}
	if !m.until.IsZero() && fl.T.Start.After(m.until) {
		return false
	}
	if f.Host != "" && !glob.Match(f.Host, fl.Host) {
		return false
	}
	if f.Path != "" && !matchPath(f.Path, fl.Path) {
		return false
	}
	if m.methods != nil && !m.methods[fl.Method] {
		return false
	}
	if m.statusFn != nil && (fl.Status == 0 || !m.statusFn(fl.Status)) {
		return false
	}
	if f.MinBytes > 0 && fl.ReqBody.Size+fl.RespBody.Size < f.MinBytes {
		return false
	}
	if f.HasError && fl.Error == "" && fl.Status < 400 {
		return false
	}
	if f.Tag != "" && !contains(fl.Tags, f.Tag) {
		return false
	}
	if f.Rule != "" && !hasRule(fl, f.Rule) {
		return false
	}
	if f.Kind != "" && string(fl.Kind) != f.Kind {
		return false
	}
	if f.Session != "" && fl.Session != f.Session {
		return false
	}
	if f.State != "" && f.State != "all" && !matchState(fl, f.State) {
		return false
	}
	if m.ctClass != "" && !matchCT(m.ctClass, fl) {
		return false
	}
	if m.q != "" && !matchQ(m.q, fl) {
		return false
	}
	return true
}

func matchPath(pattern, path string) bool {
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 && strings.ContainsAny(pattern, "^$[](){}|+\\") {
		// treat as regex-ish: fall back to substring of inner
		inner := pattern[1 : len(pattern)-1]
		return strings.Contains(path, strings.Trim(inner, "^$"))
	}
	if !glob.IsPattern(pattern) {
		return strings.HasPrefix(path, pattern)
	}
	return glob.Match(pattern, path)
}

func matchState(fl *flow.Flow, s string) bool {
	switch s {
	case "held":
		return fl.State == flow.StateHeld
	case "active":
		return fl.State == flow.StateActive
	case "done":
		return fl.State == flow.StateDone
	case "failed":
		return fl.State == flow.StateFailed
	case "replayed":
		return fl.Replay
	case "mocked", "blocked":
		for _, h := range fl.Rules {
			if (s == "mocked" && h.Action == "mock") || (s == "blocked" && h.Action == "block") {
				return true
			}
		}
		return false
	}
	return true
}

func matchCT(class string, fl *flow.Flow) bool {
	ct := fl.RespBody.MIME
	if ct == "" {
		ct = fl.RespHeaders.Get("Content-Type")
	}
	return TypeClass(ct) == class || strings.HasPrefix(strings.ToLower(ct), class)
}

func matchQ(q string, fl *flow.Flow) bool {
	if strings.Contains(strings.ToLower(fl.URL()), q) {
		return true
	}
	for _, h := range []map[string][]string{fl.ReqHeaders, fl.RespHeaders} {
		for k, vs := range h {
			if strings.Contains(strings.ToLower(k), q) {
				return true
			}
			for _, v := range vs {
				if strings.Contains(strings.ToLower(v), q) {
					return true
				}
			}
		}
	}
	return strings.Contains(strings.ToLower(fl.Error), q)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func hasRule(fl *flow.Flow, id string) bool {
	for _, h := range fl.Rules {
		if h.RuleID == id || h.Name == id {
			return true
		}
	}
	return false
}

// StatusMatcher parses "500", "4xx", "400-499", "!2xx", "200|204". Nil means any.
func StatusMatcher(spec string) func(int) bool {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return nil
	}
	neg := strings.HasPrefix(spec, "!")
	spec = strings.TrimPrefix(spec, "!")
	var fns []func(int) bool
	for _, part := range strings.Split(spec, "|") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasSuffix(part, "xx") && len(part) == 3:
			d := int(part[0] - '0')
			fns = append(fns, func(s int) bool { return s/100 == d })
		case strings.Contains(part, "-"):
			lo, hi, ok := strings.Cut(part, "-")
			l, e1 := strconv.Atoi(lo)
			h, e2 := strconv.Atoi(hi)
			if ok && e1 == nil && e2 == nil {
				fns = append(fns, func(s int) bool { return s >= l && s <= h })
			}
		default:
			if n, err := strconv.Atoi(part); err == nil {
				fns = append(fns, func(s int) bool { return s == n })
			}
		}
	}
	if len(fns) == 0 {
		return nil
	}
	return func(s int) bool {
		hit := false
		for _, fn := range fns {
			if fn(s) {
				hit = true
				break
			}
		}
		return hit != neg
	}
}

func parseTime(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "now" {
		return now, s == "now"
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if d, ok := parseRelative(s); ok {
		return now.Add(-d), true
	}
	return time.Time{}, false
}

func parseRelative(s string) (time.Duration, bool) {
	s = strings.TrimPrefix(s, "-")
	if n := len(s); n > 1 && s[n-1] == 'd' {
		if v, err := strconv.ParseFloat(s[:n-1], 64); err == nil {
			return time.Duration(v * float64(24*time.Hour)), true
		}
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

// TypeClass is mimeclass.Of.
func TypeClass(ct string) string { return mimeclass.Of(ct) }

// IsTextual is mimeclass.IsTextual.
func IsTextual(class string) bool { return mimeclass.IsTextual(class) }
