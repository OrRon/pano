package explain

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite golden digest files under testdata")

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// sameJSON compares two JSON documents structurally.
func sameJSON(t *testing.T, what string, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: got is not JSON: %v\n%s", what, err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: want is not JSON: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		gp, _ := json.MarshalIndent(g, "", "  ")
		wp, _ := json.MarshalIndent(w, "", "  ")
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", what, gp, wp)
	}
}

// cutBefore returns b up to the first occurrence of marker.
func cutBefore(t *testing.T, b []byte, marker string) []byte {
	t.Helper()
	i := bytes.Index(b, []byte(marker))
	if i < 0 {
		t.Fatalf("marker %q not found", marker)
	}
	return b[:i]
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.txt")
	if *update {
		if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	if strings.TrimSuffix(string(want), "\n") != got {
		t.Errorf("digest differs from %s (run with -update to accept)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

type fixture struct {
	name     string
	provider string
	host     string
	path     string
	status   int
	reqFile  string
	respFile string
	respMIME string
	model    string
	stream   bool
	partial  bool
	stop     string
	usage    map[string]any // normalised usage without "raw"; nil means none expected
	errors   int
	final    string // expected final JSON fixture; "" means Final must be nil
}

var fixtures = []fixture{
	{
		name: "anthropic_stream", provider: Anthropic, host: "api.anthropic.com", path: "/v1/messages", status: 200,
		reqFile: "anthropic_request.json", respFile: "anthropic_stream.sse", respMIME: "text/event-stream; charset=utf-8",
		model: "claude-opus-5", stream: true, stop: "tool_use",
		usage: map[string]any{"input_tokens": 4812, "output_tokens": 356, "cache_read_input_tokens": 4100, "cache_creation_input_tokens": 0, "total_tokens": 5168},
		final: "anthropic_stream.final.json",
	},
	{
		name: "anthropic_truncated", provider: Anthropic, host: "api.anthropic.com", path: "/v1/messages", status: 200,
		reqFile: "anthropic_request.json", respFile: "anthropic_truncated.sse", respMIME: "text/event-stream",
		model: "claude-opus-5", stream: true, partial: true, stop: "",
		usage:  map[string]any{"input_tokens": 4812, "output_tokens": 3, "cache_read_input_tokens": 4100, "cache_creation_input_tokens": 0, "total_tokens": 4815},
		errors: 1, final: "anthropic_truncated.final.json",
	},
	{
		name: "anthropic_nonstream", provider: Anthropic, host: "api.anthropic.com", path: "/v1/messages", status: 200,
		reqFile: "anthropic_request_nonstream.json", respFile: "anthropic_nonstream.json", respMIME: "application/json",
		model: "claude-opus-5", stop: "end_turn",
		usage: map[string]any{"input_tokens": 2420, "output_tokens": 42, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 900, "total_tokens": 2462},
		final: "anthropic_nonstream.json",
	},
	{
		name: "openai_chat_stream", provider: OpenAIChat, host: "api.openai.com", path: "/v1/chat/completions", status: 200,
		reqFile: "openai_chat_request.json", respFile: "openai_chat_stream.sse", respMIME: "text/event-stream; charset=utf-8",
		model: "gpt-4.1-2025-04-14", stream: true, stop: "tool_calls",
		usage: map[string]any{"input_tokens": 88, "output_tokens": 41, "total_tokens": 129, "cache_read_input_tokens": 0, "reasoning_tokens": 0},
		final: "openai_chat_stream.final.json",
	},
	{
		name: "openai_chat_nonstream", provider: OpenAIChat, host: "api.openai.com", path: "/v1/chat/completions", status: 200,
		reqFile: "openai_chat_request_nonstream.json", respFile: "openai_chat_nonstream.json", respMIME: "application/json",
		model: "gpt-4.1-2025-04-14", stop: "stop",
		usage: map[string]any{"input_tokens": 210, "output_tokens": 24, "total_tokens": 234, "cache_read_input_tokens": 128, "reasoning_tokens": 0},
		final: "openai_chat_nonstream.json",
	},
	{
		name: "openai_responses_stream", provider: OpenAIResponses, host: "api.openai.com", path: "/v1/responses", status: 200,
		reqFile: "openai_responses_request.json", respFile: "openai_responses_stream.sse", respMIME: "text/event-stream; charset=utf-8",
		model: "gpt-4.1-2025-04-14", stream: true, stop: "completed",
		usage: map[string]any{"input_tokens": 120, "output_tokens": 60, "total_tokens": 180, "cache_read_input_tokens": 0, "reasoning_tokens": 20},
		final: "openai_responses_stream.final.json",
	},
	{
		name: "gemini_stream", provider: Gemini, host: "generativelanguage.googleapis.com", path: "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", status: 200,
		reqFile: "gemini_request.json", respFile: "gemini_stream.sse", respMIME: "text/event-stream",
		model: "gemini-2.5-flash", stream: true, stop: "STOP",
		usage: map[string]any{"input_tokens": 12, "output_tokens": 20, "total_tokens": 32},
		final: "gemini_stream.final.json",
	},
	{
		name: "error_429", provider: Anthropic, host: "api.anthropic.com", path: "/v1/messages", status: 429,
		reqFile: "anthropic_request.json", respFile: "error_429.json", respMIME: "application/json",
		model: "claude-opus-5", errors: 1,
	},
}

func TestExplainFixtures(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			req := readFixture(t, f.reqFile)
			resp := readFixture(t, f.respFile)
			respHeaders := http.Header{"Content-Type": {f.respMIME}}
			r, err := Explain(f.host, f.path, f.status, nil, req, respHeaders, resp, Options{})
			if err != nil {
				t.Fatalf("Explain: %v", err)
			}
			if r.Provider != f.provider {
				t.Errorf("provider = %q, want %q", r.Provider, f.provider)
			}
			if r.Model != f.model {
				t.Errorf("model = %q, want %q", r.Model, f.model)
			}
			if r.Stream != f.stream {
				t.Errorf("stream = %v, want %v", r.Stream, f.stream)
			}
			if r.Partial != f.partial {
				t.Errorf("partial = %v, want %v", r.Partial, f.partial)
			}
			if r.Status != f.status {
				t.Errorf("status = %d, want %d", r.Status, f.status)
			}
			if r.StopReason != f.stop {
				t.Errorf("stop = %q, want %q", r.StopReason, f.stop)
			}
			if len(r.Errors) != f.errors {
				t.Errorf("errors = %q, want %d", r.Errors, f.errors)
			}
			checkUsage(t, r.Usage, f.usage)
			if f.final == "" {
				if r.Final != nil {
					t.Errorf("Final = %s, want nil", r.Final)
				}
			} else {
				want := readFixture(t, f.final)
				sameJSON(t, "Final", r.Final, want)
				if f.stream {
					got, partial, err := Reassemble(f.provider, resp)
					if err != nil {
						t.Fatalf("Reassemble: %v", err)
					}
					if partial != f.partial {
						t.Errorf("Reassemble partial = %v, want %v", partial, f.partial)
					}
					sameJSON(t, "Reassemble", got, want)
				}
			}
			checkDigest(t, r.Text, DefaultMaxChars)
			checkGolden(t, f.name, r.Text)
		})
	}
}

func checkUsage(t *testing.T, got, want map[string]any) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("usage = %v, want nil", got)
		}
		return
	}
	if got == nil {
		t.Fatalf("usage = nil, want %v", want)
	}
	if got["raw"] == nil {
		t.Errorf("usage.raw missing")
	}
	stripped := map[string]any{}
	for k, v := range got {
		if k != "raw" {
			stripped[k] = v
		}
	}
	if !reflect.DeepEqual(stripped, want) {
		t.Errorf("usage = %v, want %v", stripped, want)
	}
}

