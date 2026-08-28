package view

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

var redactCases = []struct {
	name, in, secret string
	want             string // substring expected in output
	n                int
}{
	{"bearer", `Authorization: Bearer abc.def-123_XYZ==`, "abc.def-123_XYZ==", "Authorization: Bearer abc…YZ== hash:", 1},
	{"bearer lower", `authorization: bearer tokentokentoken`, "tokentokentoken", "bearer tok…oken hash:", 1},
	{"bearer in json", `{"Authorization":"Bearer sk-abcdefghijklmnopqrstuvwxyz"}`, "abcdefghijklmnopqrstuvwxyz", `{"Authorization":"Bearer sk-…wxyz hash:`, 1},
	{"openai", `key=sk-proj-abcdefghijklmnopqrstuvwxyz0123`, "abcdefghijklmnopqrstuvwxyz0123", "sk-proj-…0123 hash:", 1},
	{"openai short prefix", `x sk-abcdefghijklmnop y`, "abcdefghijklmnop", "x sk-…mnop hash:", 1},
	{"anthropic", `sk-ant-api03-abcdefgh`, "abcdefgh", "sk-ant-…efgh hash:", 1},
	{"aws", `AKIAIOSFODNN7EXAMPLE`, "IOSFODNN7", "AKIA…MPLE hash:", 1},
	{"google", `AIzaSyA-1234567890abcdefghijklmnopqrstu`, "1234567890abcdefghijklmnop", "AIza…rstu hash:", 1},
	{"github", `ghp_abcdefghijklmnopqrstuvwxyz0123456789`, "abcdefghijklmnopqrstuvwxyz01", "ghp_…6789 hash:", 1},
	{"slack", `xoxb-1234567890-abcdefghij`, "1234567890-abcdef", "xoxb-…ghij hash:", 1},
	{"jwt", `eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c`, "eyJzdWIi", "eyJ…sw5c hash:", 1},
	{"mailgun", `key-abcdefghijklmnopqrstuvwxyzABCDEF`, "ghijklmnopqrstuvwxyz", "key-…CDEF hash:", 1},
	{"url basic auth", `https://user:hunter2hunter2@example.com/x`, "hunter2hunter2", "https://user:hun…ter2 hash:", 1},
	{"json password", `{"password": "correct horse"}`, "correct horse", `{"password": "cor…orse hash:`, 1},
	{"json api_key", `{"api_key":"abc"}`, "abc", `{"api_key":"… hash:`, 1},
	{"json escaped", `{"client_secret":"a\"b1234567890"}`, "1234567890", `{"client_secret":"a\"…7890 hash:`, 1},
	{"json Token case", `{"Token":"abcdefghij"}`, "abcdefghij", `{"Token":"…ghij hash:`, 1},
	{"json max_tokens untouched", `{"max_tokens":"1024"}`, "", `{"max_tokens":"1024"}`, 0},
	{"form", `grant=x&refresh_token=abcdefghijkl&y=1`, "abcdefghijkl", "grant=x&refresh_token=…ijkl hash:", 1},
	{"form at start", `secret=topsecretvalue`, "topsecretvalue", "secret=top…alue hash:", 1},
	{"form tokens untouched", `max_tokens=5&tokens=6`, "", `max_tokens=5&tokens=6`, 0},
	{"private_key", `private_key=-----BEGIN`, "BEGIN", "private_key=…EGIN hash:", 1},
	{"multiple", `a=sk-abcdefghijklmnopqrst b=AKIAIOSFODNN7EXAMPLE`, "IOSFODNN7", "hash:", 2},
	{"no secrets", `{"model":"claude-opus-5","max_tokens":1024,"task-id":"task-abcdefghijklmnopqrst"}`, "", `{"model":"claude-opus-5","max_tokens":1024,"task-id":"task-abcdefghijklmnopqrst"}`, 0},
	{"empty value", `{"password":""}`, "", `{"password":""}`, 0},
	// Rendered-line styles (summary, form and header views).
	{"line quoted", `password: "hunter2hunter2"`, "hunter2hunter2", `password: "hun…ter2 hash:`, 1},
	{"line summary str", `password: str "hunter2hunter2"`, "hunter2hunter2", `password: str "hun…ter2 hash:`, 1},
	{"line summary elided", `  api_key: str "abcdefghijkl"…(300 chars)`, "abcdefghijkl", `  api_key: str "…ijkl hash:`, 1},
	{"line summary int", `token: int 123456789`, "123456789", `token: int …6789 hash:`, 1},
	{"line form", `client_secret: hunter2 with spaces`, "hunter2", `client_secret: hun…aces hash:`, 1},
	{"line header cookie", `Cookie: a=1; session=abcdefgh`, "abcdefgh", `Cookie: a=1…efgh hash:`, 1},
	{"line header set-cookie", "Set-Cookie: sid=abcdefgh; Path=/\nX: y", "abcdefgh", "Set-Cookie: sid…th=/ hash:", 1},
	{"line header authorization", `Authorization: Bearer abcdefghijkl`, "abcdefghijkl", `Authorization: Bearer …ijkl hash:`, 1},
	{"line enum", `password: "hunter2"|"topsecret",`, "hunter2", `password: "… hash:`, 2},
	{"line schema type untouched", "{ password: str, token?: str<uuid>, secret: {\n  api_key: int|null\n}, cookie: [ str ] }", "", "{ password: str, token?: str<uuid>, secret: {\n  api_key: int|null\n}, cookie: [ str ] }", 0},
	{"line summary shape untouched", `secret: object[1] (redacted)`, "", `secret: object[1] (redacted)`, 0},
	{"line value starting like a type", `password: internal123`, "internal123", `password: …l123 hash:`, 1},
	// JSON embedded in a JSON string (httpbin-style echo).
	{"escaped json", `{"data":"{\"password\":\"hunter2\",\"user\":\"bob\"}"}`, "hunter2", `{"data":"{\"password\":\"… hash:`, 1},
	{"escaped json spaced", `"{\"api_key\": \"sk_live_abcdefgh\"}"`, "sk_live_abcdefgh", `"{\"api_key\": \"sk_…efgh hash:`, 1},
	{"escaped json inner escape", `"{\"token\":\"a\\nb1234567\"}"`, "b1234567", `"{\"token\":\"…4567 hash:`, 1},
	{"escaped json other key", `"{\"model\":\"claude\"}"`, "", `"{\"model\":\"claude\"}"`, 0},
	{"line max_tokens untouched", "max_tokens: int 1024\ntokens: 5\ntoken_count: 3", "", "max_tokens: int 1024\ntokens: 5\ntoken_count: 3", 0},
	{"line other key untouched", `model: str "claude-opus-5"`, "", `model: str "claude-opus-5"`, 0},
	{"line inline", `{ token: "abcdefgh", other: 1 }`, "abcdefgh", `{ token: "…efgh hash:`, 1},
	{"line empty untouched", "password: \nnext: 1", "", "password: \nnext: 1", 0},
}

