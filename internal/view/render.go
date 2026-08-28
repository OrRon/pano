package view

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"github.com/orron/pano/internal/mimeclass"
)

// Content kinds detected by Render. They refine mimeclass classes with
// sniffing (a text/plain body that is valid JSON renders as JSON).
const (
	kindJSON   = "json"
	kindSSE    = "sse"
	kindHTML   = "html"
	kindForm   = "form"
	kindXML    = "xml"
	kindJS     = "js"
	kindCSS    = "css"
	kindText   = "text"
	kindString = "string" // a plain string selected by path
)

// BinaryNote is the content line Render emits instead of a binary body.
const BinaryNote = "(binary; use pano export har or `pano show --out FILE` to save)"

// Render renders a captured body in the given view mode and returns the
// text to show, the number of redactions applied and whether the body was
// treated as binary.
//
// body holds the raw wire bytes; encoding is the Content-Encoding header
// (possibly comma-chained) and mime the Content-Type. path is an optional
// gjson path; JSONPath syntax ("$.a[0].b") is accepted and normalised with
// NormalizePath. The selected value is rendered with the mode, except that
// a plain string is shown as-is.
//
// Every result starts with a header line such as
//
//	body: application/json 5120B [gzip→18342B] sha256:1f2e3d4c [truncated to 4096 of 18342]
//
// followed by a newline and the content. Bodies whose MIME class is
// img/font/media/bin, or that are not valid UTF-8 after decoding, are
// never inlined: the content is BinaryNote and binary is true.
//
// Modes: ViewSummary (content-type aware digest), ViewSchema (inferred
// shape of JSON, forms and SSE data), ViewTruncated (head and tail within
// MaxBytes), ViewPretty (indented JSON, "key: value" forms, or the decoded
// text; truncated when over budget) and ViewRaw (decoded bytes up to
// MaxBytes). An empty mode means ViewSummary; any other value is an error.
func Render(mode string, body []byte, encoding, mime, path string, opts Options) (text string, redacted int, binary bool, err error) {
	opts = opts.normalize()
	switch mode {
	case "":
		mode = ViewSummary
	case ViewSummary, ViewSchema, ViewTruncated, ViewPretty, ViewRaw:
	default:
		return "", 0, false, fmt.Errorf("unknown view %q (want summary, schema, truncated, pretty or raw)", mode)
	}

	decoded, derr := Decode(encoding, body, 0)
	if derr != nil && len(decoded) == 0 {
		return "", 0, false, fmt.Errorf("decode body: %w", derr)
	}
	hdr := headerLine(mime, body, decoded, encoding)
	if derr != nil {
		hdr += " [decode error: " + derr.Error() + "]"
	}

	class := mimeclass.Of(mime)
	if isBinary(class, decoded) {
		return hdr + "\n" + BinaryNote, 0, true, nil
	}
	if len(decoded) == 0 {
		return hdr + "\n(empty)", 0, false, nil
	}

	kind := detectKind(class, mime, decoded)
	content := decoded
	if path = strings.TrimSpace(path); path != "" {
		if !utf8.ValidString(path) {
			return "", 0, false, fmt.Errorf("path is not valid UTF-8")
		}
		np := NormalizePath(path)
		switch kind {
		case kindJSON:
		case kindSSE:
			return "", 0, false, fmt.Errorf("path %q: not applied to event streams here; use pano_flow_explain to select from the reassembled stream", path)
		default:
			return "", 0, false, fmt.Errorf("path %q: body is %s, not JSON", path, kind)
		}
		res := gjson.GetBytes(content, np)
		if !res.Exists() {
			return "", 0, false, fmt.Errorf("path %q not found; %s", np, describeTopLevel(content))
		}
		hdr += " path:" + np
		if res.Type == gjson.String {
			content, kind = []byte(res.String()), kindString
		} else {
			content = []byte(res.Raw)
		}
	}

	var (
		out string
		cut cutInfo
	)
	m := &masker{on: opts.redacting()}
	if kind == kindString {
		if mode == ViewRaw {
			out, cut = rawView(content, opts)
		} else {
			out, cut = headTail(content, opts.MaxBytes)
		}
	} else {
		switch mode {
		case ViewSummary:
			out = summaryView(kind, content, opts, m)
		case ViewSchema:
			out = schemaView(kind, content, opts, m)
		case ViewTruncated:
			out, cut = truncatedView(kind, content, opts)
		case ViewPretty:
			out, cut = prettyView(kind, content, opts, m)
		case ViewRaw:
			out, cut = rawView(content, opts)
		}
	}
	if cut.truncated {
		hdr += fmt.Sprintf(" [truncated to %d of %d]", cut.shown, cut.total)
	}
	if opts.redacting() {
		out, redacted = RedactText(out)
	}
	redacted += m.n
	return hdr + "\n" + out, redacted, false, nil
}

