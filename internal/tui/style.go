package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/orron/pano/internal/api"
)

// Body stylers. The daemon returns plain text; these add colour without
// changing a single character, so the TUI never disagrees with the CLI/MCP
// renderings. Every styler is line-oriented and tolerant: unknown lines fall
// back to key/value colouring.

// styleBody picks the styler for the current pane and view.
func (m *Model) styleBody(s string) string {
	if m.pane == paneExplain {
		return m.styleExplainText(s)
	}
	return m.styleDetailText(s)
}

var reNumber = regexp.MustCompile(`^[0-9][0-9,]*(\.[0-9]+)?(k|M|G|B|kB|MB|GB|ms|µs|ns|s|m|h|%)?$`)

// ---- detail (summary / schema / pretty / raw / diff) ----

// styleDetailText colours the plain text produced by the daemon's renderers.
func (m *Model) styleDetailText(s string) string {
	t := m.theme()
	s = sanitize(s)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	schema := m.pane == paneBody && m.detailQ.View == api.ViewSchema
	inJSON, inBody := false, false
	for i, l := range lines {
		switch {
		case i == 0 && m.pane == paneBody:
			// first line is the flow summary line: dim it, it's in the head already
			out = append(out, t.faint().Render(l))
		case strings.HasPrefix(l, "== ") && strings.HasSuffix(l, " =="):
			name := strings.TrimSuffix(strings.TrimPrefix(l, "== "), " ==")
			out = append(out, t.fg(t.LineFaint).Render("── ")+t.secondary().Bold(true).Render(strings.ToUpper(name))+t.fg(t.LineFaint).Render(" "+strings.Repeat(glyphRule, max(0, 40-len(name)))))
			inJSON, inBody = false, false
		case strings.HasPrefix(l, "body: "):
			out = append(out, m.styleBodyLine(l))
			inJSON, inBody = false, true
		case strings.HasPrefix(l, "+ "):
			out = append(out, t.fg(t.OK).Render(l))
		case strings.HasPrefix(l, "- "):
			out = append(out, t.fg(t.Err).Render(l))
		case strings.HasPrefix(l, "~ "):
			out = append(out, t.fg(t.Warn).Render(l))
		case strings.HasPrefix(l, "error"):
			out = append(out, t.fg(t.Err).Render(l))
		case schema && inBody:
			out = append(out, m.styleSchemaLine(l))
		case strings.HasPrefix(l, "{") || strings.HasPrefix(l, "[") || inJSON:
			inJSON = true
			out = append(out, m.styleJSONLine(l))
			if l == "}" || l == "]" {
				inJSON = false
			}
		case strings.HasPrefix(l, "json object") || strings.HasPrefix(l, "json array") || strings.HasPrefix(l, "sse: ") ||
			strings.HasPrefix(l, "html (") || strings.HasPrefix(l, "form (") || strings.HasPrefix(l, "text (") || strings.HasPrefix(l, "xml ("):
			out = append(out, m.styleBodyKind(l))
		case strings.HasPrefix(l, "…") || strings.HasPrefix(l, "  …"):
			out = append(out, t.faint().Render(l))
		default:
			out = append(out, m.styleKVLine(l))
		}
	}
	return strings.Join(out, "\n")
}

// styleBodyLine colours "body: mime size sha256:…" and its truncation note.
func (m *Model) styleBodyLine(l string) string {
	t := m.theme()
	rest := strings.TrimPrefix(l, "body: ")
	var b strings.Builder
	b.WriteString(t.faint().Render("body: "))
	for i, tok := range strings.Split(rest, " ") {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch {
		case strings.HasPrefix(tok, "sha256:") || strings.HasPrefix(tok, "["):
			b.WriteString(t.faint().Render(tok))
		case reNumber.MatchString(tok):
			b.WriteString(t.muted().Render(tok))
		default:
			b.WriteString(t.muted().Render(tok))
		}
	}
	return b.String()
}