func TestRedactText(t *testing.T) {
	for _, tc := range redactCases {
		t.Run(tc.name, func(t *testing.T) {
			out, n := RedactText(tc.in)
			if n != tc.n {
				t.Errorf("count = %d, want %d (out=%s)", n, tc.n, out)
			}
			if tc.secret != "" && strings.Contains(out, tc.secret) {
				t.Errorf("secret leaked: %s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("out = %q, want substring %q", out, tc.want)
			}
			// Idempotent.
			again, m := RedactText(out)
			if again != out || m != 0 {
				t.Errorf("not idempotent: %q -> %q (%d)", out, again, m)
			}
		})
	}
}

func TestMask(t *testing.T) {
	re := regexp.MustCompile(`^(.*)…(.*) hash:[0-9a-f]{4}$`)
	tests := []struct{ in, prefix, suffix string }{
		{"sk-ant-api03-abcdefghijklmnop", "sk-ant-", "mnop"},
		{"sk-abcdefghijklmnop", "sk-", "mnop"},
		{"ghp_abcdefghijklmnop", "ghp_", "mnop"},
		{"AKIAIOSFODNN7EXAMPLE", "AKIA", "MPLE"},
		{"eyJa.b.c", "eyJ", ""},
		{"plainlongpassword", "pla", "word"},
		{"short", "", ""},
		{"12345678", "", "5678"},
		{"héllo wörld señor", "hél", "eñor"},
	}
	for _, tc := range tests {
		got := Mask(tc.in)
		m := re.FindStringSubmatch(got)
		if m == nil {
			t.Errorf("Mask(%q) = %q: bad shape", tc.in, got)
			continue
		}
		if m[1] != tc.prefix || m[2] != tc.suffix {
			t.Errorf("Mask(%q) = %q, want prefix %q suffix %q", tc.in, got, tc.prefix, tc.suffix)
		}
	}
	if Mask("a") == Mask("b") {
		t.Error("hash should differ")
	}
	sum := sha256.Sum256([]byte("sk-ant-api03-abcdefghijklmnop"))
	if want := "sk-ant-…mnop hash:" + hex.EncodeToString(sum[:2]); Mask("sk-ant-api03-abcdefghijklmnop") != want {
		t.Errorf("hash should be the first 4 hex of sha256: want %s", want)
	}
	if Mask("") != "" {
		t.Error("empty stays empty")
	}
}

func TestRedactHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-api03-abcdefghijklmnop")
	h.Set("Proxy-Authorization", "Basic dXNlcjpwYXNz")
	h.Set("X-Api-Key", "sk-abcdefghijklmnopqrstuvwxyz")
	h.Set("Cookie", "session=abcdefghijklmnop; theme=dark; empty=")
	h.Add("Set-Cookie", "sid=abcdefghijklmnop; Path=/; HttpOnly")
	h.Add("Set-Cookie", "flagvalue")
	h.Add("Set-Cookie", "")
	h.Set("X-Custom", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c")
	h.Set("Content-Type", "application/json")
	h.Set("Openai-Organization", "org-abcdefghijklmnop")

	out, n := RedactHeaders(h, false)
	if n != 8 {
		t.Errorf("count = %d, want 8\n%s", n, FormatHeaders(out))
	}
	checks := map[string]string{
		"Authorization":       "Bearer sk-ant-…mnop hash:",
		"Proxy-Authorization": "Basic …YXNz hash:",
		"X-Api-Key":           "sk-…wxyz hash:",
		"Cookie":              "session=abc…mnop hash:",
		"X-Custom":            "token eyJ…sw5c hash:",
		"Content-Type":        "application/json",
		"Openai-Organization": "org…mnop hash:",
	}
	for k, want := range checks {
		if got := out.Get(k); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want substring %q", k, got, want)
		}
	}
	if c := out.Get("Cookie"); !strings.Contains(c, "; theme=… hash:") || !strings.HasSuffix(c, "; empty=") {
		t.Errorf("cookie structure lost: %q", c)
	}
	sc := out.Values("Set-Cookie")
	if len(sc) != 3 || !strings.HasPrefix(sc[0], "sid=abc…mnop hash:") || !strings.HasSuffix(sc[0], "; Path=/; HttpOnly") || !strings.HasPrefix(sc[1], "…alue hash:") || sc[2] != "" {
		t.Errorf("set-cookie = %q", sc)
	}
	for _, leak := range []string{"abcdefghijklmnop", "dXNlcjpwYXNz", "eyJzdWIi"} {
		if strings.Contains(FormatHeaders(out), leak) {
			t.Errorf("leaked %q", leak)
		}
	}
	// Input untouched; output idempotent.
	if h.Get("Authorization") != "Bearer sk-ant-api03-abcdefghijklmnop" {
		t.Error("input header was modified")
	}
	again, m := RedactHeaders(out, false)
	if m != 0 || FormatHeaders(again) != FormatHeaders(out) {
		t.Errorf("not idempotent: %d\n%s", m, FormatHeaders(again))
	}

	// reveal returns an untouched clone.
	rev, m := RedactHeaders(h, true)
	if m != 0 || rev.Get("Authorization") != h.Get("Authorization") {
		t.Error("reveal should not mask")
	}
	rev.Set("Authorization", "changed")
	if h.Get("Authorization") == "changed" {
		t.Error("reveal must clone")
	}

	// nil input.
	if out, n := RedactHeaders(nil, false); out != nil || n != 0 {
		t.Errorf("nil: %v %d", out, n)
	}
}