// checkDigest enforces the format invariants: within budget, no trailing
// whitespace, no trailing newline.
func checkDigest(t *testing.T, text string, maxChars int) {
	t.Helper()
	if n := utf8.RuneCountInString(text); n > maxChars {
		t.Errorf("digest is %d chars, budget %d", n, maxChars)
	}
	if strings.HasSuffix(text, "\n") {
		t.Errorf("digest ends with newline")
	}
	for i, l := range strings.Split(text, "\n") {
		if strings.TrimRight(l, " \t") != l {
			t.Errorf("line %d has trailing whitespace: %q", i+1, l)
		}
	}
}

func TestExplainIncludes(t *testing.T) {
	req := readFixture(t, "anthropic_request.json")
	resp := readFixture(t, "anthropic_stream.sse")
	h := http.Header{"Content-Type": {"text/event-stream"}}
	explain := func(t *testing.T, opts Options) *Result {
		t.Helper()
		r, err := Explain("api.anthropic.com", "/v1/messages", 200, nil, req, h, resp, opts)
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		return r
	}

	t.Run("default hides thinking", func(t *testing.T) {
		r := explain(t, Options{})
		if !strings.Contains(r.Text, "[thinking] (154 chars, hidden — include=thinking to show)") {
			t.Errorf("thinking placeholder missing:\n%s", r.Text)
		}
		if strings.Contains(r.Text, "Euclidean") || strings.Contains(r.Text, "I should read") {
			t.Errorf("thinking text leaked:\n%s", r.Text)
		}
	})
	t.Run("thinking", func(t *testing.T) {
		r := explain(t, Options{Include: []string{"final,thinking"}})
		if !strings.Contains(r.Text, `[thinking] "The user wants me to look at the store package.`) {
			t.Errorf("thinking text missing:\n%s", r.Text)
		}
		if strings.Contains(r.Text, "usage:") || strings.Contains(r.Text, "errors:") {
			t.Errorf("sections not requested were rendered:\n%s", r.Text)
		}
	})
	t.Run("messages system request", func(t *testing.T) {
		r := explain(t, Options{Include: []string{"messages", "system", "request"}})
		for _, want := range []string{
			"messages:\n  user: Please read main.go and tell me what it does.",
			"assistant: I'll read the file first. [tool_use Read]",
			"user: [tool_result package main",
			"system:\n  You are a coding assistant working in a Go repository.",
			"request_json:\n  {\n    \"max_tokens\": 8192,",
		} {
			if !strings.Contains(r.Text, want) {
				t.Errorf("missing %q in:\n%s", want, r.Text)
			}
		}
		if strings.Contains(r.Text, "final:") {
			t.Errorf("final rendered without being included:\n%s", r.Text)
		}
		checkDigest(t, r.Text, DefaultMaxChars)
	})
	t.Run("no tools hides inputs", func(t *testing.T) {
		r := explain(t, Options{Include: []string{"final"}})
		if !strings.Contains(r.Text, "[tool_use] Read (input hidden — include=tools to show)") {
			t.Errorf("tool input not hidden:\n%s", r.Text)
		}
		if !strings.Contains(r.Text, " · 6 tools · ") {
			t.Errorf("tool names not hidden in request line:\n%s", r.Text)
		}
	})
	t.Run("max chars", func(t *testing.T) {
		r := explain(t, Options{MaxChars: 120})
		checkDigest(t, r.Text, 120)
		if !strings.HasSuffix(r.Text, truncMarker) {
			t.Errorf("truncated digest should end with marker: %q", r.Text)
		}
	})
	t.Run("unknown include", func(t *testing.T) {
		if _, err := Explain("api.anthropic.com", "/v1/messages", 200, nil, req, h, resp, Options{Include: []string{"bogus"}}); err == nil {
			t.Errorf("expected error for unknown include")
		}
	})
	t.Run("unknown provider", func(t *testing.T) {
		if _, err := Explain("api.anthropic.com", "/v1/messages", 200, nil, req, h, resp, Options{Provider: "cohere"}); err == nil {
			t.Errorf("expected error for unknown provider")
		}
	})
	t.Run("forced provider", func(t *testing.T) {
		r, err := Explain("gateway.internal", "/proxy/llm", 200, nil, req, h, resp, Options{Provider: Anthropic})
		if err != nil {
			t.Fatalf("Explain: %v", err)
		}
		if r.Provider != Anthropic || r.StopReason != "tool_use" {
			t.Errorf("forced provider not honoured: %+v", r)
		}
	})
	t.Run("not llm", func(t *testing.T) {
		_, err := Explain("example.com", "/index.html", 200, nil, nil, http.Header{"Content-Type": {"text/html"}}, []byte("<html>"), Options{})
		if !errors.Is(err, ErrNotLLM) {
			t.Errorf("err = %v, want ErrNotLLM", err)
		}
	})
}