// styleBodyKind colours the digest's first line ("json object (5 keys)").
func (m *Model) styleBodyKind(l string) string {
	t := m.theme()
	if i := strings.Index(l, " ("); i >= 0 {
		return t.secondary().Render(l[:i]) + " " + t.muted().Render(l[i+1:])
	}
	if k, v, ok := strings.Cut(l, ": "); ok {
		return t.secondary().Render(k+":") + " " + m.styleWords(v, t.primary())
	}
	return t.secondary().Render(l)
}

// styleKVLine colours "Key: value" and "key: type value" lines.
func (m *Model) styleKVLine(l string) string {
	t := m.theme()
	trim := strings.TrimLeft(l, " ")
	indent := l[:len(l)-len(trim)]
	k, v, ok := strings.Cut(trim, ": ")
	if !ok && strings.HasSuffix(trim, ":") && !strings.ContainsAny(trim, " \"{}") {
		// bare section label, e.g. "notable:"
		return indent + t.secondary().Bold(true).Render(trim)
	}
	if !ok || strings.ContainsAny(k, " \"{}") {
		if strings.HasPrefix(trim, "[") { // explain final items: [text] "…"
			return indent + m.styleExplainItem(trim)
		}
		return indent + m.styleValue(trim)
	}
	if i := strings.Index(v, " hash:"); i >= 0 {
		return indent + t.secondary().Render(k+":") + " " + t.muted().Render(v[:i]) + t.faint().Render(v[i:]) + t.faint().Render("  redacted")
	}
	if strings.Contains(v, "hash:") {
		return indent + t.secondary().Render(k+":") + " " + t.muted().Render(v)
	}
	// summary type words: "str "gpt-5"", "int 8192", "array[14] of object{…}"
	for _, typ := range []string{"str ", "int ", "float ", "bool "} {
		if strings.HasPrefix(v, typ) {
			return indent + t.secondary().Render(k+":") + " " + t.faint().Render(typ) + m.styleValue(v[len(typ):])
		}
	}
	if v == "null" {
		return indent + t.secondary().Render(k+":") + " " + t.fg(t.SynBool).Render(v)
	}
	if strings.HasPrefix(v, "array[") || strings.HasPrefix(v, "object{") || strings.HasPrefix(v, "object[") {
		return indent + t.secondary().Render(k+":") + " " + m.styleSchemaLine(v)
	}
	return indent + t.secondary().Render(k+":") + " " + m.styleValue(v)
}

// styleValue colours a bare value: quoted strings, numbers, booleans, and
// otherwise words with numbers highlighted and parentheses muted.
func (m *Model) styleValue(v string) string {
	t := m.theme()
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, "\""):
		// quoted, possibly followed by "…(n chars)" or " (n chars)"
		end := strings.LastIndex(v, "\"")
		if end > 0 && end < len(v)-1 {
			return t.fg(t.SynStr).Render(v[:end+1]) + t.muted().Render(v[end+1:])
		}
		return t.fg(t.SynStr).Render(v)
	case v == "true" || v == "false" || v == "null":
		return t.fg(t.SynBool).Render(v)
	case v == "-" || v == "(empty)" || strings.HasPrefix(v, "(no ") || strings.HasPrefix(v, "(not "):
		return t.muted().Render(v)
	case reNumber.MatchString(v):
		return t.fg(t.SynNum).Render(v)
	}
	return m.styleWords(v, t.primary())
}

// styleWords colours numbers inside a phrase and mutes parenthesised parts.
func (m *Model) styleWords(v string, base interface{ Render(...string) string }) string {
	t := m.theme()
	var b strings.Builder
	depth := 0
	for i, tok := range strings.Split(v, " ") {
		if i > 0 {
			b.WriteByte(' ')
		}
		open := strings.Count(tok, "(")
		closeN := strings.Count(tok, ")")
		switch {
		case depth > 0 || open > 0:
			b.WriteString(t.muted().Render(tok))
		case reNumber.MatchString(strings.TrimRight(tok, ",;")):
			b.WriteString(t.fg(t.SynNum).Render(tok))
		default:
			b.WriteString(base.Render(tok))
		}
		depth += open - closeN
		if depth < 0 {
			depth = 0
		}
	}
	return b.String()
}

