package explain

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// reqSummary is what the digest's request line is built from.
type reqSummary struct {
	OK        bool // the request body was a JSON object
	Bytes     int
	System    string
	Messages  int
	Roles     map[string]int // u, a, s, t
	ToolNames []string
	Tools     int
	Params    []string // "max_tokens 8,192", "temperature 0.2", ...
	Lines     []string // "role: text", one per message
}

func (rs *reqSummary) addMessage(role, text string) {
	rs.Messages++
	switch role {
	case "user":
		rs.Roles["u"]++
	case "assistant", "model":
		rs.Roles["a"]++
	case "system", "developer":
		rs.Roles["s"]++
	case "tool", "function":
		rs.Roles["t"]++
	}
	if role == "" {
		role = "?"
	}
	rs.Lines = append(rs.Lines, role+": "+text)
}

func (rs *reqSummary) addTool(name string) {
	rs.Tools++
	if name != "" {
		rs.ToolNames = append(rs.ToolNames, name)
	}
}

// param appends "name value" when v is a number (or any non-nil scalar).
func (rs *reqSummary) param(name string, v any) {
	if v == nil {
		return
	}
	if f, ok := num(v); ok {
		if f == float64(int64(f)) {
			rs.Params = append(rs.Params, name+" "+commas(int(f)))
		} else {
			rs.Params = append(rs.Params, name+" "+strconv.FormatFloat(f, 'g', -1, 64))
		}
		return
	}
	rs.Params = append(rs.Params, name+" "+fmt.Sprint(v))
}

// streamParam appends "stream yes|no" when v is a boolean.
func (rs *reqSummary) streamParam(v any) {
	b, ok := v.(bool)
	if !ok {
		return
	}
	if b {
		rs.Params = append(rs.Params, "stream yes")
	} else {
		rs.Params = append(rs.Params, "stream no")
	}
}

// line renders the "request:" digest line.
func (rs reqSummary) line(showTools bool) string {
	if !rs.OK {
		if rs.Bytes == 0 {
			return "request: -"
		}
		return fmt.Sprintf("request: (not JSON, %s bytes)", commas(rs.Bytes))
	}
	var parts []string
	if n := utf8.RuneCountInString(rs.System); n > 0 {
		parts = append(parts, "system "+humanChars(n)+" chars")
	}
	if rs.Messages > 0 {
		s := plural(rs.Messages, "message")
		var roles []string
		for _, k := range []string{"u", "a", "s", "t"} {
			if c := rs.Roles[k]; c > 0 {
				roles = append(roles, k+strconv.Itoa(c))
			}
		}
		if len(roles) > 0 {
			s += " (" + strings.Join(roles, "/") + ")"
		}
		parts = append(parts, s)
	}
	if rs.Tools > 0 {
		s := plural(rs.Tools, "tool")
		if showTools && len(rs.ToolNames) > 0 {
			names := rs.ToolNames
			if len(names) > 5 {
				names = append(append([]string{}, names[:5]...), fmt.Sprintf("…(+%d)", rs.Tools-5))
			}
			s += " (" + strings.Join(names, ", ") + ")"
		}
		parts = append(parts, s)
	}
	parts = append(parts, rs.Params...)
	if len(parts) == 0 {
		return "request: (empty)"
	}
	return "request: " + strings.Join(parts, " · ")
}

// item is one rendered element of the final response.
type item struct {
	Kind string // text, tool_use, tool_call, function_call, thinking, reasoning, refusal, ...
	Name string // tool name
	Text string // text / thinking / refusal content
	JSON string // compact tool input
	Note string // free-form annotation
}

// group is a set of items under an optional label (per choice / candidate).
type group struct {
	Label string
	Items []item
}

const (
	textPreview     = 200
	thinkingPreview = 800
	toolPreview     = 200
	messagePreview  = 120
	systemPreview   = 800
	requestPreview  = 2000
	truncMarker     = "…[truncated]"
)

