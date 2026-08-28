package rules

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/glob"
)

type pathKind uint8

const (
	pathAny pathKind = iota
	pathPrefix
	pathGlob
	pathRegex
)

// pathMatcher matches a URL path as a prefix, a glob or a regexp.
type pathMatcher struct {
	kind    pathKind
	pattern string
	re      *regexp.Regexp
}

// regexMeta are the characters that mark a slash-wrapped pattern as a regexp
// rather than a plain prefix such as "/v1/".
const regexMeta = `^$.|?*+()[]{}\`

func compilePath(p string) (pathMatcher, error) {
	switch {
	case p == "" || p == "*":
		return pathMatcher{kind: pathAny}, nil
	case len(p) > 2 && p[0] == '/' && p[len(p)-1] == '/' && strings.ContainsAny(p[1:len(p)-1], regexMeta):
		re, err := regexp.Compile(p[1 : len(p)-1])
		if err != nil {
			return pathMatcher{}, invalidf("rule: match.path: bad regexp %s: %v", p, err)
		}
		return pathMatcher{kind: pathRegex, pattern: p, re: re}, nil
	case glob.IsPattern(p):
		return pathMatcher{kind: pathGlob, pattern: p}, nil
	default:
		return pathMatcher{kind: pathPrefix, pattern: p}, nil
	}
}

func (m pathMatcher) match(path string) bool {
	switch m.kind {
	case pathPrefix:
		return strings.HasPrefix(path, m.pattern)
	case pathGlob:
		return glob.Match(m.pattern, path)
	case pathRegex:
		return m.re.MatchString(path)
	case pathAny:
		return true
	}
	return true
}

// headerMatcher matches one request header against a glob. An empty pattern
// only requires the header to be present.
type headerMatcher struct{ name, pattern string }

func (h headerMatcher) match(hdr http.Header) bool {
	vs := hdr[h.name]
	if len(vs) == 0 {
		return false
	}
	if h.pattern == "" {
		return true
	}
	v := vs[0]
	if len(vs) > 1 {
		v = strings.Join(vs, ", ")
	}
	return glob.Match(h.pattern, v)
}

// statusMatcher parses "500", "4xx", "400-499", "!2xx", "200|204". An empty
// spec returns a nil matcher (any status).
func statusMatcher(spec string) (func(int) bool, error) {
	s := strings.ToLower(strings.TrimSpace(spec))
	if s == "" {
		return nil, nil
	}
	neg := strings.HasPrefix(s, "!")
	s = strings.TrimPrefix(s, "!")
	type span struct{ lo, hi int }
	var spans []span
	bad := func() error {
		return invalidf("rule: match.status: bad spec %q (want e.g. 500, 4xx, 400-499, !2xx, 200|204)", spec)
	}
	for part := range strings.SplitSeq(s, "|") {
		part = strings.TrimSpace(part)
		switch {
		case len(part) == 3 && strings.HasSuffix(part, "xx"):
			d := int(part[0] - '0')
			if d < 1 || d > 5 {
				return nil, bad()
			}
			spans = append(spans, span{d * 100, d*100 + 99})
		case strings.Contains(part, "-"):
			lo, hi, _ := strings.Cut(part, "-")
			l, err1 := strconv.Atoi(strings.TrimSpace(lo))
			h, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil || l > h || l < 0 {
				return nil, bad()
			}
			spans = append(spans, span{l, h})
		default:
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				return nil, bad()
			}
			spans = append(spans, span{n, n})
		}
	}
	return func(code int) bool {
		for _, sp := range spans {
			if code >= sp.lo && code <= sp.hi {
				return !neg
			}
		}
		return neg
	}, nil
}

// matches reports whether the rule selects this exchange in phase ph. status
// is the response status (response phase only). It allocates nothing unless a
// host pattern carries a port or a header has several values.
func (cr *compiledRule) matches(ph phaseMask, f *flow.Flow, r *http.Request, status int) bool {
	if cr.host != "" {
		host := f.Host
		if cr.hostHasPort {
			host = host + ":" + strconv.Itoa(f.Port)
		}
		if !glob.Match(cr.host, host) {
			return false
		}
	}
	if cr.path.kind != pathAny && !cr.path.match(requestPath(f, r)) {
		return false
	}
	if len(cr.methods) > 0 {
		method := f.Method
		if r != nil {
			method = r.Method
		}
		found := false
		for _, m := range cr.methods {
			if strings.EqualFold(m, method) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if cr.scheme != "" {
		scheme := f.Scheme
		if r != nil && r.URL != nil && r.URL.Scheme != "" {
			scheme = r.URL.Scheme
		}
		if !strings.EqualFold(cr.scheme, scheme) {
			return false
		}
	}
	if len(cr.headers) > 0 {
		hdr := f.ReqHeaders
		if r != nil {
			hdr = r.Header
		}
		for _, h := range cr.headers {
			if !h.match(hdr) {
				return false
			}
		}
	}
	if cr.status != nil && (ph != phaseResponse || !cr.status(status)) {
		return false
	}
	return true
}

func requestPath(f *flow.Flow, r *http.Request) string {
	if r != nil && r.URL != nil {
		return r.URL.Path
	}
	return f.Path
}
