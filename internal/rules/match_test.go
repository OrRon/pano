package rules

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/orron/pano/internal/api"
)

func TestStatusMatcher(t *testing.T) {
	tests := []struct {
		spec    string
		code    int
		want    bool
		wantErr bool
	}{
		{"", 500, true, false},
		{"500", 500, true, false},
		{"500", 501, false, false},
		{"4xx", 404, true, false},
		{"4xx", 500, false, false},
		{"400-499", 450, true, false},
		{"400-499", 399, false, false},
		{"!2xx", 200, false, false},
		{"!2xx", 503, true, false},
		{"200|204", 204, true, false},
		{"200|204", 201, false, false},
		{" 5XX | 429 ", 429, true, false},
		{"!500|502", 502, false, false},
		{"!500|502", 200, true, false},
		{"abc", 0, false, true},
		{"9xx", 0, false, true},
		{"500-400", 0, false, true},
		{"4x", 0, false, true},
	}
	for _, tc := range tests {
		fn, err := statusMatcher(tc.spec)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: want error", tc.spec)
			} else if !errors.Is(err, ErrInvalid) {
				t.Errorf("%q: error %v does not match ErrInvalid", tc.spec, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.spec, err)
			continue
		}
		got := fn == nil || fn(tc.code)
		if got != tc.want {
			t.Errorf("%q code %d: got %v want %v", tc.spec, tc.code, got, tc.want)
		}
	}
}

