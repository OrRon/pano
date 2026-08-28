package view

import (
	"net/http"
	"strings"
	"testing"
)

func TestDiffJSON(t *testing.T) {
	tests := []struct {
		name, a, b string
		changes    int
		want       []string
		absent     []string
	}{
		{"identical", anthropicBody, anthropicBody, 0, []string{"(no changes)"}, nil},
		{"added", `{"a":1}`, `{"a":1,"b":"x"}`, 1, []string{`+ b: "x"`}, nil},
		{"removed", `{"a":1,"b":[1,2]}`, `{"a":1}`, 1, []string{`- b: [1,2]`}, nil},
		{"changed", `{"a":1}`, `{"a":2}`, 1, []string{"~ a: 1 → 2"}, nil},
		{"nested", `{"usage":{"total_tokens":35,"x":{"y":1}}}`, `{"usage":{"total_tokens":40,"x":{"y":1}}}`, 1, []string{"~ usage.total_tokens: 35 → 40"}, []string{"usage.x"}},
		{"type change", `{"a":{"b":1}}`, `{"a":[1]}`, 1, []string{"~ a: {\"b\":1} → [1]"}, nil},
		{"null vs value", `{"a":null}`, `{"a":false}`, 1, []string{"~ a: null → false"}, nil},
		{"array element", `{"m":[{"role":"user"},{"role":"assistant"}]}`, `{"m":[{"role":"user"},{"role":"system"}]}`, 1, []string{`~ m.1.role: "assistant" → "system"`}, nil},
		{"array longer", `{"m":[1]}`, `{"m":[1,2,3]}`, 3, []string{"~ m: array length 1 → 3", "+ m.1: 2", "+ m.2: 3"}, nil},
		{"array shorter", `[1,2]`, `[1]`, 2, []string{"~ $: array length 2 → 1", "- 1: 2"}, nil},
		{"root scalar", `1`, `"x"`, 1, []string{`~ $: 1 → "x"`}, nil},
		{"root object to array", `{}`, `[]`, 1, []string{"~ $: {} → []"}, nil},
		{"number literal preserved", `{"n":1.10}`, `{"n":1.1}`, 1, []string{"~ n: 1.10 → 1.1"}, nil},
		{"big int preserved", `{"n":12345678901234567890}`, `{"n":12345678901234567891}`, 1, []string{"12345678901234567890 → 12345678901234567891"}, nil},
		{"key needs escaping", `{"a.b":1}`, `{"a.b":2}`, 1, []string{`~ a\.b: 1 → 2`}, nil},
		{"invalid json a", `{oops`, `{"a":1}`, 2, []string{"(not JSON; text diff)", "-{oops", `+{"a":1}`}, nil},
		{"invalid json b", `{"a":1}`, `{"a":1} trailing`, 2, []string{"(not JSON; text diff)"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, n := DiffJSON([]byte(tc.a), []byte(tc.b), 0)
			if n != tc.changes {
				t.Errorf("changes = %d, want %d\n%s", n, tc.changes, text)
			}
			wantContains(t, text, tc.want...)
			for _, a := range tc.absent {
				if strings.Contains(text, a) {
					t.Errorf("unexpected %q in:\n%s", a, text)
				}
			}
		})
	}
}

func TestDiffJSONLimits(t *testing.T) {
	var a, b strings.Builder
	a.WriteString("{")
	b.WriteString("{")
	for i := 0; i < 70; i++ {
		if i > 0 {
			a.WriteString(",")
			b.WriteString(",")
		}
		a.WriteString(`"k` + itoa(i) + `":` + itoa(i))
		b.WriteString(`"k` + itoa(i) + `":` + itoa(i+1))
	}
	a.WriteString("}")
	b.WriteString("}")
	text, n := DiffJSON([]byte(a.String()), []byte(b.String()), 0)
	if n != 70 {
		t.Errorf("changes = %d", n)
	}
	lines := strings.Split(text, "\n")
	if len(lines) != DefaultMaxChanges+1 || lines[len(lines)-1] != "… and 20 more" {
		t.Errorf("want %d lines + tail, got %d: %q", DefaultMaxChanges, len(lines), lines[len(lines)-1])
	}
	text, n = DiffJSON([]byte(a.String()), []byte(b.String()), 5)
	if n != 70 || !strings.HasSuffix(text, "… and 65 more") || len(strings.Split(text, "\n")) != 6 {
		t.Errorf("maxChanges=5: n=%d\n%s", n, text)
	}

	// Long values are cut to 120 chars.
	long := `{"v":"` + strings.Repeat("x", 300) + `"}`
	text, _ = DiffJSON([]byte(`{"v":"short"}`), []byte(long), 0)
	if !strings.Contains(text, strings.Repeat("x", 119)+"…") || strings.Contains(text, strings.Repeat("x", 121)) {
		t.Errorf("value not cut: %s", text)
	}
}