// styleJSONLine is a tiny tokenizer for pretty-printed JSON.
func (m *Model) styleJSONLine(l string) string {
	t := m.theme()
	var b strings.Builder
	i := 0
	for i < len(l) {
		c := l[i]
		switch c {
		case '"':
			j := i + 1
			for j < len(l) {
				if l[j] == '\\' {
					j += 2
					continue
				}
				if l[j] == '"' {
					break
				}
				j++
			}
			if j >= len(l) {
				j = len(l) - 1
			}
			tok := l[i : j+1]
			// key if followed by ':'
			k := j + 1
			for k < len(l) && l[k] == ' ' {
				k++
			}
			switch {
			case k < len(l) && l[k] == ':':
				b.WriteString(t.secondary().Render(tok))
			case strings.Contains(tok, "hash:"):
				b.WriteString(t.muted().Render(tok))
			default:
				b.WriteString(t.fg(t.SynStr).Render(tok))
			}
			i = j + 1
		case '{', '}', '[', ']', ',', ':':
			b.WriteString(t.faint().Render(string(c)))
			i++
		case ' ':
			b.WriteByte(' ')
			i++
		default:
			j := i
			for j < len(l) && l[j] != ',' && l[j] != '}' && l[j] != ']' && l[j] != ' ' {
				j++
			}
			tok := l[i:j]
			switch {
			case tok == "true" || tok == "false" || tok == "null":
				b.WriteString(t.fg(t.SynBool).Render(tok))
			case strings.HasPrefix(tok, "…"):
				b.WriteString(t.faint().Render(tok))
			default:
				b.WriteString(t.fg(t.SynNum).Render(tok))
			}
			i = j
		}
	}
	return b.String()
}

