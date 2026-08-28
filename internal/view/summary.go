package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// notableKeys are keys whose values a JSON summary always surfaces, in
// addition to any key ending in "_id".
var notableKeys = map[string]bool{
	"id": true, "status": true, "error": true, "message": true, "model": true,
	"count": true, "total": true, "usage": true, "stop_reason": true,
	"finish_reason": true,
}

const (
	summaryMaxKeys    = 40   // top-level keys listed
	summaryMaxNotable = 10   // nested notable values listed
	summaryMaxNodes   = 2000 // nodes visited while looking for notable values
	summaryMaxDepth   = 5
	sampleValueLen    = 30 // per-element preview inside array shapes
)

func isNotable(k string) bool {
	lk := strings.ToLower(k)
	return notableKeys[lk] || strings.HasSuffix(lk, "_id")
}

// capText cuts s to at most n bytes (rune-safe) with a trailing ellipsis.
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutRunes(s, n) + "…"
}

// summaryView is the "summary" mode dispatcher. Values under sensitive
// keys are masked through m at render time.
func summaryView(kind string, content []byte, opts Options, m *masker) string {
	var s string
	switch kind {
	case kindJSON:
		s = summarizeJSON(content, opts, m)
	case kindHTML:
		s = summarizeHTML(content)
	case kindSSE:
		s = summarizeSSE(content, opts)
	case kindForm:
		s = summarizeForm(content, opts, m)
	default:
		s = summarizeText(kind, content)
	}
	return capText(s, summaryBudget)
}

// typeWord names the JSON type of a gjson value.
func typeWord(v gjson.Result) string {
	switch {
	case v.IsObject():
		return "object"
	case v.IsArray():
		return "array"
	case v.Type == gjson.String:
		return "str"
	case v.Type == gjson.Number:
		return numKind(v.Raw)
	case v.Type == gjson.True || v.Type == gjson.False:
		return "bool"
	}
	return "null"
}

// numKind distinguishes integer from floating-point literals.
func numKind(raw string) string {
	if strings.ContainsAny(raw, ".eE") {
		return "float"
	}
	return "int"
}

// countElems counts the members of an object or array (bounded).
func countElems(v gjson.Result) int {
	n := 0
	v.ForEach(func(_, _ gjson.Result) bool {
		n++
		return n < 1_000_000
	})
	return n
}

// quoteText quotes s for display, escaping control characters but keeping
// non-ASCII text readable.
func quoteText(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// shortString renders a string value, eliding it when longer than limit.
func shortString(s string, limit int) string {
	n := utf8.RuneCountInString(s)
	if n <= limit {
		return quoteText(s)
	}
	return quoteText(cutRunes(s, shortValueLen)) + fmt.Sprintf("…(%d chars)", n)
}

// compactJSON removes insignificant whitespace and cuts the result.
func compactJSON(raw string, limit int) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err == nil {
		raw = buf.String()
	}
	if len(raw) > limit {
		return cutRunes(raw, limit) + "…"
	}
	return raw
}

// scalarText renders a scalar for notable lines.
func scalarText(v gjson.Result, opts Options) string {
	if v.Type == gjson.String {
		return shortString(v.String(), opts.StringTruncate)
	}
	return v.Raw
}

// describeMasked renders a summary line for a value under a sensitive key:
// scalars are masked, containers show only their size.
func describeMasked(v gjson.Result, m *masker) string {
	switch {
	case v.IsObject() || v.IsArray():
		m.n++
		return fmt.Sprintf("%s[%d] (redacted)", typeWord(v), countElems(v))
	case v.Type == gjson.String:
		m.n++
		return "str " + quoteText(Mask(v.String()))
	}
	m.n++
	return typeWord(v) + " " + Mask(v.Raw)
}

// describeValue renders one summary line for a JSON value.
func describeValue(v gjson.Result, opts Options) string {
	switch {
	case v.Type == gjson.String:
		return "str " + shortString(v.String(), opts.StringTruncate)
	case v.Type == gjson.Number:
		return numKind(v.Raw) + " " + v.Raw
	case v.Type == gjson.True || v.Type == gjson.False:
		return "bool " + v.Raw
	case v.IsArray():
		n := countElems(v)
		if n == 0 {
			return "array[0]"
		}
		return fmt.Sprintf("array[%d] of %s", n, arrayShape(v, opts))
	case v.IsObject():
		var keys []string
		n := 0
		v.ForEach(func(k, _ gjson.Result) bool {
			n++
			if len(keys) < 8 {
				keys = append(keys, k.String())
			}
			return true
		})
		if n == 0 {
			return "object{}"
		}
		list := strings.Join(keys, ", ")
		if n > len(keys) {
			list += ", …"
		}
		return fmt.Sprintf("object{%d: %s}", n, capText(list, 70))
	}
	return "null"
}

