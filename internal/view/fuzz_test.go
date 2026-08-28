package view

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzRedactText(f *testing.F) {
	for _, tc := range redactCases {
		f.Add(tc.in)
	}
	f.Add("Bearer …a1b2 hash:9f3c")
	f.Add(`{"token":"…"}`)
	f.Add("://:@")
	f.Fuzz(func(t *testing.T, s string) {
		out, n := RedactText(s)
		if n < 0 {
			t.Fatalf("negative count")
		}
		if n == 0 && out != s {
			t.Fatalf("changed text without counting: %q -> %q", s, out)
		}
		again, m := RedactText(out)
		if again != out || m != 0 {
			t.Fatalf("not idempotent: %q -> %q -> %q (%d)", s, out, again, m)
		}
	})
}

func FuzzRender(f *testing.F) {
	modes := []string{ViewSummary, ViewSchema, ViewTruncated, ViewPretty, ViewRaw, "", "bogus"}
	seeds := []struct {
		body, enc, mime, path string
	}{
		{anthropicBody, "", "application/json", ""},
		{openaiBody, "", "application/json", "$.choices[0].message.content"},
		{anthropicRequest, "", "application/json", "messages.#.role"},
		{anthropicSSE, "", "text/event-stream", ""},
		{htmlBody, "", "text/html", ""},
		{"a=1&b=2", "", "application/x-www-form-urlencoded", ""},
		{"\x1f\x8b\x08\x00", "gzip", "application/json", ""},
		{"[1,[2,[3,[4,[5,[6,[7,[8]]]]]]]]", "", "", "0.1"},
		{"data: {\"a\":1}\n\n", "", "text/plain", "a"},
		{"\xff\xfe", "", "text/plain", ""},
		{`{"a":"\ud800"}`, "", "application/json", "a"},
		{`{"a":1}`, "br, gzip", "application/json", "@this"},
		{`{"a":{"b":[{"c":"d"}]}}`, "", "application/json", `$['a']["b"][*].c`},
	}
	for i, s := range seeds {
		f.Add(modes[i%len(modes)], []byte(s.body), s.enc, s.mime, s.path, 64)
	}
	f.Fuzz(func(t *testing.T, mode string, body []byte, enc, mime, path string, maxBytes int) {
		opts := DefaultOptions()
		opts.MaxBytes = maxBytes
		opts.StringTruncate = maxBytes % 300
		out, n, bin, err := Render(mode, body, enc, mime, path, opts)
		if err != nil {
			if out != "" || n != 0 || bin {
				t.Fatalf("error with output: %v", err)
			}
			return
		}
		if !strings.HasPrefix(out, "body: ") {
			t.Fatalf("missing header line: %q", out)
		}
		if bin && !strings.HasSuffix(out, "\n"+BinaryNote) {
			t.Fatalf("binary without note: %q", out)
		}
		if !bin && utf8.Valid(body) && enc == "" && !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 output for valid input")
		}
	})
}
