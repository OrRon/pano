package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// cutInfo describes how much of a body a view showed.
type cutInfo struct {
	shown, total int
	truncated    bool
}

// cutHead returns at most n leading bytes of b without splitting a rune.
func cutHead(b []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	if len(b) <= n {
		return b
	}
	for i := 0; i < utf8.UTFMax && n > 0; i++ {
		if utf8.RuneStart(b[n]) {
			break
		}
		n--
	}
	return b[:n]
}

// cutTail returns at most n trailing bytes of b without splitting a rune.
func cutTail(b []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	if len(b) <= n {
		return b
	}
	start := len(b) - n
	for i := 0; i < utf8.UTFMax && start < len(b); i++ {
		if utf8.RuneStart(b[start]) {
			break
		}
		start++
	}
	return b[start:]
}

// cutRunes returns at most n leading bytes of s at a rune boundary.
func cutRunes(s string, n int) string {
	return string(cutHead([]byte(s), n))
}

// headTail keeps the first 75% and last 25% of budget bytes of b with a
// skip marker between them.
func headTail(b []byte, budget int) (string, cutInfo) {
	if len(b) <= budget {
		return string(b), cutInfo{shown: len(b), total: len(b)}
	}
	headN := budget * 3 / 4
	head := cutHead(b, headN)
	tail := cutTail(b, budget-headN)
	skipped := len(b) - len(head) - len(tail)
	text := string(head) + fmt.Sprintf("\n… [skipped %d bytes] …\n", skipped) + string(tail)
	return text, cutInfo{shown: len(head) + len(tail), total: len(b), truncated: true}
}

// headOnly keeps the first budget bytes of b with a trailing marker.
func headOnly(b []byte, budget int) (string, cutInfo) {
	if len(b) <= budget {
		return string(b), cutInfo{shown: len(b), total: len(b)}
	}
	head := cutHead(b, budget)
	text := string(head) + fmt.Sprintf("\n… [truncated; %d more bytes] …", len(b)-len(head))
	return text, cutInfo{shown: len(head), total: len(b), truncated: true}
}

// prettyJSON indents valid JSON with two spaces, preserving key order.
// Invalid input is returned unchanged.
func prettyJSON(b []byte) []byte {
	var buf bytes.Buffer
	buf.Grow(len(b) + len(b)/4)
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return b
	}
	return buf.Bytes()
}

// formPairs decodes application/x-www-form-urlencoded into ordered
// key/value pairs.
func formPairs(b []byte) [][2]string {
	var out [][2]string
	for _, kv := range strings.Split(string(b), "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		if uk, err := url.QueryUnescape(k); err == nil {
			k = uk
		}
		if uv, err := url.QueryUnescape(v); err == nil {
			v = uv
		}
		out = append(out, [2]string{k, v})
	}
	return out
}

// formLines renders a form as "key: value" lines, preserving field order.
// Values of sensitive keys are masked through m.
func formLines(b []byte, m *masker) string {
	var sb strings.Builder
	for _, kv := range formPairs(b) {
		v, _ := m.value(kv[0], kv[1])
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(kv[0])
		sb.WriteString(": ")
		sb.WriteString(v)
	}
	return sb.String()
}

// prettyBytes returns the human-friendly form of content for a kind.
func prettyBytes(kind string, content []byte, m *masker) []byte {
	switch kind {
	case kindJSON:
		return prettyJSON(content)
	case kindForm:
		return []byte(formLines(content, m))
	}
	return content
}

// truncatedView is the "truncated" mode: head + tail of the body. JSON is
// pretty-printed first when the pretty form is within 4× the budget so the
// head is readable.
func truncatedView(kind string, content []byte, opts Options) (string, cutInfo) {
	if kind == kindJSON {
		if p := prettyJSON(content); len(p) <= 4*opts.MaxBytes {
			content = p
		}
	}
	return headTail(content, opts.MaxBytes)
}

// prettyView is the "pretty" mode, falling back to head+tail truncation.
func prettyView(kind string, content []byte, opts Options, m *masker) (string, cutInfo) {
	return headTail(prettyBytes(kind, content, m), opts.MaxBytes)
}

// rawView is the "raw" mode: decoded bytes as-is up to the budget.
func rawView(content []byte, opts Options) (string, cutInfo) {
	return headOnly(content, opts.MaxBytes)
}