// arrayShape describes the elements of an array: the set of element types
// and, for objects, the union of keys seen in the first ArraySample
// elements, or a preview of the first values for scalars.
func arrayShape(v gjson.Result, opts Options) string {
	kinds := map[string]bool{}
	var kindOrder []string
	var keys []string
	seenKey := map[string]bool{}
	var samples []string
	i := 0
	v.ForEach(func(_, e gjson.Result) bool {
		t := typeWord(e)
		if !kinds[t] {
			kinds[t] = true
			kindOrder = append(kindOrder, t)
		}
		if i < opts.ArraySample {
			switch {
			case e.IsObject():
				e.ForEach(func(k, _ gjson.Result) bool {
					if !seenKey[k.String()] {
						seenKey[k.String()] = true
						keys = append(keys, k.String())
					}
					return len(keys) < 12
				})
			case e.Type == gjson.String:
				samples = append(samples, shortString(e.String(), sampleValueLen))
			case !e.IsArray():
				samples = append(samples, e.Raw)
			}
		}
		i++
		return i < 1000
	})
	if len(kinds) == 1 && kinds["object"] {
		return "object{" + capText(strings.Join(keys, ", "), 70) + "}"
	}
	if len(kinds) == 1 && len(samples) > 0 {
		s := kindOrder[0] + " [" + strings.Join(samples, ", ")
		if countElems(v) > len(samples) {
			s += ", …"
		}
		return s + "]"
	}
	return strings.Join(kindOrder, "|")
}