func TestExplainEdgeCases(t *testing.T) {
	t.Run("empty stream body", func(t *testing.T) {
		req := readFixture(t, "anthropic_request.json")
		r, err := Explain("api.anthropic.com", "/v1/messages", 200, nil, req, nil, nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Stream || !r.Partial || r.Final != nil {
			t.Errorf("empty streamed body: %+v", r)
		}
		if !strings.Contains(r.Text, "stream: yes (INCOMPLETE — 0 events)") {
			t.Errorf("header: %s", r.Text)
		}
	})
	t.Run("empty nonstream body", func(t *testing.T) {
		req := readFixture(t, "anthropic_request_nonstream.json")
		r, err := Explain("api.anthropic.com", "/v1/messages", 200, nil, req, nil, nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Stream || len(r.Errors) != 1 {
			t.Errorf("empty body: %+v", r)
		}
	})
	t.Run("error status with sse body", func(t *testing.T) {
		body := []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
		r, err := Explain("api.anthropic.com", "/v1/messages", 529, nil, readFixture(t, "anthropic_request.json"), http.Header{"Content-Type": {"text/event-stream"}}, body, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Errors) != 1 || r.Errors[0] != "HTTP 529: overloaded_error: Overloaded" {
			t.Errorf("errors = %q", r.Errors)
		}
		if !strings.Contains(r.Text, "errors: HTTP 529: overloaded_error: Overloaded") {
			t.Errorf("digest: %s", r.Text)
		}
	})
	t.Run("error status non-json body", func(t *testing.T) {
		r, err := Explain("api.openai.com", "/v1/chat/completions", 502, nil, readFixture(t, "openai_chat_request.json"), http.Header{"Content-Type": {"text/html"}}, []byte("<html>bad gateway</html>"), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Errors) != 1 || !strings.HasPrefix(r.Errors[0], "HTTP 502: <html>") {
			t.Errorf("errors = %q", r.Errors)
		}
	})
	t.Run("openai error in stream", func(t *testing.T) {
		body := []byte("data: {\"error\":{\"message\":\"The server had an error\",\"type\":\"server_error\",\"code\":null}}\n\n")
		r, err := Explain("api.openai.com", "/v1/chat/completions", 200, nil, readFixture(t, "openai_chat_request.json"), http.Header{"Content-Type": {"text/event-stream"}}, body, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Partial || len(r.Errors) < 1 || r.Errors[0] != "server_error: The server had an error" {
			t.Errorf("partial=%v errors=%q", r.Partial, r.Errors)
		}
	})
	t.Run("openai chat n>1", func(t *testing.T) {
		req := []byte(`{"model":"gpt-4.1","n":2,"messages":[{"role":"user","content":"Say hi"}]}`)
		resp := []byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":"Hi!"},"finish_reason":"stop"},{"index":1,"message":{"role":"assistant","content":"Hello there."},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`)
		r, err := Explain("api.openai.com", "/v1/chat/completions", 200, nil, req, http.Header{"Content-Type": {"application/json"}}, resp, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"  choice 0 (finish: stop):\n    [text] \"Hi!\" (3 chars)",
			"  choice 1 (finish: length):\n    [text] \"Hello there.\" (12 chars)",
			"stop: stop,length",
			"n 2",
		} {
			if !strings.Contains(r.Text, want) {
				t.Errorf("missing %q in:\n%s", want, r.Text)
			}
		}
	})
	t.Run("responses stream cut before completed", func(t *testing.T) {
		full := readFixture(t, "openai_responses_stream.sse")
		cut := cutBefore(t, full, "event: response.completed")
		// Also drop the message item's output_item.done so deltas must be applied.
		cut = cut[:bytes.LastIndex(cut, []byte("event: response.output_item.done"))]
		r, err := Explain("api.openai.com", "/v1/responses", 200, nil, readFixture(t, "openai_responses_request.json"), http.Header{"Content-Type": {"text/event-stream"}}, cut, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Partial || r.StopReason != "in_progress" {
			t.Errorf("partial=%v stop=%q", r.Partial, r.StopReason)
		}
		var final map[string]any
		if err := json.Unmarshal(r.Final, &final); err != nil {
			t.Fatal(err)
		}
		output, _ := final["output"].([]any)
		if len(output) != 3 {
			t.Fatalf("output = %d items, want 3: %s", len(output), r.Final)
		}
		msg := output[2].(map[string]any)
		content := msg["content"].([]any)[0].(map[string]any)
		if content["text"] != "Fetching the Paris forecast now." {
			t.Errorf("assembled text = %q", content["text"])
		}
		fc := output[1].(map[string]any)
		if fc["arguments"] != `{"location":"Paris, France"}` {
			t.Errorf("arguments = %q", fc["arguments"])
		}
		for _, want := range []string{
			"stream: yes (INCOMPLETE — 22 events, last: response.content_part.done)",
			"usage: - · stop: in_progress",
		} {
			if !strings.Contains(r.Text, want) {
				t.Errorf("missing %q in:\n%s", want, r.Text)
			}
		}
		if r.Usage != nil {
			t.Errorf("usage should be absent before completion: %v", r.Usage)
		}
	})
	t.Run("responses incomplete", func(t *testing.T) {
		body := []byte(`event: response.incomplete
data: {"type":"response.incomplete","sequence_number":5,"response":{"id":"resp_1","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"model":"gpt-4.1","output":[{"id":"msg_1","type":"message","status":"incomplete","role":"assistant","content":[{"type":"output_text","text":"Once upon a","annotations":[]}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":15}}}

`)
		r, err := Explain("api.openai.com", "/v1/responses", 200, nil, nil, http.Header{"Content-Type": {"text/event-stream"}}, body, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Partial || r.StopReason != "incomplete (max_output_tokens)" || len(r.Errors) != 1 {
			t.Errorf("partial=%v stop=%q errors=%q", r.Partial, r.StopReason, r.Errors)
		}
	})
	t.Run("gemini json array stream", func(t *testing.T) {
		body := []byte(`[{"candidates":[{"content":{"parts":[{"text":"Hel"}],"role":"model"}}]},{"candidates":[{"content":{"parts":[{"text":"lo"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}]`)
		r, err := Explain("generativelanguage.googleapis.com", "/v1beta/models/gemini-2.5-pro:streamGenerateContent", 200, nil, readFixture(t, "gemini_request.json"), http.Header{"Content-Type": {"application/json"}}, body, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Stream || r.Partial || r.StopReason != "STOP" || r.Model != "gemini-2.5-pro" {
			t.Errorf("stream=%v partial=%v stop=%q model=%q", r.Stream, r.Partial, r.StopReason, r.Model)
		}
		if !strings.Contains(r.Text, `[text] "Hello" (5 chars)`) {
			t.Errorf("digest: %s", r.Text)
		}
	})
	t.Run("gemini thought and function call", func(t *testing.T) {
		body := []byte(`{"candidates":[{"content":{"parts":[{"text":"Let me think.","thought":true},{"text":"Sure."},{"functionCall":{"name":"lookup_word","args":{"word":"proxy"}}}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":12,"totalTokenCount":50,"thoughtsTokenCount":8,"cachedContentTokenCount":10},"modelVersion":"gemini-2.5-flash"}`)
		r, err := Explain("generativelanguage.googleapis.com", "/v1beta/models/gemini-2.5-flash:generateContent", 200, nil, readFixture(t, "gemini_request.json"), http.Header{"Content-Type": {"application/json"}}, body, Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkUsage(t, r.Usage, map[string]any{"input_tokens": 30, "output_tokens": 12, "total_tokens": 50, "reasoning_tokens": 8, "cache_read_input_tokens": 10})
		for _, want := range []string{
			"[thinking] (13 chars, hidden — include=thinking to show)",
			`[function_call] lookup_word {"word":"proxy"}`,
			"usage: in 30 (cache_read 10) · out 12 (reasoning 8) · total 50 · stop: STOP",
		} {
			if !strings.Contains(r.Text, want) {
				t.Errorf("missing %q in:\n%s", want, r.Text)
			}
		}
	})
	t.Run("anthropic stream without message_start", func(t *testing.T) {
		body := []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
		r, err := Explain("api.anthropic.com", "/v1/messages", 200, nil, readFixture(t, "anthropic_request.json"), http.Header{"Content-Type": {"text/event-stream"}}, body, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Partial || r.Final != nil || len(r.Errors) != 2 {
			t.Errorf("partial=%v final=%s errors=%q", r.Partial, r.Final, r.Errors)
		}
	})
}

func TestDetect(t *testing.T) {
	cases := []struct {
		host, path, body, mime string
		want                   string
		ok                     bool
	}{
		{"api.anthropic.com", "/v1/messages", "", "", Anthropic, true},
		{"api.anthropic.com", "/v1/messages?beta=true", "", "text/event-stream", Anthropic, true},
		{"api.anthropic.com", "/v1/messages/count_tokens", `{"model":"x","messages":[]}`, "application/json", "", false},
		{"api.anthropic.com", "/v1/messages/batches", "", "application/json", "", false},
		{"gateway.example.com:8443", "/anthropic/v1/messages", "", "", Anthropic, true},
		{"api.openai.com", "/v1/chat/completions", "", "", OpenAIChat, true},
		{"myorg.openai.azure.com", "/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21", "", "", OpenAIChat, true},
		{"openrouter.ai", "/api/v1/chat/completions", "", "", OpenAIChat, true},
		{"api.openai.com", "/v1/responses", "", "", OpenAIResponses, true},
		{"llm.internal", "/v1/responses", `{"model":"gpt-4.1","input":"hi"}`, "application/json", OpenAIResponses, true},
		{"llm.internal", "/v1/responses", "", "application/json", "", false},
		{"generativelanguage.googleapis.com", "/v1beta/models/gemini-2.5-flash:generateContent", "", "", Gemini, true},
		{"us-central1-aiplatform.googleapis.com", "/v1/projects/p/locations/l/publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse", "", "", Gemini, true},
		{"bedrock-runtime.us-east-1.amazonaws.com", "/model/anthropic.claude-3/invoke", `{"anthropic_version":"bedrock-2023-05-31","max_tokens":100,"messages":[]}`, "application/json", Anthropic, true},
		{"llm.internal", "/complete", `{"model":"claude-opus-5","max_tokens":100,"system":"x","messages":[]}`, "application/json", Anthropic, true},
		{"llm.internal", "/complete", `{"model":"gpt-4o","max_tokens":100,"messages":[]}`, "application/json", OpenAIChat, true},
		{"llm.internal", "/complete", `{"contents":[]}`, "application/json", Gemini, true},
		{"llm.internal", "/complete", `{"model":"gpt-4o","max_tokens":100,"messages":[]}`, "text/html", "", false},
		{"example.com", "/api/messages", `{"messages":["hello"]}`, "application/json", "", false},
		{"example.com", "/index.html", "", "text/html", "", false},
		{"example.com", "/", "not json", "", "", false},
	}
	for _, c := range cases {
		got, ok := Detect(c.host, c.path, []byte(c.body), c.mime)
		if got != c.want || ok != c.ok {
			t.Errorf("Detect(%q, %q, %q, %q) = %q,%v want %q,%v", c.host, c.path, c.body, c.mime, got, ok, c.want, c.ok)
		}
	}
}

func TestReassembleUnknownProvider(t *testing.T) {
	if _, _, err := Reassemble("nope", []byte("data: {}\n\n")); err == nil {
		t.Error("expected error")
	}
	for _, p := range Providers {
		if _, _, err := Reassemble(p, nil); err == nil {
			t.Errorf("%s: expected error for empty body", p)
		}
	}
}

func TestHelpers(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 4812: "4,812", 1234567: "1,234,567", -5000: "-5,000"} {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{12: "12", 1200: "1.2k", 1000: "1k", 15000: "15k", 2_500_000: "2.5M"} {
		if got := humanChars(n); got != want {
			t.Errorf("humanChars(%d) = %q, want %q", n, got, want)
		}
	}
	if got := truncRunes("héllo wörld", 5); got != "héll…" {
		t.Errorf("truncRunes = %q", got)
	}
	if got, n := quoteTrunc("a\"b\nc", 10); got != `"a\"b\nc"` || n != 5 {
		t.Errorf("quoteTrunc = %q, %d", got, n)
	}
}