// render produces the digest text.
func render(r *Result, req reqSummary, groups []group, info streamInfo, inc map[string]bool, maxChars int, rawReq []byte) string {
	var b strings.Builder
	line := func(format string, args ...any) {
		b.WriteString(strings.TrimRight(fmt.Sprintf(format, args...), " \t"))
		b.WriteByte('\n')
	}

	model := r.Model
	if model == "" {
		model = "-"
	}
	stream := "no"
	if r.Stream {
		stream = "yes"
		if r.Partial {
			last := ""
			if info.Last != "" {
				last = ", last: " + info.Last
			}
			stream = fmt.Sprintf("yes (INCOMPLETE — %s%s)", plural(info.Events, "event"), last)
		}
	}
	line("provider: %s  model: %s  stream: %s  status: %d", r.Provider, model, stream, r.Status)
	line("%s", req.line(inc[IncludeTools]))

	stop := ""
	if inc[IncludeStop] && r.StopReason != "" {
		stop = "stop: " + r.StopReason
	}
	if inc[IncludeUsage] {
		parts := usageParts(r.Usage)
		if len(parts) == 0 {
			parts = []string{"-"}
		}
		if stop != "" {
			parts = append(parts, stop)
		}
		line("usage: %s", strings.Join(parts, " · "))
	} else if stop != "" {
		line("%s", stop)
	}

	if inc[IncludeFinal] && r.Final != nil {
		total := 0
		for _, g := range groups {
			total += len(g.Items)
		}
		if total == 0 {
			line("final: (no content)")
		} else {
			line("final:")
			for _, g := range groups {
				indent := "  "
				if g.Label != "" {
					line("  %s:", g.Label)
					indent = "    "
				}
				for _, it := range g.Items {
					line("%s%s", indent, renderItem(it, inc))
				}
			}
		}
	}

	if inc[IncludeErrors] {
		switch len(r.Errors) {
		case 0:
			line("errors: none")
		case 1:
			line("errors: %s", oneLine(r.Errors[0]))
		default:
			line("errors:")
			for _, e := range r.Errors {
				line("  %s", oneLine(e))
			}
		}
	}

	if inc[IncludeMessages] {
		if len(req.Lines) == 0 {
			line("messages: -")
		} else {
			line("messages:")
			for _, l := range req.Lines {
				line("  %s", truncRunes(oneLine(l), messagePreview))
			}
		}
	}
	if inc[IncludeSystem] {
		if req.System == "" {
			line("system: -")
		} else {
			line("system:")
			line("%s", indentBlock(truncRunes(req.System, systemPreview), "  "))
		}
	}
	if inc[IncludeRequest] {
		if v, err := decodeJSON(rawReq); err == nil {
			line("request_json:")
			line("%s", indentBlock(truncRunes(string(marshalJSON(v, "  ")), requestPreview), "  "))
		} else if len(rawReq) > 0 {
			line("request_json: (not JSON)")
			line("%s", indentBlock(truncRunes(string(rawReq), requestPreview), "  "))
		} else {
			line("request_json: -")
		}
	}

	out := strings.TrimRight(b.String(), "\n")
	if utf8.RuneCountInString(out) > maxChars {
		keep := maxChars - utf8.RuneCountInString(truncMarker)
		if keep < 0 {
			keep = 0
		}
		out = strings.TrimRight(cutRunes(out, keep), " \t\n") + truncMarker
		out = cutRunes(out, maxChars)
	}
	return out
}

func usageParts(u map[string]any) []string {
	if u == nil {
		return nil
	}
	var parts []string
	in := "in " + commas(intOr(u["input_tokens"], 0))
	cr, hasCR := toInt(u["cache_read_input_tokens"])
	cc, hasCC := toInt(u["cache_creation_input_tokens"])
	if hasCR || hasCC {
		var cache []string
		if hasCR {
			cache = append(cache, "cache_read "+commas(cr))
		}
		if hasCC {
			cache = append(cache, "cache_write "+commas(cc))
		}
		in += " (" + strings.Join(cache, ", ") + ")"
	}
	parts = append(parts, in)
	out := "out " + commas(intOr(u["output_tokens"], 0))
	if rt, ok := toInt(u["reasoning_tokens"]); ok && rt > 0 {
		out += " (reasoning " + commas(rt) + ")"
	}
	parts = append(parts, out)
	if t, ok := toInt(u["total_tokens"]); ok {
		parts = append(parts, "total "+commas(t))
	}
	return parts
}

func renderItem(it item, inc map[string]bool) string {
	switch it.Kind {
	case "text", "refusal", "executable_code", "code_execution_result":
		q, n := quoteTrunc(it.Text, textPreview)
		return fmt.Sprintf("[%s] %s (%s chars)", it.Kind, q, commas(n))
	case "thinking", "reasoning":
		n := utf8.RuneCountInString(it.Text)
		if n == 0 {
			if it.Note != "" {
				return fmt.Sprintf("[%s] %s", it.Kind, it.Note)
			}
			return fmt.Sprintf("[%s] (empty)", it.Kind)
		}
		if !inc[IncludeThinking] {
			return fmt.Sprintf("[%s] (%s chars, hidden — include=thinking to show)", it.Kind, commas(n))
		}
		q, _ := quoteTrunc(it.Text, thinkingPreview)
		return fmt.Sprintf("[%s] %s (%s chars)", it.Kind, q, commas(n))
	}
	s := "[" + it.Kind + "]"
	if it.Name != "" {
		s += " " + it.Name
	}
	if it.JSON != "" {
		if inc[IncludeTools] {
			s += " " + truncRunes(oneLine(it.JSON), toolPreview)
		} else {
			s += " (input hidden — include=tools to show)"
		}
	}
	if it.Note != "" {
		s += " " + it.Note
	}
	if it.Text != "" && it.JSON == "" {
		q, n := quoteTrunc(it.Text, textPreview)
		s += fmt.Sprintf(" %s (%s chars)", q, commas(n))
	}
	return s
}

var escaper = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t")

// quoteTrunc quotes s on one line, truncated to max runes, and returns the
// full rune count.
func quoteTrunc(s string, max int) (string, int) {
	n := utf8.RuneCountInString(s)
	return "\"" + escaper.Replace(truncRunes(s, max)) + "\"", n
}

// oneLine collapses newlines so a value fits a single digest line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// truncRunes cuts s to max runes, appending "…" when it was cut.
func truncRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return cutRunes(s, max-1) + "…"
}

// cutRunes returns the first n runes of s.
func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func indentBlock(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(indent+l, " \t")
	}
	return strings.Join(lines, "\n")
}

// commas formats an integer with thousands separators.
func commas(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	if len(s) > 3 {
		var b strings.Builder
		pre := len(s) % 3
		if pre > 0 {
			b.WriteString(s[:pre])
		}
		for i := pre; i < len(s); i += 3 {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(s[i : i+3])
		}
		s = b.String()
	}
	if neg {
		return "-" + s
	}
	return s
}

// humanChars renders a character count as 812, 1.2k or 3.4M.
func humanChars(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1000, 'f', 1, 64), ".0") + "k"
	default:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64), ".0") + "M"
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return commas(n) + " " + word + "s"
}