// summarizeJSON digests a JSON document.
func summarizeJSON(content []byte, opts Options, m *masker) string {
	r := gjson.ParseBytes(content)
	var sb strings.Builder
	switch {
	case r.IsObject():
		n := countElems(r)
		fmt.Fprintf(&sb, "json object (%d keys)\n", n)
		shown := 0
		r.ForEach(func(k, v gjson.Result) bool {
			if shown >= summaryMaxKeys {
				fmt.Fprintf(&sb, "… %d more keys\n", n-shown)
				return false
			}
			shown++
			key := k.String()
			sb.WriteString(key)
			sb.WriteString(": ")
			switch {
			case m.on && isSensitiveKey(key):
				sb.WriteString(describeMasked(v, m))
			case isNotable(key) && (v.IsObject() || v.IsArray()):
				// Notable containers (usage, error, …) show their value
				// instead of a key list.
				fmt.Fprintf(&sb, "%s[%d] = %s", typeWord(v), countElems(v), compactJSON(v.Raw, compactJSONLen))
			default:
				sb.WriteString(describeValue(v, opts))
			}
			sb.WriteByte('\n')
			return true
		})
		if notable := collectNotable(r, opts, m); len(notable) > 0 {
			sb.WriteString("notable:\n")
			for _, l := range notable {
				sb.WriteString("  ")
				sb.WriteString(l)
				sb.WriteByte('\n')
			}
		}
	case r.IsArray():
		n := countElems(r)
		if n == 0 {
			sb.WriteString("json array[0]\n")
			break
		}
		fmt.Fprintf(&sb, "json array[%d] of %s\n", n, arrayShape(r, opts))
		i := 0
		r.ForEach(func(_, e gjson.Result) bool {
			if i >= opts.ArraySample {
				return false
			}
			fmt.Fprintf(&sb, "  [%d]: %s\n", i, compactJSON(e.Raw, compactJSONLen))
			i++
			return true
		})
		if n > i {
			fmt.Fprintf(&sb, "  … %d more\n", n-i)
		}
	default:
		sb.WriteString(describeValue(r, opts))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// collectNotable walks nested values and returns "path: value" lines for
// keys that carry status, ids, usage and similar diagnostic information.
// Top-level keys are skipped because the summary already lists them, and
// sensitive subtrees are not entered while masking is on.
func collectNotable(root gjson.Result, opts Options, m *masker) []string {
	var lines []string
	nodes := 0
	var walk func(path string, v gjson.Result, depth int)
	walk = func(path string, v gjson.Result, depth int) {
		if depth > summaryMaxDepth || len(lines) >= summaryMaxNotable {
			return
		}
		switch {
		case v.IsObject():
			v.ForEach(func(k, val gjson.Result) bool {
				nodes++
				if nodes > summaryMaxNodes || len(lines) >= summaryMaxNotable {
					return false
				}
				key := k.String()
				if m.on && isSensitiveKey(key) {
					return true
				}
				p := childPath(path, escapeKey(key))
				if depth > 0 && isNotable(key) {
					if val.IsObject() || val.IsArray() {
						lines = append(lines, p+": "+compactJSON(val.Raw, compactJSONLen))
					} else {
						lines = append(lines, p+": "+scalarText(val, opts))
					}
				}
				walk(p, val, depth+1)
				return true
			})
		case v.IsArray():
			i := 0
			v.ForEach(func(_, val gjson.Result) bool {
				if i >= opts.ArraySample || nodes > summaryMaxNodes {
					return false
				}
				nodes++
				walk(childPath(path, strconv.Itoa(i)), val, depth+1)
				i++
				return true
			})
		}
	}
	walk("$", root, 0)
	return lines
}

var (
	reTitle     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reScript    = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces    = regexp.MustCompile(`\s+`)
	reXMLRoot   = regexp.MustCompile(`(?s)<([A-Za-z_][\w.:-]*)[\s>/]`)
	reXMLPrefix = regexp.MustCompile(`(?s)^(\s*<\?[^>]*\?>|\s*<![^>]*>|\s*<!--.*?-->)*`)
)

func collapseSpace(s string) string {
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// summarizeHTML shows the title and the start of the visible text.
func summarizeHTML(content []byte) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "html (%d chars)\n", utf8.RuneCount(content))
	if m := reTitle.FindSubmatch(content); m != nil {
		fmt.Fprintf(&sb, "title: %s\n", collapseSpace(html.UnescapeString(string(m[1]))))
	}
	text := reScript.ReplaceAll(content, nil)
	text = reTag.ReplaceAll(text, []byte(" "))
	visible := collapseSpace(html.UnescapeString(string(text)))
	if visible != "" {
		sb.WriteString("text: ")
		sb.WriteString(preview(visible))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// preview returns the first textPreviewLen characters of s.
func preview(s string) string {
	if utf8.RuneCountInString(s) <= textPreviewLen {
		return s
	}
	r := []rune(s)
	return string(r[:textPreviewLen]) + "…"
}

// summarizeSSE digests a text/event-stream body.
func summarizeSSE(content []byte, opts Options) string {
	events := parseSSE(content)
	var sb strings.Builder
	fmt.Fprintf(&sb, "sse: %d events (%d bytes)\n", len(events), len(content))
	if len(events) == 0 {
		return strings.TrimRight(sb.String(), "\n")
	}
	order, counts := eventNameCounts(events)
	parts := make([]string, 0, len(order))
	for i, name := range order {
		if i >= 20 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", name, counts[name]))
	}
	fmt.Fprintf(&sb, "events: %s\n", strings.Join(parts, ", "))
	first, last := events[0], events[len(events)-1]
	fmt.Fprintf(&sb, "first: %s %s\n", first.name, capText(first.data, opts.StringTruncate))
	if len(events) > 1 {
		fmt.Fprintf(&sb, "last: %s %s\n", last.name, capText(last.data, opts.StringTruncate))
	}
	if isLLMStream(events) {
		sb.WriteString("next: pano_flow_explain (looks like an LLM stream)\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// summarizeForm lists form fields with values cut to StringTruncate.
func summarizeForm(content []byte, opts Options, m *masker) string {
	pairs := formPairs(content)
	var sb strings.Builder
	fmt.Fprintf(&sb, "form (%d fields)\n", len(pairs))
	for i, kv := range pairs {
		if i >= summaryMaxKeys {
			fmt.Fprintf(&sb, "… %d more fields\n", len(pairs)-i)
			break
		}
		v, masked := m.value(kv[0], kv[1])
		if !masked {
			v = capText(v, opts.StringTruncate)
		}
		sb.WriteString(kv[0])
		sb.WriteString(": ")
		sb.WriteString(v)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// summarizeText shows size and the first characters of any other text.
func summarizeText(kind string, content []byte) string {
	var sb strings.Builder
	lines := bytes.Count(content, []byte{'\n'})
	if len(content) > 0 && content[len(content)-1] != '\n' {
		lines++
	}
	fmt.Fprintf(&sb, "%s (%d chars, %d lines)\n", kind, utf8.RuneCount(content), lines)
	if kind == kindXML {
		rest := reXMLPrefix.ReplaceAll(content, nil)
		if m := reXMLRoot.FindSubmatch(rest); m != nil {
			fmt.Fprintf(&sb, "root: <%s>\n", m[1])
		}
	}
	sb.WriteString(preview(collapseSpace(string(cutHead(content, 4*textPreviewLen)))))
	return strings.TrimRight(sb.String(), "\n")
}