func TestDiffText(t *testing.T) {
	a := "a\nb\nc\nd\ne\nf\ng\nh\n"
	b := "a\nb\nX\nd\ne\nf\ng\nh\ni\n"
	text, n := DiffText(a, b, 0)
	if n != 3 {
		t.Errorf("changes = %d\n%s", n, text)
	}
	wantContains(t, text, "@@ -1,5 +1,5 @@\n a\n b\n-c\n+X\n d\n e", "@@ -7,2 +7,3 @@\n g\n h\n+i")

	if text, n := DiffText("same", "same", 0); n != 0 || text != "(no changes)" {
		t.Errorf("identical: %q %d", text, n)
	}
	if _, n := DiffText("", "x\ny", 0); n != 2 {
		t.Errorf("empty a: %d", n)
	}
	if _, n := DiffText("x\ny", "", 0); n != 2 {
		t.Errorf("empty b: %d", n)
	}
	// Trailing newline differences count as no line change.
	if _, n := DiffText("x\n", "x", 0); n != 0 {
		t.Errorf("trailing newline: %d", n)
	}

	// maxLines caps the rendered lines but not the count.
	var sa, sb strings.Builder
	for i := 0; i < 50; i++ {
		sa.WriteString("line " + itoa(i) + "\n")
		sb.WriteString("LINE " + itoa(i) + "\n")
	}
	text, n = DiffText(sa.String(), sb.String(), 10)
	if n != 100 {
		t.Errorf("changes = %d", n)
	}
	if !strings.HasSuffix(text, "… and 90 more lines") {
		t.Errorf("missing tail:\n%s", text)
	}
	if c := strings.Count(text, "\n-") + strings.Count(text, "\n+"); c != 10 {
		t.Errorf("rendered %d change lines, want 10", c)
	}

	// Large inputs take the block-replace path and still count correctly.
	big1 := strings.Repeat("a\n", 3000) + "mid1\n" + strings.Repeat("z\n", 10)
	big2 := strings.Repeat("a\n", 3000) + "mid2\nmid3\n" + strings.Repeat("z\n", 10)
	text, n = DiffText(big1, big2, 0)
	if n != 3 {
		t.Errorf("big: changes = %d\n%s", n, text)
	}
	wantContains(t, text, "-mid1", "+mid2", "+mid3")
	// Force the block fallback with distinct middles that exceed the cell cap.
	var m1, m2 strings.Builder
	for i := 0; i < 2500; i++ {
		m1.WriteString("p" + itoa(i) + "\n")
		m2.WriteString("q" + itoa(i) + "\n")
	}
	_, n = DiffText(m1.String(), m2.String(), 0)
	if n != 5000 {
		t.Errorf("fallback: changes = %d", n)
	}
}

func TestDiffHeaders(t *testing.T) {
	a := http.Header{}
	a.Set("Content-Type", "application/json")
	a.Set("Date", "Mon, 01 Jan 2024 00:00:00 GMT")
	a.Set("X-Request-Id", "abc")
	a.Set("Content-Length", "10")
	a.Set("Server", "nginx")
	a.Add("Vary", "Accept")
	a.Add("Vary", "Origin")

	b := http.Header{}
	b.Set("content-type", "application/json; charset=utf-8")
	b.Set("Date", "Tue, 02 Jan 2024 00:00:00 GMT")
	b.Set("X-Request-Id", "def")
	b.Set("Content-Length", "20")
	b.Set("Cache-Control", "no-store")
	b.Add("Vary", "Accept")
	b.Add("Vary", "Origin")

	text, n := DiffHeaders(a, b, nil)
	if n != 3 {
		t.Errorf("changes = %d\n%s", n, text)
	}
	want := "+ Cache-Control: no-store\n~ Content-Type: application/json → application/json; charset=utf-8\n- Server: nginx"
	if text != want {
		t.Errorf("got:\n%s\nwant:\n%s", text, want)
	}

	// Custom ignore list replaces the default.
	text, n = DiffHeaders(a, b, []string{"content-type", "server", "cache-control"})
	if n != 3 {
		t.Errorf("custom ignore: changes = %d\n%s", n, text)
	}
	wantContains(t, text, "~ Date:", "~ X-Request-Id: abc → def", "~ Content-Length: 10 → 20")

	// Empty non-nil ignore compares everything.
	if _, n := DiffHeaders(a, b, []string{}); n != 6 {
		t.Errorf("no ignore: changes = %d", n)
	}

	if text, n := DiffHeaders(a, a, nil); n != 0 || text != "(no header changes)" {
		t.Errorf("identical: %q %d", text, n)
	}
	if _, n := DiffHeaders(nil, nil, nil); n != 0 {
		t.Errorf("nil: %d", n)
	}
}