// styleSchemaLine colours the inferred-schema notation:
// { id: str<uuid>, role: "user"|"assistant", n?: int, tags: [ str ] }.
func (m *Model) styleSchemaLine(l string) string {
	t := m.theme()
	var b strings.Builder
	i := 0
	for i < len(l) {
		c := l[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(l) && l[j] != '"' {
				if l[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(l) {
				j = len(l) - 1
			}
			b.WriteString(t.fg(t.SynStr).Render(l[i : j+1]))
			i = j + 1
		case c == ' ':
			b.WriteByte(' ')
			i++
		case isWordStart(c):
			j := i
			for j < len(l) && isWordChar(l[j]) {
				j++
			}
			// str<uuid> / object{…} / array[…]
			if j < len(l) && l[j] == '<' {
				if k := strings.IndexByte(l[j:], '>'); k >= 0 {
					j += k + 1
				}
			}
			word := l[i:j]
			opt := j < len(l) && l[j] == '?'
			k := j
			if opt {
				k++
			}
			switch {
			case k < len(l) && l[k] == ':':
				b.WriteString(t.secondary().Render(word))
				if opt {
					b.WriteString(t.fg(t.Warn).Render("?"))
				}
				i = k
			default:
				b.WriteString(m.styleTypeWord(word))
				i = j
			}
		default:
			// punctuation: { } [ ] , | … ? : ( )
			b.WriteString(t.faint().Render(string(l[i])))
			i++
		}
	}
	return b.String()
}

func isWordStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isWordChar(c byte) bool { return isWordStart(c) || c == '-' || c == '.' || c == '/' }

// styleTypeWord colours a schema type word.
func (m *Model) styleTypeWord(w string) string {
	t := m.theme()
	switch {
	case w == "str" || strings.HasPrefix(w, "str<"):
		return t.fg(t.SynStr).Render(w)
	case w == "int" || w == "float":
		return t.fg(t.SynNum).Render(w)
	case w == "bool" || w == "null":
		return t.fg(t.SynBool).Render(w)
	case w == "array" || w == "object" || w == "of":
		return t.muted().Render(w)
	case reNumber.MatchString(w):
		return t.fg(t.SynNum).Render(w)
	}
	return t.primary().Render(w)
}

// ---- explain ----

var explainKeys = map[string]bool{
	"provider": true, "request": true, "usage": true, "final": true, "errors": true,
	"messages": true, "system": true, "request_json": true, "stop": true,
}

// styleExplainText colours the LLM digest. Top-level "key:" lines start a
// section; indented lines (items, wrapped continuations) are coloured
// according to the section they belong to.
func (m *Model) styleExplainText(s string) string {
	t := m.theme()
	s = sanitize(s)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	section := ""
	for _, l := range lines {
		trim := strings.TrimLeft(l, " ")
		indent := l[:len(l)-len(trim)]
		if indent == "" {
			key, rest, ok := strings.Cut(l, ":")
			if ok && explainKeys[key] {
				section = key
				out = append(out, m.styleExplainHead(key, strings.TrimPrefix(rest, " ")))
				continue
			}
			if strings.HasSuffix(l, truncMarker) {
				out = append(out, t.primary().Render(strings.TrimSuffix(l, truncMarker))+t.faint().Render(truncMarker))
				continue
			}
			out = append(out, m.styleKVLine(l))
			continue
		}
		out = append(out, indent+m.styleExplainBody(section, trim))
	}
	return strings.Join(out, "\n")
}

const truncMarker = "…[truncated]"

func (m *Model) styleExplainLabel(key string) string {
	return m.theme().secondary().Bold(true).Render(key + ":")
}

// styleExplainHead renders a top-level "key: rest" digest line.
func (m *Model) styleExplainHead(key, rest string) string {
	t := m.theme()
	label := m.styleExplainLabel(key)
	if rest == "" {
		return label
	}
	switch key {
	case "provider":
		// provider: X  model: Y  stream: Z  status: N
		var b strings.Builder
		for i, f := range strings.Split(rest, "  ") {
			if i > 0 {
				b.WriteString("  ")
			}
			k, v, ok := strings.Cut(f, ": ")
			if !ok {
				b.WriteString(t.fg(t.LLM).Bold(true).Render(f))
				continue
			}
			b.WriteString(t.secondary().Render(k + ":"))
			b.WriteByte(' ')
			switch k {
			case "model":
				b.WriteString(t.primary().Bold(true).Render(v))
			case "stream":
				switch {
				case strings.Contains(v, "INCOMPLETE"):
					b.WriteString(t.fg(t.Warn).Render(v))
				case v == "yes":
					b.WriteString(t.accent().Render(v))
				default:
					b.WriteString(t.muted().Render(v))
				}
			case "status":
				code := 0
				for _, c := range v {
					if c < '0' || c > '9' {
						break
					}
					code = code*10 + int(c-'0')
				}
				b.WriteString(t.fg(t.status(code, "")).Bold(true).Render(v))
			default:
				b.WriteString(t.primary().Render(v))
			}
		}
		return label + " " + b.String()
	case "request", "usage":
		return label + " " + m.styleSegments(key, rest)
	case "stop":
		return label + " " + m.styleStopReason(rest)
	case "errors":
		if rest == "none" {
			return label + " " + t.muted().Render(rest)
		}
		return label + " " + t.fg(t.Err).Render(rest)
	case "final":
		return label + " " + t.muted().Render(rest)
	}
	return label + " " + m.styleValue(rest)
}

// styleSegments colours " · "-separated request/usage segments.
func (m *Model) styleSegments(section, s string) string {
	t := m.theme()
	if s == "-" || strings.HasPrefix(s, "(") {
		return t.muted().Render(s)
	}
	segs := strings.Split(s, " · ")
	var b strings.Builder
	for i, seg := range segs {
		if i > 0 {
			b.WriteString(t.faint().Render(" · "))
		}
		if k, v, ok := strings.Cut(seg, ": "); ok && k == "stop" {
			b.WriteString(t.secondary().Render("stop:") + " " + m.styleStopReason(v))
			continue
		}
		if section == "usage" {
			b.WriteString(m.styleWords(seg, t.muted()))
			continue
		}
		b.WriteString(m.styleWords(seg, t.primary()))
	}
	return b.String()
}

// styleStopReason colours a stop/finish reason by what it means.
func (m *Model) styleStopReason(v string) string {
	t := m.theme()
	switch strings.ToLower(v) {
	case "end_turn", "stop", "completed", "done":
		return t.fg(t.OK).Render(v)
	case "tool_use", "tool_calls", "function_call", "tool_call":
		return t.fg(t.LLM).Render(v)
	case "max_tokens", "length", "incomplete", "max_output_tokens":
		return t.fg(t.Warn).Render(v)
	case "refusal", "content_filter", "safety", "recitation", "error":
		return t.fg(t.Err).Render(v)
	}
	return t.primary().Render(v)
}

// styleExplainBody renders an indented digest line under the given section.
func (m *Model) styleExplainBody(section, trim string) string {
	t := m.theme()
	switch section {
	case "final":
		switch {
		case strings.HasPrefix(trim, "["):
			return m.styleExplainItem(trim)
		case strings.HasSuffix(trim, ":"):
			// group label: "choice 0 (finish: stop):"
			return m.styleWords(strings.TrimSuffix(trim, ":"), t.secondary()) + t.faint().Render(":")
		}
		return t.primary().Render(trim)
	case "errors":
		return t.fg(t.Err).Render(trim)
	case "messages":
		if role, text, ok := strings.Cut(trim, ": "); ok && len(role) <= 12 && !strings.ContainsAny(role, " \"") {
			return m.styleRole(role) + t.faint().Render(":") + " " + m.styleValue(text)
		}
		return t.primary().Render(trim)
	case "system":
		return t.secondary().Render(trim)
	case "request_json":
		return m.styleJSONLine(trim)
	case "request", "usage":
		return m.styleSegments(section, trim)
	}
	return m.styleKVLine(trim)
}

// styleRole colours a chat role.
func (m *Model) styleRole(role string) string {
	t := m.theme()
	switch strings.ToLower(role) {
	case "user", "u", "human":
		return t.accent().Render(role)
	case "assistant", "a", "model":
		return t.fg(t.LLM).Render(role)
	case "system", "s", "developer":
		return t.fg(t.Warn).Render(role)
	case "tool", "t", "function":
		return t.fg(t.OK).Render(role)
	}
	return t.secondary().Render(role)
}

// styleExplainItem colours one "[kind] …" line of the final response.
func (m *Model) styleExplainItem(trim string) string {
	t := m.theme()
	kind, rest, _ := strings.Cut(trim[1:], "]")
	kc := t.FgSecondary
	switch kind {
	case "text":
		kc = t.OK
	case "thinking", "reasoning":
		kc = t.LLM
	case "tool_use", "tool_call", "function_call", "tool_result":
		kc = t.Warn
	case "refusal":
		kc = t.Err
	case "executable_code", "code_execution_result":
		kc = t.Redirect
	}
	head := t.fg(kc).Bold(true).Render("[" + kind + "]")
	rest = strings.TrimPrefix(rest, " ")
	if rest == "" {
		return head
	}
	// trailing "(n chars)" / "(… hidden …)" note
	note := ""
	if strings.HasSuffix(rest, ")") {
		cut := -1
		if strings.HasPrefix(rest, "\"") {
			if i := strings.LastIndex(rest, "\" ("); i >= 0 {
				cut = i + 1
			}
		} else if i := strings.LastIndex(rest, " ("); i >= 0 {
			cut = i
		}
		if cut > 0 {
			note, rest = rest[cut:], rest[:cut]
		}
	}
	var body string
	switch {
	case rest == "":
	case strings.HasPrefix(rest, "\""):
		body = t.primary().Render(rest)
	case strings.HasPrefix(rest, "("):
		note, body = rest, ""
	default:
		// "Name {json…}" or "Name note"
		name, args, _ := strings.Cut(rest, " ")
		body = t.primary().Bold(true).Render(name)
		if args != "" {
			if strings.HasPrefix(args, "{") || strings.HasPrefix(args, "[") {
				body += " " + m.styleJSONLine(args)
			} else {
				body += " " + t.muted().Render(args)
			}
		}
	}
	out := head
	if body != "" {
		out += " " + body
	}
	if note != "" {
		out += " " + t.muted().Render(strings.TrimLeft(note, " "))
	}
	return out
}

var _ = ansi.Strip
