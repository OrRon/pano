package rules

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/orron/pano/internal/api"
)

// PresetInfo describes a rule preset.
type PresetInfo struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Params      []PresetParam `json:"params"`
}

// PresetParam describes one preset parameter.
type PresetParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // string|number
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description"`
}

type preset struct {
	info   PresetInfo
	expand func(p *params) (api.Rule, error)
}

var (
	hostParam = PresetParam{Name: "host", Type: "string", Required: true, Description: "host glob to match"}
	pathParam = PresetParam{Name: "path", Type: "string", Description: "optional path prefix, glob or /regex/"}
)

var presets = []preset{
	{
		info: PresetInfo{Name: "slow_network", Description: "add latency to every matching request", Params: []PresetParam{
			{Name: "host", Type: "string", Default: "*", Description: "host glob to match"},
			pathParam,
			{Name: "ms", Type: "number", Default: 2000, Description: "base delay in milliseconds"},
			{Name: "jitter_ms", Type: "number", Default: 500, Description: "random extra delay, 0..jitter_ms"},
		}},
		expand: func(p *params) (api.Rule, error) {
			host := p.str("host", "*")
			return api.Rule{
				Name:    "slow network " + host,
				Match:   api.Match{Host: host, Path: p.str("path", "")},
				Actions: []api.Action{{Type: ActionDelay, MS: p.int("ms", 2000), JitterMS: p.int("jitter_ms", 500)}},
			}, p.err
		},
	},
	{
		info: PresetInfo{Name: "fail_rate", Description: "answer a fraction of requests with an injected error", Params: []PresetParam{
			hostParam, pathParam,
			{Name: "rate", Type: "number", Default: 0.3, Description: "probability of failing, 0..1"},
			{Name: "status", Type: "number", Default: 503, Description: "error status code"},
			{Name: "body", Type: "string", Default: `{"error":"injected failure"}`, Description: "error body"},
		}},
		expand: func(p *params) (api.Rule, error) {
			host := p.str("host", "")
			return api.Rule{
				Name:        "fail rate " + host,
				Match:       api.Match{Host: host, Path: p.str("path", "")},
				Probability: p.float("rate", 0.3),
				Actions:     []api.Action{{Type: ActionMock, Status: p.int("status", 503), Body: p.str("body", `{"error":"injected failure"}`)}},
			}, p.err
		},
	},
	{
		info: PresetInfo{Name: "offline_host", Description: "reset every connection to a host", Params: []PresetParam{hostParam, pathParam}},
		expand: func(p *params) (api.Rule, error) {
			host := p.str("host", "")
			return api.Rule{
				Name:    "offline " + host,
				Match:   api.Match{Host: host, Path: p.str("path", "")},
				Actions: []api.Action{{Type: ActionBlock, Mode: "reset"}},
			}, p.err
		},
	},
	{
		info: PresetInfo{Name: "timeout", Description: "hang matching requests until the client gives up", Params: []PresetParam{
			hostParam, pathParam,
			{Name: "after_ms", Type: "number", Default: 30000, Description: "how long to hang before dropping"},
		}},
		expand: func(p *params) (api.Rule, error) {
			host := p.str("host", "")
			return api.Rule{
				Name:    "timeout " + host,
				Match:   api.Match{Host: host, Path: p.str("path", "")},
				Actions: []api.Action{{Type: ActionBlock, Mode: "timeout", MS: p.int("after_ms", 30000)}},
			}, p.err
		},
	},
	{
		info: PresetInfo{Name: "rate_limit", Description: "answer every nth request with 429", Params: []PresetParam{
			hostParam, pathParam,
			{Name: "every_n", Type: "number", Default: 5, Description: "fail every nth request"},
			{Name: "status", Type: "number", Default: 429, Description: "status code"},
			{Name: "retry_after", Type: "number", Default: 2, Description: "Retry-After header, seconds"},
		}},
		expand: func(p *params) (api.Rule, error) {
			host := p.str("host", "")
			return api.Rule{
				Name:  "rate limit " + host,
				Match: api.Match{Host: host, Path: p.str("path", "")},
				Actions: []api.Action{{
					Type: ActionMockEveryN, Value: strconv.Itoa(p.int("every_n", 5)), Status: p.int("status", 429),
					Headers: map[string]string{"Retry-After": strconv.Itoa(p.int("retry_after", 2))},
					Body:    `{"error":"rate limited"}`,
				}},
			}, p.err
		},
	},
	{
		info: PresetInfo{Name: "hold", Description: "park matching exchanges on a breakpoint", Params: []PresetParam{
			hostParam,
			{Name: "path", Type: "string", Default: "*", Description: "path prefix, glob or /regex/"},
			{Name: "on", Type: "string", Default: "request", Description: "request|response|both"},
		}},
		expand: func(p *params) (api.Rule, error) {
			host := p.str("host", "")
			on := strings.ToLower(p.str("on", "request"))
			if on != "request" && on != "response" && on != "both" {
				return api.Rule{}, invalidf("preset hold: on: unknown %q (want request|response|both)", on)
			}
			return api.Rule{
				Name:    "hold " + host,
				Match:   api.Match{Host: host, Path: p.str("path", "*"), Phase: on},
				Actions: []api.Action{{Type: ActionBreakpoint}},
			}, p.err
		},
	},
}

