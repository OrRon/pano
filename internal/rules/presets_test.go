package rules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orron/pano/internal/api"
)

func TestPresetsExpand(t *testing.T) {
	e := newEngine(t, Options{})
	tests := []struct {
		preset string
		params map[string]any
		check  func(t *testing.T, r api.Rule)
	}{
		{"slow_network", nil, func(t *testing.T, r api.Rule) {
			a := r.Actions[0]
			if r.Match.Host != "*" || a.Type != "delay" || a.MS != 2000 || a.JitterMS != 500 || r.Match.Phase != "request" {
				t.Errorf("%+v", r)
			}
		}},
		{"slow_network", map[string]any{"host": "api.test", "ms": float64(100), "jitter_ms": "5"}, func(t *testing.T, r api.Rule) {
			if r.Match.Host != "api.test" || r.Actions[0].MS != 100 || r.Actions[0].JitterMS != 5 {
				t.Errorf("%+v", r)
			}
		}},
		{"fail_rate", map[string]any{"host": "api.test"}, func(t *testing.T, r api.Rule) {
			a := r.Actions[0]
			if r.Probability != 0.3 || a.Type != "mock" || a.Status != 503 || a.Body != `{"error":"injected failure"}` {
				t.Errorf("%+v", r)
			}
		}},
		{"fail_rate", map[string]any{"host": "api.test", "rate": 0.9, "status": 500.0, "body": "boom"}, func(t *testing.T, r api.Rule) {
			if r.Probability != 0.9 || r.Actions[0].Status != 500 || r.Actions[0].Body != "boom" {
				t.Errorf("%+v", r)
			}
		}},
		{"offline_host", map[string]any{"host": "*.test"}, func(t *testing.T, r api.Rule) {
			if r.Actions[0].Type != "block" || r.Actions[0].Mode != "reset" {
				t.Errorf("%+v", r)
			}
		}},
		{"timeout", map[string]any{"host": "api.test"}, func(t *testing.T, r api.Rule) {
			if r.Actions[0].Type != "block" || r.Actions[0].Mode != "timeout" || r.Actions[0].MS != 30000 {
				t.Errorf("%+v", r)
			}
		}},
		{"timeout", map[string]any{"host": "api.test", "after_ms": 250, "path": "/v1/"}, func(t *testing.T, r api.Rule) {
			if r.Actions[0].MS != 250 || r.Match.Path != "/v1/" {
				t.Errorf("%+v", r)
			}
		}},
		{"rate_limit", map[string]any{"host": "api.test"}, func(t *testing.T, r api.Rule) {
			a := r.Actions[0]
			if a.Type != "mock_every_n" || a.Value != "5" || a.Status != 429 || a.Headers["Retry-After"] != "2" {
				t.Errorf("%+v", r)
			}
		}},
		{"rate_limit", map[string]any{"host": "api.test", "every_n": 2.0, "retry_after": 7}, func(t *testing.T, r api.Rule) {
			if r.Actions[0].Value != "2" || r.Actions[0].Headers["Retry-After"] != "7" {
				t.Errorf("%+v", r)
			}
		}},
		{"hold", map[string]any{"host": "api.test"}, func(t *testing.T, r api.Rule) {
			if r.Actions[0].Type != "breakpoint" || r.Match.Path != "*" || r.Match.Phase != "request" {
				t.Errorf("%+v", r)
			}
		}},
		{"hold", map[string]any{"host": "api.test", "path": "/v1/", "on": "both"}, func(t *testing.T, r api.Rule) {
			if r.Match.Phase != "both" || r.Match.Path != "/v1/" {
				t.Errorf("%+v", r)
			}
		}},
	}
	for _, tc := range tests {
		r, err := e.Add(api.RuleAddRequest{Preset: tc.preset, Params: tc.params})
		if err != nil {
			t.Errorf("%s %v: %v", tc.preset, tc.params, err)
			continue
		}
		if r.ID == "" || r.Name == "" || r.Enabled == nil {
			t.Errorf("%s: incomplete %+v", tc.preset, r)
		}
		tc.check(t, r)
	}
}

func TestPresetErrors(t *testing.T) {
	e := newEngine(t, Options{})
	tests := []struct {
		preset string
		params map[string]any
		want   string
	}{
		{"nope", nil, `unknown preset "nope" (want slow_network|fail_rate|offline_host|timeout|rate_limit|hold)`},
		{"fail_rate", nil, `param "host" is required`},
		{"fail_rate", map[string]any{"host": "x", "rate": "fast"}, `want number`},
		{"slow_network", map[string]any{"ms": 1.5}, `want integer`},
		{"slow_network", map[string]any{"speed": 1}, `unknown param "speed" (want host, path, ms, jitter_ms)`},
		{"hold", map[string]any{"host": "x", "on": "never"}, `on: unknown "never"`},
		{"fail_rate", map[string]any{"host": "x", "rate": 2}, `probability`},
	}
	for _, tc := range tests {
		_, err := e.Add(api.RuleAddRequest{Preset: tc.preset, Params: tc.params})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %v: got %v, want %q", tc.preset, tc.params, err, tc.want)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: not ErrInvalid: %v", tc.preset, err)
		}
	}
	if _, err := e.Add(api.RuleAddRequest{Preset: "hold", Rule: &api.Rule{}}); err == nil {
		t.Error("rule and preset together should fail")
	}
}

func TestPresetsInfoAndRateLimitBehaviour(t *testing.T) {
	e := newEngine(t, Options{})
	infos := e.Presets()
	if len(infos) != 6 || infos[0].Name != "slow_network" || infos[4].Params[2].Default != 5 {
		t.Errorf("%+v", infos)
	}
	for _, info := range infos {
		if info.Description == "" || len(info.Params) == 0 {
			t.Errorf("%s: missing docs", info.Name)
		}
	}
	if _, err := e.Add(api.RuleAddRequest{Preset: "rate_limit", Params: map[string]any{"host": "api.test", "every_n": 2}, Name: "rl", TTLS: 60}); err != nil {
		t.Fatal(err)
	}
	r := newReq(t, "GET", "https://api.test/", "", nil)
	mocked := 0
	for range 6 {
		if d := e.Request(context.Background(), newFlow(r), r); d.Mock != nil {
			mocked++
			if d.Mock.Header.Get("Retry-After") != "2" || d.Mock.StatusCode != 429 {
				t.Errorf("mock %+v", d.Mock.Header)
			}
		}
	}
	if mocked != 3 {
		t.Errorf("mocked %d of 6", mocked)
	}
	if got := e.List()[0]; got.Name != "rl" || got.TTLSeconds != 60 {
		t.Errorf("overrides %+v", got)
	}
}