func TestRedactExtra(t *testing.T) {
	defer func() { Extra.Headers, Extra.Patterns = nil, nil }()
	Extra.Headers = []string{"X-Tenant-Secret"}
	Extra.Patterns = []*regexp.Regexp{regexp.MustCompile(`ACME-[0-9]{6}`), nil}

	h := http.Header{"X-Tenant-Secret": {"value123456"}, "X-Note": {"ref ACME-123456 ok"}}
	out, n := RedactHeaders(h, false)
	if n != 2 {
		t.Errorf("count = %d", n)
	}
	if v := out.Get("X-Tenant-Secret"); !strings.HasPrefix(v, "…3456 hash:") {
		t.Errorf("extra header not masked: %q", v)
	}
	if v := out.Get("X-Note"); !strings.Contains(v, "ref …3456 hash:") {
		t.Errorf("extra pattern not masked: %q", v)
	}
	s, n := RedactText("ACME-000001 and ACME-000001")
	if n != 2 || strings.Contains(s, "ACME-000001") {
		t.Errorf("RedactText extra: %q %d", s, n)
	}
	if again, m := RedactText(s); again != s || m != 0 {
		t.Errorf("extra pattern not idempotent: %q", again)
	}
}

func TestFormatHeaders(t *testing.T) {
	h := http.Header{}
	h.Add("Zeta", "1")
	h.Add("accept", "text/html")
	h.Add("Accept-Encoding", "gzip")
	h.Add("Accept-Encoding", "br")
	got := FormatHeaders(h)
	want := "Accept: text/html\nAccept-Encoding: gzip\nAccept-Encoding: br\nZeta: 1"
	if got != want {
		t.Errorf("FormatHeaders =\n%s\nwant\n%s", got, want)
	}
	if FormatHeaders(nil) != "" {
		t.Error("nil should render empty")
	}
}