// Presets lists the available presets with their parameters and defaults.
func (e *Engine) Presets() []PresetInfo {
	out := make([]PresetInfo, 0, len(presets))
	for _, p := range presets {
		info := p.info
		info.Params = append([]PresetParam(nil), p.info.Params...)
		out = append(out, info)
	}
	return out
}

func presetNames() []string {
	names := make([]string, 0, len(presets))
	for _, p := range presets {
		names = append(names, p.info.Name)
	}
	return names
}

// expandPreset builds the rule for a preset. Unknown presets and parameters
// are errors; parameter values are coerced from their JSON forms.
func expandPreset(name string, raw map[string]any) (api.Rule, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range presets {
		if p.info.Name != name {
			continue
		}
		known := make(map[string]bool, len(p.info.Params))
		for _, param := range p.info.Params {
			known[param.Name] = true
		}
		for k := range raw {
			if !known[k] {
				return api.Rule{}, invalidf("preset %s: unknown param %q (want %s)", name, k, strings.Join(paramNames(p.info), ", "))
			}
		}
		for _, param := range p.info.Params {
			if v, ok := raw[param.Name]; param.Required && (!ok || v == "" || v == nil) {
				return api.Rule{}, invalidf("preset %s: param %q is required", name, param.Name)
			}
		}
		ps := &params{preset: name, m: raw}
		return p.expand(ps)
	}
	return api.Rule{}, invalidf("unknown preset %q (want %s)", name, strings.Join(presetNames(), "|"))
}

func paramNames(info PresetInfo) []string {
	names := make([]string, 0, len(info.Params))
	for _, p := range info.Params {
		names = append(names, p.Name)
	}
	return names
}

// params reads preset parameters with coercion, collecting the first error.
type params struct {
	preset string
	m      map[string]any
	err    error
}

func (p *params) fail(key string, v any, want string) {
	if p.err == nil {
		p.err = invalidf("preset %s: param %q: want %s, got %v (%T)", p.preset, key, want, v, v)
	}
}

func (p *params) str(key, def string) string {
	v, ok := p.m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return def
		}
		return t
	case float64, int, int64, bool, json.Number:
		return fmt.Sprint(t)
	}
	p.fail(key, v, "string")
	return def
}

func (p *params) float(key string, def float64) float64 {
	v, ok := p.m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	p.fail(key, v, "number")
	return def
}

func (p *params) int(key string, def int) int {
	f := p.float(key, float64(def))
	if f != math.Trunc(f) || f > math.MaxInt32 || f < math.MinInt32 {
		p.fail(key, p.m[key], "integer")
		return def
	}
	return int(f)
}
