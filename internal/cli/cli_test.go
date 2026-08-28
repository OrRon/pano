package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func TestParseAction(t *testing.T) {
	cases := map[string]api.Action{
		"delay:1500":                     {Type: "delay", MS: 1500},
		"set_header:X-A=b=c":             {Type: "set_header", Name: "X-A", Value: "b=c"},
		"remove_header:Cookie":           {Type: "remove_header", Name: "Cookie"},
		"mock:503:{\"e\":1}":             {Type: "mock", Status: 503, Body: `{"e":1}`},
		"block:reset":                    {Type: "block", Mode: "reset"},
		"redirect:http://localhost:3000": {Type: "redirect", Upstream: "http://localhost:3000"},
		"throttle:64":                    {Type: "throttle", KBps: 64},
		"hold:response":                  {Type: "breakpoint", On: "response"},
		"tag:slow":                       {Type: "tag", Tags: []string{"slow"}},
	}
	for in, want := range cases {
		got, err := parseAction(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got.Type != want.Type || got.MS != want.MS || got.Name != want.Name || got.Value != want.Value ||
			got.Status != want.Status || got.Body != want.Body || got.Mode != want.Mode || got.Upstream != want.Upstream ||
			got.KBps != want.KBps || got.On != want.On || strings.Join(got.Tags, ",") != strings.Join(want.Tags, ",") {
			t.Fatalf("%s: got %+v want %+v", in, got, want)
		}
	}
	for _, bad := range []string{"delay:x", "mock:abc", "nope:1", "set_header:novalue", "throttle:fast"} {
		if _, err := parseAction(bad); err == nil {
			t.Fatalf("%s: expected error", bad)
		}
	}
}

func TestCoerce(t *testing.T) {
	if coerce("42") != int64(42) || coerce("0.5") != 0.5 || coerce("true") != true || coerce("x") != "x" {
		t.Fatal("coerce")
	}
}

func TestFormatRowAndHelpers(t *testing.T) {
	a := &App{color: false}
	row := api.FlowRow{
		ID: 5, Short: "5", Time: time.Date(2026, 1, 1, 12, 30, 45, 0, time.UTC), Kind: flow.KindHTTP, Method: "POST",
		Host: "api.anthropic.com", Path: "/v1/messages", Status: 200, Duration: "3.21s", Up: 8100, Down: 22400,
		Type: "sse", Flags: []string{"llm", "stream"}, State: flow.StateDone,
	}
	line := a.formatRow(row)
	for _, want := range []string{"5", "POST", "api.anthropic.com", "/v1/messages", "200", "3.21s", "8.1k", "22.4k", "sse", "llm,stream"} {
		if !strings.Contains(line, want) {
			t.Fatalf("row missing %q: %s", want, line)
		}
	}
	row.Kind, row.Error = flow.KindTunnel, "dial: refused"
	line = a.formatRow(row)
	if !strings.Contains(line, "TUN") || !strings.Contains(line, "↳ dial: refused") {
		t.Fatalf("tunnel/error row: %s", line)
	}
	a.color = true
	colored := a.formatRow(row)
	if stripANSI(colored) == colored {
		t.Fatal("expected ANSI codes when color is on")
	}
	if truncate("abcdef", 4) != "abc…" || truncate("ab", 4) != "ab" || pad("a", 3) != "a  " {
		t.Fatal("truncate/pad")
	}
	if a.statusColor(500, "") != red || a.statusColor(404, "") != yellow || a.statusColor(200, "") != green || a.statusColor(200, "boom") != red {
		t.Fatal("statusColor")
	}
}

func TestRootCommandTree(t *testing.T) {
	root := New(Hooks{})
	want := []string{"start", "stop", "status", "on", "off", "ca", "tail", "flows", "show", "explain", "diff", "replay", "rules", "bp", "session", "bypass", "export", "import", "run", "env", "doctor", "config", "mcp", "version"}
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Fatalf("missing command %s", w)
		}
	}
	if have["daemon"] {
		// hidden commands are still registered; ensure it is hidden
		for _, c := range root.Commands() {
			if c.Name() == "daemon" && !c.Hidden {
				t.Fatal("daemon must be hidden")
			}
		}
	}
	if !strings.Contains(mcpJSON("/usr/local/bin/pano"), `"args": [`) {
		t.Fatal("mcpJSON")
	}
}