func TestCompilePath(t *testing.T) {
	tests := []struct {
		pattern string
		kind    pathKind
		path    string
		want    bool
	}{
		{"", pathAny, "/anything", true},
		{"*", pathAny, "/anything", true},
		{"/v1/", pathPrefix, "/v1/models", true},
		{"/v1/", pathPrefix, "/api/v1/x", false},
		{"/v1", pathPrefix, "/v1beta", true},
		{"/v1/*/messages", pathGlob, "/v1/abc/messages", true},
		{"/v1/*/messages", pathGlob, "/v1/abc/messages/x", false},
		{"/V1/*", pathGlob, "/v1/anything", true},
		{`/^\/v[12]\//`, pathRegex, "/v2/models", true},
		{`/^\/v[12]\//`, pathRegex, "/v3/models", false},
		{"/users/.*/posts/", pathRegex, "/api/users/7/posts", true},
		{"/(foo|bar)/", pathRegex, "/x/bar", true},
		{"/(foo|bar)/", pathRegex, "/x/baz", false},
	}
	for _, tc := range tests {
		m, err := compilePath(tc.pattern)
		if err != nil {
			t.Errorf("%q: %v", tc.pattern, err)
			continue
		}
		if m.kind != tc.kind {
			t.Errorf("%q: kind %d want %d", tc.pattern, m.kind, tc.kind)
		}
		if got := m.match(tc.path); got != tc.want {
			t.Errorf("%q vs %q: got %v want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
	if _, err := compilePath("/(unclosed/"); err == nil || !errors.Is(err, ErrInvalid) {
		t.Errorf("bad regex: got %v", err)
	}
}

func TestHeaderMatch(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Add("Accept", "text/html")
	h.Add("Accept", "application/json")
	tests := []struct {
		name, pattern string
		want          bool
	}{
		{"Content-Type", "application/json*", true},
		{"Content-Type", "APPLICATION/JSON*", true},
		{"Content-Type", "text/*", false},
		{"Content-Type", "", true},
		{"Accept", "*application/json", true},
		{"Accept", "text/html", false},
		{"X-Missing", "*", false},
		{"X-Missing", "", false},
	}
	for _, tc := range tests {
		hm := headerMatcher{name: http.CanonicalHeaderKey(tc.name), pattern: tc.pattern}
		if got := hm.match(h); got != tc.want {
			t.Errorf("%s=%q: got %v want %v", tc.name, tc.pattern, got, tc.want)
		}
	}
}

func TestRuleMatching(t *testing.T) {
	tag := []api.Action{{Type: "tag", Tags: []string{"x"}}}
	tests := []struct {
		name  string
		match api.Match
		url   string
		meth  string
		hdr   map[string]string
		phase phaseMask
		code  int
		want  bool
	}{
		{"host glob", api.Match{Host: "*.openai.com"}, "https://api.openai.com/v1/chat", "POST", nil, phaseRequest, 0, true},
		{"host glob miss", api.Match{Host: "*.openai.com"}, "https://api.anthropic.com/v1", "POST", nil, phaseRequest, 0, false},
		{"host star", api.Match{Host: "*"}, "https://x.test/", "GET", nil, phaseRequest, 0, true},
		{"host with port", api.Match{Host: "localhost:3000"}, "http://localhost:3000/", "GET", nil, phaseRequest, 0, true},
		{"host with wrong port", api.Match{Host: "localhost:3000"}, "http://localhost:4000/", "GET", nil, phaseRequest, 0, false},
		{"method ci", api.Match{Method: []string{"post", "PUT"}}, "https://x.test/", "POST", nil, phaseRequest, 0, true},
		{"method miss", api.Match{Method: []string{"post"}}, "https://x.test/", "GET", nil, phaseRequest, 0, false},
		{"scheme", api.Match{Scheme: "http"}, "http://x.test/", "GET", nil, phaseRequest, 0, true},
		{"scheme miss", api.Match{Scheme: "http"}, "https://x.test/", "GET", nil, phaseRequest, 0, false},
		{"header", api.Match{Header: map[string]string{"authorization": "Bearer *"}}, "https://x.test/", "GET", map[string]string{"Authorization": "Bearer sk-1"}, phaseRequest, 0, true},
		{"header miss", api.Match{Header: map[string]string{"authorization": "Bearer *"}}, "https://x.test/", "GET", nil, phaseRequest, 0, false},
		{"status resp", api.Match{Status: "5xx", Phase: "both"}, "https://x.test/", "GET", nil, phaseResponse, 503, true},
		{"status resp miss", api.Match{Status: "5xx", Phase: "both"}, "https://x.test/", "GET", nil, phaseResponse, 200, false},
		{"status never in request phase", api.Match{Status: "5xx", Phase: "both"}, "https://x.test/", "GET", nil, phaseRequest, 0, false},
		{"path + host", api.Match{Host: "x.test", Path: "/v1/"}, "https://x.test/v1/models", "GET", nil, phaseRequest, 0, true},
	}
	for _, tc := range tests {
		cr, err := compileRule(api.Rule{ID: "r", Match: tc.match, Actions: tag})
		if err != nil {
			t.Errorf("%s: compile: %v", tc.name, err)
			continue
		}
		r := newReq(t, tc.meth, tc.url, "", tc.hdr)
		if got := cr.matches(tc.phase, newFlow(r), r, tc.code); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestPhaseDerivation(t *testing.T) {
	tests := []struct {
		name    string
		rule    api.Rule
		phase   string
		req     int // expected request-phase actions
		resp    int
		wantErr string
	}{
		{"delay defaults to request", api.Rule{Actions: []api.Action{{Type: "delay", MS: 1}}}, "request", 1, 0, ""},
		{"delay with status is response", api.Rule{Match: api.Match{Status: "5xx"}, Actions: []api.Action{{Type: "delay", MS: 1}}}, "response", 0, 1, ""},
		{"set_header on response", api.Rule{Actions: []api.Action{{Type: "set_header", On: "response", Name: "X", Value: "1"}}}, "response", 0, 1, ""},
		{"mixed sides is both", api.Rule{Actions: []api.Action{{Type: "set_header", On: "request", Name: "X", Value: "1"}, {Type: "throttle", KBps: 1}}}, "both", 1, 1, ""},
		{"flexible in both runs twice", api.Rule{Match: api.Match{Phase: "both"}, Actions: []api.Action{{Type: "tag", Tags: []string{"t"}}}}, "both", 1, 1, ""},
		{"throttle is response", api.Rule{Actions: []api.Action{{Type: "throttle", KBps: 8}}}, "response", 0, 1, ""},
		{"mock is request", api.Rule{Actions: []api.Action{{Type: "mock", Body: "x"}}}, "request", 1, 0, ""},
		{"mock on response allowed", api.Rule{Match: api.Match{Status: "5xx"}, Actions: []api.Action{{Type: "mock", On: "response", Status: 200}}}, "response", 0, 1, ""},
		{"hold alias", api.Rule{Actions: []api.Action{{Type: "hold"}}}, "request", 1, 0, ""},
		{"throttle in request phase", api.Rule{Match: api.Match{Phase: "request"}, Actions: []api.Action{{Type: "throttle", KBps: 8}}}, "", 0, 0, "throttle runs on the response side"},
		{"status needs response", api.Rule{Match: api.Match{Status: "5xx"}, Actions: []api.Action{{Type: "mock"}}}, "", 0, 0, "needs phase response"},
		{"redirect on response", api.Rule{Actions: []api.Action{{Type: "redirect", On: "response", Upstream: "http://localhost:1"}}}, "", 0, 0, "cannot run on the response side"},
		{"unknown phase", api.Rule{Match: api.Match{Phase: "sometimes"}, Actions: []api.Action{{Type: "tag", Tags: []string{"t"}}}}, "", 0, 0, "match.phase"},
	}
	for _, tc := range tests {
		tc.rule.ID = "r"
		cr, err := compileRule(tc.rule)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: want error containing %q, got %v", tc.name, tc.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if cr.match.Phase != tc.phase || len(cr.reqActions) != tc.req || len(cr.respActions) != tc.resp {
			t.Errorf("%s: phase %q req %d resp %d; want %q %d %d", tc.name, cr.match.Phase, len(cr.reqActions), len(cr.respActions), tc.phase, tc.req, tc.resp)
		}
	}
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		rule api.Rule
		want string
	}{
		{"unknown type", api.Rule{Actions: []api.Action{{Type: "delay", MS: 1}, {Type: "foo"}}}, `action[1]: unknown type "foo" (want delay|set_header|`},
		{"no actions", api.Rule{}, "at least one action"},
		{"probability", api.Rule{Probability: 1.5, Actions: []api.Action{{Type: "delay", MS: 1}}}, "probability"},
		{"delay zero", api.Rule{Actions: []api.Action{{Type: "delay"}}}, "ms or jitter_ms required"},
		{"set_header name", api.Rule{Actions: []api.Action{{Type: "set_header"}}}, "name required"},
		{"rewrite none", api.Rule{Actions: []api.Action{{Type: "rewrite_body"}}}, "exactly one of json_patch"},
		{"rewrite two", api.Rule{Actions: []api.Action{{Type: "rewrite_body", Regex: "a", Template: "b"}}}, "exactly one of json_patch"},
		{"bad regex", api.Rule{Actions: []api.Action{{Type: "rewrite_body", Regex: "("}}}, "bad regex"},
		{"bad template", api.Rule{Actions: []api.Action{{Type: "rewrite_body", Template: "{{.Body"}}}, "bad template"},
		{"mock status", api.Rule{Actions: []api.Action{{Type: "mock", Status: 42}}}, "status 42 out of range"},
		{"every_n", api.Rule{Actions: []api.Action{{Type: "mock_every_n", Value: "zero"}}}, "positive integer"},
		{"block mode", api.Rule{Actions: []api.Action{{Type: "block", Mode: "explode"}}}, "unknown mode"},
		{"redirect upstream", api.Rule{Actions: []api.Action{{Type: "redirect", Upstream: "localhost:3000"}}}, "upstream must be an http(s) URL"},
		{"throttle kbps", api.Rule{Actions: []api.Action{{Type: "throttle"}}}, "kbps must be >= 1"},
		{"tag empty", api.Rule{Actions: []api.Action{{Type: "tag", Tags: []string{" "}}}}, "tags required"},
		{"bad scheme", api.Rule{Match: api.Match{Scheme: "ftp"}, Actions: []api.Action{{Type: "delay", MS: 1}}}, "match.scheme"},
		{"bad on", api.Rule{Actions: []api.Action{{Type: "delay", MS: 1, On: "sideways"}}}, "on: unknown"},
	}
	for _, tc := range tests {
		tc.rule.ID = "r"
		_, err := compileRule(tc.rule)
		if err == nil {
			t.Errorf("%s: want error", tc.name)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v is not ErrInvalid", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, err.Error(), tc.want)
		}
	}
}