// headerLine builds the first line of every rendered body. The size and
// hash refer to the raw wire bytes so they match the stored blob.
func headerLine(mime string, raw, decoded []byte, encoding string) string {
	mt := mimeclass.MediaType(mime)
	if mt == "" {
		mt = "-"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "body: %s %dB", mt, len(raw))
	if encs := splitEncodings(encoding); len(encs) > 0 {
		fmt.Fprintf(&sb, " [%s→%dB]", strings.Join(encs, ","), len(decoded))
	}
	sum := sha256.Sum256(raw)
	sb.WriteString(" sha256:")
	sb.WriteString(hex.EncodeToString(sum[:4]))
	return sb.String()
}

// isBinary reports whether a body must not be shown inline.
func isBinary(class string, b []byte) bool {
	switch class {
	case "img", "font", "media", "bin":
		return true
	}
	if !utf8.Valid(b) {
		return true
	}
	return bytes.IndexByte(b, 0) >= 0
}

// looksLikeJSON reports whether b is a JSON object or array.
func looksLikeJSON(b []byte) bool {
	t := bytes.TrimLeft(b, " \t\r\n")
	if len(t) == 0 || (t[0] != '{' && t[0] != '[') {
		return false
	}
	return json.Valid(t)
}

// detectKind maps a mimeclass class plus content sniffing to a kind.
func detectKind(class, mime string, b []byte) string {
	switch class {
	case "json":
		if json.Valid(b) {
			return kindJSON
		}
		return kindText
	case "sse":
		return kindSSE
	case "html":
		return kindHTML
	case "form":
		if strings.HasPrefix(mimeclass.MediaType(mime), "multipart/") {
			return kindText
		}
		return kindForm
	case "xml", "js", "css":
		return class
	}
	if looksLikeJSON(b) {
		return kindJSON
	}
	if looksLikeSSE(b) {
		return kindSSE
	}
	return kindText
}

// describeTopLevel explains what a JSON body contains, for path errors.
func describeTopLevel(content []byte) string {
	r := gjson.ParseBytes(content)
	switch {
	case r.IsObject():
		var keys []string
		r.ForEach(func(k, _ gjson.Result) bool {
			if len(keys) >= 30 {
				keys = append(keys, "…")
				return false
			}
			keys = append(keys, k.String())
			return true
		})
		if len(keys) == 0 {
			return "top-level object is empty"
		}
		return "top-level keys: " + strings.Join(keys, ", ")
	case r.IsArray():
		n := 0
		r.ForEach(func(_, _ gjson.Result) bool { n++; return true })
		return fmt.Sprintf("top-level is an array of %d elements (start with an index, e.g. 0.key, or # for all)", n)
	}
	return "body is a JSON " + typeWord(r)
}

// NormalizePath converts a JSONPath expression such as "$.a[0].b" or
// "$['a b'][*].c" into gjson syntax ("a.0.b", "a b.#.c"). Paths that are
// already gjson syntax are returned unchanged apart from trimming.
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	hasDollar := strings.HasPrefix(p, "$")
	if !hasDollar && !strings.Contains(p, "[") {
		return p
	}
	if hasDollar {
		p = strings.TrimPrefix(p, "$")
	}
	var sb strings.Builder
	emit := func(seg string) {
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(seg)
	}
	i := 0
	for i < len(p) {
		switch p[i] {
		case '.':
			i++
		case '[':
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				emit(p[i:])
				i = len(p)
				continue
			}
			inner := strings.TrimSpace(p[i+1 : i+end])
			i += end + 1
			switch {
			case inner == "*":
				emit("#")
			case len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') && inner[len(inner)-1] == inner[0]:
				emit(escapeKey(inner[1 : len(inner)-1]))
			default:
				emit(inner)
			}
		default:
			j := i
			for j < len(p) && p[j] != '.' && p[j] != '[' {
				j++
			}
			emit(p[i:j])
			i = j
		}
	}
	return sb.String()
}
