package view

import (
	"bytes"
	"compress/gzip"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustRender(t *testing.T, mode, body, mime, path string, opts Options) string {
	t.Helper()
	out, _, _, err := Render(mode, []byte(body), "", mime, path, opts)
	if err != nil {
		t.Fatalf("Render(%s): %v", mode, err)
	}
	return out
}

func wantContains(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
}

func TestRenderModesJSON(t *testing.T) {
	opts := DefaultOptions()
	opts.StringTruncate = 80
	tests := []struct {
		name, body, mode string
		want             []string
	}{
		{"anthropic summary", anthropicBody, ViewSummary, []string{
			"body: application/json 538B sha256:",
			"json object (8 keys)",
			`model: str "claude-opus-5"`,
			"content: array[1] of object{type, text}",
			`stop_reason: str "end_turn"`,
			"stop_sequence: null",
			`usage: object[4] = {"input_tokens":25`,
		}},
		{"anthropic schema", anthropicBody, ViewSchema, []string{
			"content: [ { type: str, text: str } ]",
			"stop_sequence: null",
			"input_tokens: int",
		}},
		{"anthropic truncated", anthropicBody, ViewTruncated, []string{`"id": "msg_01XFDUDYJgAACzvnptvVoYEL"`}},
		{"anthropic pretty", anthropicBody, ViewPretty, []string{"{\n  \"id\": \"msg_01XFDUDYJgAACzvnptvVoYEL\",\n"}},
		{"anthropic raw", anthropicBody, ViewRaw, []string{"\n" + anthropicBody}},

		{"openai summary", openaiBody, ViewSummary, []string{
			"json object (7 keys)",
			"created: int 1719000000",
			"choices: array[1] of object{index, message, logprobs, finish_reason}",
			`usage: object[3] = {"prompt_tokens":14,"completion_tokens":21,"total_tokens":35}`,
			"notable:",
			`choices.0.finish_reason: "stop"`,
			`choices.0.message: {"role":"assistant"`,
		}},
		{"openai schema", openaiBody, ViewSchema, []string{
			"message: { role: str, content: str }",
			"finish_reason: str",
			"logprobs: null",
			"total_tokens: int",
		}},
		{"openai pretty", openaiBody, ViewPretty, []string{"\"finish_reason\": \"stop\""}},
		{"openai raw", openaiBody, ViewRaw, []string{"\n" + openaiBody}},

		{"request summary", anthropicRequest, ViewSummary, []string{
			"temperature: float 0.7",
			"stream: bool false",
			"messages: array[3] of object{role, content}",
			`metadata.user_id: "user_7f3e"`,
		}},
		{"request schema", anthropicRequest, ViewSchema, []string{
			`role: "user"|"assistant"`,
			"created_at: str<date-time>",
			"temperature: float",
			"stream: bool",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRender(t, tc.mode, tc.body, "application/json", "", opts)
			if !strings.HasPrefix(out, "body: application/json ") {
				t.Errorf("bad header: %q", strings.SplitN(out, "\n", 2)[0])
			}
			wantContains(t, out, tc.want...)
			if tc.mode == ViewSummary && len(out) > summaryBudget+64 {
				t.Errorf("summary too long: %d bytes", len(out))
			}
		})
	}
}

func TestRenderLongStringElided(t *testing.T) {
	opts := DefaultOptions()
	opts.StringTruncate = 50
	out := mustRender(t, ViewSummary, `{"text":"`+strings.Repeat("abcdefghij", 20)+`"}`, "application/json", "", opts)
	wantContains(t, out, `text: str "`+strings.Repeat("abcdefghij", 6)+`"…(200 chars)`)
}

func TestRenderTopLevelArrayAndScalars(t *testing.T) {
	opts := DefaultOptions()
	out := mustRender(t, ViewSummary, `[{"id":1,"name":"a"},{"id":2,"name":"b"},{"id":3},{"id":4}]`, "application/json", "", opts)
	wantContains(t, out, "json array[4] of object{id, name}", `[0]: {"id":1,"name":"a"}`, "… 1 more")

	out = mustRender(t, ViewSummary, `["x","y","z","w"]`, "application/json", "", opts)
	wantContains(t, out, `json array[4] of str ["x", "y", "z", …]`)

	out = mustRender(t, ViewSummary, `[1, 2.5, "s", null]`, "application/json", "", opts)
	wantContains(t, out, "json array[4] of int|float|str|null")

	out = mustRender(t, ViewSummary, `[]`, "application/json", "", opts)
	wantContains(t, out, "json array[0]")

	out = mustRender(t, ViewSummary, `42`, "application/json", "", opts)
	wantContains(t, out, "\nint 42")

	out = mustRender(t, ViewSummary, `{"a":{},"b":[],"c":true,"n":[[1],[2]]}`, "application/json", "", opts)
	wantContains(t, out, "a: object{}", "b: array[0]", "c: bool true", "n: array[2] of array")

	out = mustRender(t, ViewSchema, `[]`, "application/json", "", opts)
	wantContains(t, out, "\n[]")
	out = mustRender(t, ViewSchema, `{}`, "application/json", "", opts)
	wantContains(t, out, "\n{}")
}

func TestRenderManyKeys(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < 60; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"k` + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `":` + "1")
	}
	sb.WriteString("}")
	out := mustRender(t, ViewSummary, sb.String(), "application/json", "", DefaultOptions())
	wantContains(t, out, "json object (60 keys)", "… 20 more keys")
	out = mustRender(t, ViewSchema, sb.String(), "application/json", "", DefaultOptions())
	wantContains(t, out, "…")
}

func TestRenderHeaderLine(t *testing.T) {
	opts := DefaultOptions()
	gz := gzipBytes(t, []byte(openaiBody))
	out, _, bin, err := Render(ViewRaw, gz, "gzip", "application/json; charset=utf-8", "", opts)
	if err != nil || bin {
		t.Fatalf("err=%v bin=%v", err, bin)
	}
	first := strings.SplitN(out, "\n", 2)[0]
	wantContains(t, first, "body: application/json ", "B [gzip→390B] sha256:")
	if !strings.HasPrefix(first, "body: application/json "+itoa(len(gz))+"B") {
		t.Errorf("header size should be wire size: %s", first)
	}
	hash := first[strings.Index(first, "sha256:")+7:]
	if len(hash) != 8 {
		t.Errorf("want 8 hex chars of hash, got %q", hash)
	}

	// Unknown media type renders as "-".
	out = mustRender(t, ViewRaw, "hello", "", "", opts)
	wantContains(t, out, "body: - 5B sha256:")

	// Empty body.
	out = mustRender(t, ViewSummary, "", "text/plain", "", opts)
	wantContains(t, out, "body: text/plain 0B", "\n(empty)")
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRenderTruncationMarkers(t *testing.T) {
	opts := DefaultOptions()
	opts.MaxBytes = 300
	for _, mode := range []string{ViewTruncated, ViewPretty} {
		out := mustRender(t, mode, openaiBody, "application/json", "", opts)
		wantContains(t, out, "[truncated to 300 of ", "… [skipped ", " bytes] …")
		// Pretty-printed head.
		wantContains(t, out, "{\n  \"id\":")
	}
	out := mustRender(t, ViewRaw, openaiBody, "application/json", "", opts)
	wantContains(t, out, "[truncated to 300 of 390]", "… [truncated; 90 more bytes] …")
	if !strings.Contains(out, "\n"+openaiBody[:300]+"\n") {
		t.Errorf("raw head mismatch:\n%s", out)
	}

	// Pretty form too large for 4×MaxBytes: truncated mode falls back to
	// the compact body.
	big := `{"items":[` + strings.Repeat(`{"a":1,"b":2},`, 200) + `{"a":1}]}`
	opts.MaxBytes = 200
	out = mustRender(t, ViewTruncated, big, "application/json", "", opts)
	if strings.Contains(out, "{\n  \"items\"") {
		t.Errorf("expected compact head when pretty form exceeds 4×MaxBytes")
	}
	wantContains(t, out, "[truncated to 200 of "+itoa(len(big))+"]")

	// MaxBytes above the cap is clamped.
	opts.MaxBytes = MaxBytesCap * 4
	out = mustRender(t, ViewRaw, strings.Repeat("x", MaxBytesCap+10), "text/plain", "", opts)
	wantContains(t, out, "[truncated to 1048576 of 1048586]")
}

func TestRenderNeverSplitsRunes(t *testing.T) {
	body := strings.Repeat("日本語テキスト🙂", 100) // 3- and 4-byte runes
	for _, mode := range []string{ViewTruncated, ViewPretty, ViewRaw} {
		for _, max := range []int{7, 64, 65, 66, 67, 100, 333} {
			opts := DefaultOptions()
			opts.MaxBytes = max
			out := mustRender(t, mode, body, "text/plain", "", opts)
			if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
				t.Fatalf("%s max=%d: split a rune:\n%q", mode, max, out)
			}
		}
	}
	// JSON with multibyte strings.
	js := `{"s":"` + strings.Repeat("éàü🙂", 200) + `"}`
	for _, max := range []int{50, 51, 52, 53} {
		opts := DefaultOptions()
		opts.MaxBytes = max
		for _, mode := range []string{ViewTruncated, ViewPretty, ViewRaw} {
			out := mustRender(t, mode, js, "application/json", "", opts)
			if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
				t.Fatalf("%s max=%d: split a rune:\n%q", mode, max, out)
			}
		}
	}
}

func TestCutHeadTail(t *testing.T) {
	b := []byte("aé🙂z")
	for n := 0; n <= len(b)+1; n++ {
		h := cutHead(b, n)
		if !utf8.Valid(h) || len(h) > n {
			t.Errorf("cutHead(%d) = %q", n, h)
		}
		tl := cutTail(b, n)
		if !utf8.Valid(tl) || len(tl) > n {
			t.Errorf("cutTail(%d) = %q", n, tl)
		}
	}
	if got := string(cutTail(b, 4)); got != "🙂z"[0:0]+"z" && got != "z" {
		t.Errorf("cutTail(4) = %q", got)
	}
	if got := string(cutTail(b, 5)); got != "🙂z" {
		t.Errorf("cutTail(5) = %q", got)
	}
}

func TestRenderBinary(t *testing.T) {
	opts := DefaultOptions()
	tests := []struct {
		name, mime string
		body       []byte
	}{
		{"png", "image/png", []byte("\x89PNG\r\n\x1a\n....")},
		{"font", "font/woff2", []byte("wOF2")},
		{"media", "audio/mpeg", []byte("ID3")},
		{"octet", "application/octet-stream", []byte(`{"looks":"like json"}`)},
		{"pdf", "application/pdf", []byte("%PDF-1.7")},
		{"invalid utf8", "text/plain", []byte("abc\xff\xfe")},
		{"nul byte", "", []byte("abc\x00def")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{ViewSummary, ViewSchema, ViewTruncated, ViewPretty, ViewRaw} {
				out, n, bin, err := Render(mode, tc.body, "", tc.mime, "", opts)
				if err != nil {
					t.Fatal(err)
				}
				if !bin || n != 0 {
					t.Errorf("%s: binary=%v redacted=%d", mode, bin, n)
				}
				lines := strings.Split(out, "\n")
				if len(lines) != 2 || lines[1] != BinaryNote {
					t.Errorf("%s: want header + note, got:\n%s", mode, out)
				}
				if strings.Contains(out, "json") && tc.name == "octet" {
					t.Errorf("binary content leaked: %s", out)
				}
			}
		})
	}
}

func TestRenderModeErrors(t *testing.T) {
	if _, _, _, err := Render("yaml", []byte("{}"), "", "application/json", "", Options{}); err == nil {
		t.Fatal("expected error for unknown mode")
	}
	out, _, _, err := Render("", []byte(`{"a":1}`), "", "application/json", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "json object (1 keys)")
}

func TestRenderDecodeErrors(t *testing.T) {
	gz := gzipBytes(t, []byte(openaiBody))
	// Truncated stream: partial content plus a note.
	out, _, bin, err := Render(ViewRaw, gz[:len(gz)-20], "gzip", "application/json", "", DefaultOptions())
	if err != nil || bin {
		t.Fatalf("err=%v bin=%v", err, bin)
	}
	wantContains(t, out, "[decode error: gzip:", `"chatcmpl-`)

	// Garbage: nothing decodable.
	if _, _, _, err := Render(ViewRaw, []byte("not gzip at all"), "gzip", "application/json", "", DefaultOptions()); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestRenderPath(t *testing.T) {
	opts := DefaultOptions()
	tests := []struct {
		name, mode, path string
		want             []string
	}{
		{"gjson object", ViewPretty, "choices.0.message", []string{"path:choices.0.message", "{\n  \"role\": \"assistant\""}},
		{"jsonpath object", ViewPretty, "$.choices[0].message", []string{"path:choices.0.message", `"role": "assistant"`}},
		{"jsonpath bracket key", ViewRaw, "$['usage']['total_tokens']", []string{"path:usage.total_tokens", "\n35"}},
		{"wildcard", ViewRaw, "$.choices[*].finish_reason", []string{"path:choices.#.finish_reason", `["stop"]`}},
		{"string raw", ViewSummary, "choices.0.message.content", []string{"path:choices.0.message.content", "\nSure — here is a haiku.\nOld pond"}},
		{"string raw mode", ViewRaw, "model", []string{"\ngpt-4o-2024-05-13"}},
		{"number summary", ViewSummary, "usage.total_tokens", []string{"\nint 35"}},
		{"schema of subtree", ViewSchema, "usage", []string{"{ prompt_tokens: int, completion_tokens: int, total_tokens: int }"}},
		{"summary of subtree", ViewSummary, "choices.0", []string{"json object (4 keys)", `finish_reason: str "stop"`}},
		{"count", ViewRaw, "choices.#", []string{"\n1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRender(t, tc.mode, openaiBody, "application/json", tc.path, opts)
			wantContains(t, out, tc.want...)
		})
	}

	// String result is truncated like raw text.
	small := opts
	small.MaxBytes = 20
	out := mustRender(t, ViewPretty, openaiBody, "application/json", "choices.0.message.content", small)
	wantContains(t, out, "[truncated to 20 of", "… [skipped ")
	out = mustRender(t, ViewRaw, openaiBody, "application/json", "choices.0.message.content", small)
	wantContains(t, out, "… [truncated; ")

	// Errors.
	errCases := []struct {
		name, body, mime, path, want string
	}{
		{"missing key", openaiBody, "application/json", "nope", "top-level keys: id, object, created, model, choices, usage, system_fingerprint"},
		{"missing nested", openaiBody, "application/json", "$.choices[3].x", "not found"},
		{"array root", `[1,2,3]`, "application/json", "x", "array of 3 elements"},
		{"empty object", `{}`, "application/json", "x", "top-level object is empty"},
		{"scalar root", `"s"`, "application/json", "x", "body is a JSON str"},
		{"sse", anthropicSSE, "text/event-stream", "message.usage", "pano_flow_explain"},
		{"html", htmlBody, "text/html", "title", "body is html, not JSON"},
		{"invalid utf8 path", openaiBody, "application/json", "a\xff", "not valid UTF-8"},
	}
	for _, tc := range errCases {
		t.Run("err "+tc.name, func(t *testing.T) {
			_, _, _, err := Render(ViewRaw, []byte(tc.body), "", tc.mime, tc.path, opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestNormalizePath(t *testing.T) {
	tests := map[string]string{
		"":                        "",
		"a.b":                     "a.b",
		"a.0.b":                   "a.0.b",
		"$":                       "",
		"$.a[0].b":                "a.0.b",
		"$.a":                     "a",
		"$['a b'].c":              "a b.c",
		`$["x.y"]`:                `x\.y`,
		"$.items[*].id":           "items.#.id",
		"a[1]":                    "a.1",
		"  $.a[2]  ":              "a.2",
		"$.a[":                    "a.[",
		"messages.#(role==user)#": "messages.#(role==user)#",
		"choices.#":               "choices.#",
	}
	for in, want := range tests {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderRedaction(t *testing.T) {
	body := `{"api_key":"sk-ant-api03-abcdefghijklmnop","ok":true}`
	opts := DefaultOptions()
	out, n, _, err := Render(ViewPretty, []byte(body), "", "application/json", "", opts)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || strings.Contains(out, "abcdefghijklmnop") {
		t.Errorf("redacted=%d out=%s", n, out)
	}
	wantContains(t, out, `"api_key": "sk-ant-…mnop hash:`)

	opts.RevealSecrets = true
	out, n, _, _ = Render(ViewPretty, []byte(body), "", "application/json", "", opts)
	if n != 0 || !strings.Contains(out, "abcdefghijklmnop") {
		t.Errorf("reveal: redacted=%d out=%s", n, out)
	}
	_, n, _, _ = Render(ViewPretty, []byte(body), "", "application/json", "", Options{})
	if n != 0 {
		t.Errorf("zero Options should not redact, got %d", n)
	}
}

func TestRenderSensitiveKeysMasked(t *testing.T) {
	opts := DefaultOptions()
	body := `{"password":"hunter2","token":1234567890,"api_key":"sk_live_abcdefgh","secret":{"inner":"topsecret"},"cookie":["c1value","c2value"],"nested":{"client_secret":"deep1234","user_id":"u1"},"model":"m"}`
	leaks := []string{"hunter2", "1234567890", "sk_live_abcdefgh", "topsecret", "c1value", "deep1234"}
	tests := []struct {
		mode, mime, body string
		want             []string
		minRedacted      int
	}{
		{ViewSummary, "application/json", body, []string{
			`password: str "…`, "token: int …7890 hash:", "api_key: str \"sk_…",
			"secret: object[1] (redacted)", "cookie: array[2] (redacted)",
			"nested: object{2: client_secret, user_id}", `model: str "m"`,
		}, 5},
		{
			ViewSchema, "application/json", `[{"password":"hunter2","role":"user"},{"password":"topsecret","role":"admin"}]`,
			[]string{`password: "…`, ` hash:`, `role: "user"|"admin"`},
			2,
		},
		{ViewSchema, "application/json", `{"secret":{"kind":["hunter2","topsecret"]}}`, []string{`kind: [ "…`}, 2},
		{ViewSummary, "application/x-www-form-urlencoded", "user=bob&password=hunter2&access_token=deep1234", []string{"user: bob", "password: …", "access_token: …"}, 2},
		{ViewPretty, "application/x-www-form-urlencoded", "user=bob&password=hunter2", []string{"user: bob\npassword: … hash:"}, 1},
		{ViewSchema, "application/x-www-form-urlencoded", "password=hunter2", []string{"{ password: str }"}, 0},
		{ViewSummary, "text/event-stream", "event: auth\ndata: {\"password\":\"hunter2\"}\n\n", []string{`first: auth {"password":"…`}, 1},
		{ViewSchema, "text/event-stream", "data: {\"password\":\"hunter2\"}\n\ndata: {\"password\":\"topsecret\"}\n\n", []string{`password: "…`}, 2},
		// httpbin-style echo: JSON inside a JSON string, shown escaped.
		{ViewSummary, "application/json", `{"data":"{\"password\":\"hunter2\"}","json":{"password":"hunter2"}}`, []string{`data: str "{\"password\":\"…`, `json: object{1: password}`}, 1},
		{ViewPretty, "application/json", `{"data":"{\"password\":\"hunter2\"}"}`, []string{`"data": "{\"password\":\"…`}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.mode+" "+tc.mime, func(t *testing.T) {
			out, n, _, err := Render(tc.mode, []byte(tc.body), "", tc.mime, "", opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, leak := range leaks {
				if strings.Contains(out, leak) {
					t.Errorf("leaked %q:\n%s", leak, out)
				}
			}
			wantContains(t, out, tc.want...)
			if n < tc.minRedacted {
				t.Errorf("redacted = %d, want >= %d\n%s", n, tc.minRedacted, out)
			}
			// Revealing shows the values and counts nothing.
			reveal := opts
			reveal.RevealSecrets = true
			out, n, _, _ = Render(tc.mode, []byte(tc.body), "", tc.mime, "", reveal)
			if n != 0 {
				t.Errorf("reveal: redacted = %d\n%s", n, out)
			}
			if tc.mode != ViewSchema && !strings.Contains(out, "hunter2") {
				t.Errorf("reveal should show the value:\n%s", out)
			}
		})
	}

	// Notable lines never descend into sensitive subtrees.
	out, _, _, _ := Render(ViewSummary, []byte(`{"auth":{"token":{"user_id":"u1","value":"hunter2"},"session_id":"s1"}}`), "", "application/json", "", opts)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "auth.token.user_id") {
		t.Errorf("notable leaked sensitive subtree:\n%s", out)
	}
	wantContains(t, out, `auth.session_id: "s1"`)
}

func TestRenderOtherKinds(t *testing.T) {
	opts := DefaultOptions()

	t.Run("sniffed json", func(t *testing.T) {
		out := mustRender(t, ViewSummary, ` {"a":1}`, "text/plain", "", opts)
		wantContains(t, out, "json object (1 keys)")
		out = mustRender(t, ViewSummary, `{"a":1}`, "", "", opts)
		wantContains(t, out, "json object (1 keys)")
	})
	t.Run("invalid json is text", func(t *testing.T) {
		out := mustRender(t, ViewSummary, `{"a":`, "application/json", "", opts)
		wantContains(t, out, "text (5 chars, 1 lines)")
	})
	t.Run("html", func(t *testing.T) {
		out := mustRender(t, ViewSummary, htmlBody, "text/html; charset=utf-8", "", opts)
		wantContains(t, out, "html (231 chars)", "title: Pano & Friends", "text: Pano & Friends Welcome Some bold text here.")
		if strings.Contains(out, "ignored") || strings.Contains(out, "color:red") {
			t.Errorf("script/style leaked: %s", out)
		}
		out = mustRender(t, ViewSchema, htmlBody, "text/html", "", opts)
		wantContains(t, out, "(no schema for html; showing summary)")
		out = mustRender(t, ViewPretty, htmlBody, "text/html", "", opts)
		wantContains(t, out, "\n"+htmlBody)
	})
	t.Run("sse", func(t *testing.T) {
		out := mustRender(t, ViewSummary, anthropicSSE, "text/event-stream", "", opts)
		wantContains(t, out,
			"sse: 7 events",
			"events: message_start 1, content_block_start 1, content_block_delta 2, content_block_stop 1, message_delta 1, message_stop 1",
			`first: message_start {"type":"message_start"`,
			`last: message_stop {"type":"message_stop"}`,
			"next: pano_flow_explain")
		out = mustRender(t, ViewSchema, anthropicSSE, "text/event-stream", "", opts)
		wantContains(t, out, "events: 7", `content_block_delta (2): {`, `delta: { type: "text_delta", text: "Hel"|"lo" }`)

		// Sniffed SSE with unnamed events and no LLM hint.
		plain := "data: one\n\ndata: two\r\n\r\ndata: three"
		out = mustRender(t, ViewSummary, plain, "text/plain", "", opts)
		wantContains(t, out, "sse: 3 events", "events: message 3", "first: message one", "last: message three")
		if strings.Contains(out, "pano_flow_explain") {
			t.Errorf("unexpected LLM hint: %s", out)
		}
		out = mustRender(t, ViewSchema, plain, "text/event-stream", "", opts)
		wantContains(t, out, `message (3): "one"|"two"|"three"`)

		// OpenAI-style stream detected by data.
		oai := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\"}\n\ndata: [DONE]\n\n"
		out = mustRender(t, ViewSummary, oai, "text/event-stream", "", opts)
		wantContains(t, out, "next: pano_flow_explain")

		out = mustRender(t, ViewSummary, ": only a comment\n", "text/event-stream", "", opts)
		wantContains(t, out, "sse: 0 events")
	})
	t.Run("form", func(t *testing.T) {
		form := "grant_type=client_credentials&scope=read%20write&note=a%26b&flag"
		out := mustRender(t, ViewSummary, form, "application/x-www-form-urlencoded", "", opts)
		wantContains(t, out, "form (4 fields)", "grant_type: client_credentials", "scope: read write", "note: a&b", "flag: ")
		out = mustRender(t, ViewPretty, form, "application/x-www-form-urlencoded", "", opts)
		wantContains(t, out, "\ngrant_type: client_credentials\nscope: read write\n")
		out = mustRender(t, ViewSchema, form, "application/x-www-form-urlencoded", "", opts)
		wantContains(t, out, "{ grant_type: str, scope: str, note: str, flag: str }")
		out = mustRender(t, ViewRaw, form, "application/x-www-form-urlencoded", "", opts)
		wantContains(t, out, "\n"+form)
		// Multipart is shown as text.
		out = mustRender(t, ViewSummary, "--b\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\n1\r\n--b--", "multipart/form-data; boundary=b", "", opts)
		wantContains(t, out, "text (")
	})
	t.Run("xml", func(t *testing.T) {
		out := mustRender(t, ViewSummary, "<?xml version=\"1.0\"?>\n<!-- c -->\n<feed xmlns=\"x\"><entry/></feed>", "application/xml", "", opts)
		wantContains(t, out, "xml (", "root: <feed>")
	})
	t.Run("text", func(t *testing.T) {
		long := strings.Repeat("word ", 200)
		out := mustRender(t, ViewSummary, long, "text/plain", "", opts)
		wantContains(t, out, "text (1000 chars, 1 lines)", "…")
		out = mustRender(t, ViewSummary, "a\nb\n", "text/csv", "", opts)
		wantContains(t, out, "text (4 chars, 2 lines)\na b")
		out = mustRender(t, ViewSummary, "body{}", "text/css", "", opts)
		wantContains(t, out, "css (6 chars, 1 lines)")
		out = mustRender(t, ViewSummary, "x=1", "application/javascript", "", opts)
		wantContains(t, out, "js (3 chars")
	})
}

const formatsBody = `{"t":"2026-08-27T10:00:00.5Z","t2":"2026-08-27T10:00:00","d":"2026-08-27","u":"123e4567-e89b-12d3-a456-426614174000","l":"https://example.com/x","e":"a@b.co"}`

func TestSchemaInference(t *testing.T) {
	opts := DefaultOptions()
	tests := []struct {
		name, body string
		want       []string
	}{
		{"optional keys", `[{"a":1,"b":"x"},{"a":2},{"a":3,"c":null}]`, []string{"a: int", "b?: str", "c?: null"}},
		{"enum", `[{"r":"user"},{"r":"assistant"},{"r":"user"}]`, []string{`r: "user"|"assistant"`}},
		{"too many for enum", `["a","b","c","d","e","f","g"]`, []string{"[ str ]"}},
		{"single sample not enum", `{"finish_reason":"stop"}`, []string{"finish_reason: str"}},
		{"long strings not enum", `["` + strings.Repeat("x", 40) + `","` + strings.Repeat("y", 40) + `"]`, []string{"[ str ]"}},
		{"formats", formatsBody, []string{"t: str<date-time>", "t2: str<date-time>", "d: str<date>", "u: str<uuid>", "l: str<url>", "e: str<email>"}},
		{"mixed format", `[{"v":"https://x"},{"v":"plain"}]`, []string{"v: str"}},
		{"int float merge", `[{"n":1},{"n":1.5}]`, []string{"n: float"}},
		{"nullable", `[{"n":1},{"n":null}]`, []string{"n: int|null"}},
		{"nullable object", `[{"o":{"a":1}},{"o":null}]`, []string{"o: { a: int }|null"}},
		{"mixed scalar and object", `[1,{"a":true},[2]]`, []string{"{ a: bool }|[ int ]|int"}},
		{"quoted key", `{"a b":1,"c-d":2,"e.f":3}`, []string{`"a b": int`, "c-d: int", `"e.f": int`}},
		{"deep", `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":1}}}}}}}}`, []string{"…"}},
		{"deep array", `[[[[[[[[1]]]]]]]]`, []string{"…"}},
		{"empty containers", `{"a":[],"b":{}}`, []string{"a: []", "b: {}"}},
		{"bool", `{"x":true,"y":false}`, []string{"x: bool", "y: bool"}},
		{"array of arrays", `[[1,2],[3]]`, []string{"[ [ int ] ]"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRender(t, ViewSchema, tc.body, "application/json", "", opts)
			wantContains(t, out, tc.want...)
		})
	}

	// Budget cap.
	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < 40; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"key_number_` + itoa(i) + `_with_a_long_name":{"nested_field_one":"value","nested_field_two":[1,2,3],"x":{"y":{"z":true}}}`)
	}
	sb.WriteString("}")
	out := mustRender(t, ViewSchema, sb.String(), "application/json", "", opts)
	if len(out) > schemaBudget+128 {
		t.Errorf("schema too long: %d", len(out))
	}
	wantContains(t, out, "…")
}

func TestSummaryBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < 40; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"key_number_` + itoa(i) + `":"` + strings.Repeat("v", 150) + `"`)
	}
	sb.WriteString("}")
	out := mustRender(t, ViewSummary, sb.String(), "application/json", "", DefaultOptions())
	if len(out) > summaryBudget+128 {
		t.Errorf("summary too long: %d", len(out))
	}
	if !utf8.ValidString(out) {
		t.Error("invalid utf8")
	}
}

func TestQuoteText(t *testing.T) {
	got := quoteText("a\"b\\c\nd\te\rf\x01g")
	want := `"a\"b\\c\nd\te\rf\u0001g"`
	if got != want {
		t.Errorf("quoteText = %s, want %s", got, want)
	}
}
