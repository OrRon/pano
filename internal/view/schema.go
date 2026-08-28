package view

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

const (
	schemaMaxKeys  = 48  // keys tracked per object
	schemaMaxElems = 256 // array elements merged
	enumMaxValues  = 5   // distinct values for an enum
	enumMaxLen     = 32  // longest enum value
	inlineWidth    = 72  // objects/arrays rendered on one line up to this
)

var (
	reEnumValue = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	reUUID      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reEmail     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	reBareKey   = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$-]*$`)
)

// shape is the merged type of every JSON value seen at one position.
type shape struct {
	seen      int            // values merged into this shape
	kinds     map[string]int // "str", "int", "float", "bool", "null", "object", "array"
	deep      bool           // nesting exceeded schemaMaxDepth
	sensitive bool           // under a sensitive key: enum values are masked

	keys      map[string]*shape // object members (union)
	order     []string          // first-seen key order
	objects   int               // object values merged (for optional detection)
	extraKeys bool              // more than schemaMaxKeys distinct keys

	elem *shape // merged array element shape

	strCount int            // string values seen
	strShort int            // … of which are enum candidates
	strs     map[string]int // distinct candidate values (bounded)
	strOrder []string
	formats  map[string]int // detected string formats
}

func newShape() *shape { return &shape{kinds: map[string]int{}} }

// detectFormat recognises common string formats.
func detectFormat(s string) string {
	switch {
	case s == "":
		return ""
	case len(s) >= 19 && len(s) <= 40 && s[4] == '-':
		if _, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return "date-time"
		}
		if _, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
			return "date-time"
		}
	case len(s) == 10 && s[4] == '-':
		if _, err := time.Parse("2006-01-02", s); err == nil {
			return "date"
		}
	}
	switch {
	case len(s) == 36 && reUUID.MatchString(s):
		return "uuid"
	case strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://"):
		return "url"
	case len(s) < 254 && reEmail.MatchString(s):
		return "email"
	}
	return ""
}

// merge folds a value into the shape.
func (s *shape) merge(v gjson.Result, depth int) {
	s.seen++
	switch {
	case v.Type == gjson.String:
		s.kinds["str"]++
		s.strCount++
		str := v.String()
		if f := detectFormat(str); f != "" {
			if s.formats == nil {
				s.formats = map[string]int{}
			}
			s.formats[f]++
		} else if utf8.RuneCountInString(str) <= enumMaxLen && reEnumValue.MatchString(str) {
			s.strShort++
			if s.strs == nil {
				s.strs = map[string]int{}
			}
			if _, ok := s.strs[str]; ok || len(s.strs) <= enumMaxValues {
				if !ok {
					s.strOrder = append(s.strOrder, str)
				}
				s.strs[str]++
			}
		}
	case v.Type == gjson.Number:
		s.kinds[numKind(v.Raw)]++
	case v.Type == gjson.True || v.Type == gjson.False:
		s.kinds["bool"]++
	case v.IsObject():
		s.kinds["object"]++
		s.objects++
		if depth >= schemaMaxDepth {
			s.deep = true
			return
		}
		if s.keys == nil {
			s.keys = map[string]*shape{}
		}
		v.ForEach(func(k, val gjson.Result) bool {
			key := k.String()
			c := s.keys[key]
			if c == nil {
				if len(s.keys) >= schemaMaxKeys {
					s.extraKeys = true
					return true
				}
				c = newShape()
				c.sensitive = s.sensitive || isSensitiveKey(key)
				s.keys[key] = c
				s.order = append(s.order, key)
			}
			c.merge(val, depth+1)
			return true
		})
	case v.IsArray():
		s.kinds["array"]++
		if depth >= schemaMaxDepth {
			s.deep = true
			return
		}
		if s.elem == nil {
			s.elem = newShape()
			s.elem.sensitive = s.sensitive
		}
		i := 0
		v.ForEach(func(_, val gjson.Result) bool {
			if i >= schemaMaxElems {
				return false
			}
			i++
			s.elem.merge(val, depth+1)
			return true
		})
	default:
		s.kinds["null"]++
	}
}

// keyName renders an object key bare when it is identifier-like.
func keyName(k string) string {
	if reBareKey.MatchString(k) {
		return k
	}
	return strconv.Quote(k)
}

// render writes the shape; indent is the indentation of the line the
// shape starts on. Enum values under sensitive keys are masked through m.
func (s *shape) render(indent string, m *masker) string {
	var parts []string
	if s.kinds["object"] > 0 {
		if s.deep && len(s.keys) == 0 {
			parts = append(parts, "{…}")
		} else {
			parts = append(parts, s.renderObject(indent, m))
		}
	}
	if s.kinds["array"] > 0 {
		if s.deep && s.elem == nil {
			parts = append(parts, "[…]")
		} else {
			parts = append(parts, s.renderArray(indent, m))
		}
	}
	if s.kinds["str"] > 0 {
		parts = append(parts, s.renderStr(m))
	}
	switch {
	case s.kinds["float"] > 0:
		parts = append(parts, "float")
	case s.kinds["int"] > 0:
		parts = append(parts, "int")
	}
	if s.kinds["bool"] > 0 {
		parts = append(parts, "bool")
	}
	if s.kinds["null"] > 0 {
		parts = append(parts, "null")
	}
	if len(parts) == 0 {
		return "?"
	}
	return strings.Join(parts, "|")
}

func (s *shape) renderObject(indent string, m *masker) string {
	if len(s.keys) == 0 {
		return "{}"
	}
	entries := make([]string, 0, len(s.order)+1)
	for _, k := range s.order {
		c := s.keys[k]
		name := keyName(k)
		if c.seen < s.objects {
			name += "?"
		}
		entries = append(entries, name+": "+c.render(indent+"  ", m))
	}
	if s.extraKeys {
		entries = append(entries, "…")
	}
	inline := "{ " + strings.Join(entries, ", ") + " }"
	if len(inline) <= inlineWidth && !strings.Contains(inline, "\n") {
		return inline
	}
	inner := indent + "  "
	return "{\n" + inner + strings.Join(entries, ",\n"+inner) + "\n" + indent + "}"
}

func (s *shape) renderArray(indent string, m *masker) string {
	if s.elem == nil || s.elem.seen == 0 {
		return "[]"
	}
	e := s.elem.render(indent+"  ", m)
	if len(e) <= inlineWidth && !strings.Contains(e, "\n") {
		return "[ " + e + " ]"
	}
	return "[\n" + indent + "  " + e + "\n" + indent + "]"
}

func (s *shape) renderStr(m *masker) string {
	if s.strCount >= 2 && s.strShort == s.strCount && len(s.strs) <= enumMaxValues {
		vals := make([]string, 0, len(s.strOrder))
		for _, v := range s.strOrder {
			if s.sensitive && m != nil && m.on {
				m.n++
				v = Mask(v)
			}
			vals = append(vals, strconv.Quote(v))
		}
		return strings.Join(vals, "|")
	}
	for f, n := range s.formats {
		if n == s.strCount {
			return "str<" + f + ">"
		}
	}
	return "str"
}

// schemaView is the "schema" mode dispatcher.
func schemaView(kind string, content []byte, opts Options, m *masker) string {
	var out string
	switch kind {
	case kindJSON:
		s := newShape()
		s.merge(gjson.ParseBytes(content), 0)
		out = s.render("", m)
	case kindForm:
		s := newShape()
		s.kinds["object"] = 1
		s.objects = 1
		s.keys = map[string]*shape{}
		for _, kv := range formPairs(content) {
			k, v := kv[0], kv[1]
			c := s.keys[k]
			if c == nil {
				c = newShape()
				c.sensitive = isSensitiveKey(k)
				s.keys[k] = c
				s.order = append(s.order, k)
			}
			c.merge(gjson.Result{Type: gjson.String, Str: v, Raw: strconv.Quote(v)}, 1)
		}
		out = s.render("", m)
	case kindSSE:
		out = schemaSSE(content, m)
	default:
		out = fmt.Sprintf("(no schema for %s; showing summary)\n", kind) + summaryView(kind, content, opts, m)
	}
	return capText(out, schemaBudget)
}

// schemaSSE merges the JSON data of each event name into a shape.
func schemaSSE(content []byte, m *masker) string {
	events := parseSSE(content)
	shapes := map[string]*shape{}
	var order []string
	for _, ev := range events {
		s := shapes[ev.name]
		if s == nil {
			s = newShape()
			shapes[ev.name] = s
			order = append(order, ev.name)
		}
		data := strings.TrimSpace(ev.data)
		if looksLikeJSON([]byte(data)) {
			s.merge(gjson.Parse(data), 1)
		} else {
			s.merge(gjson.Result{Type: gjson.String, Str: data, Raw: strconv.Quote(data)}, 1)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "events: %d\n", len(events))
	for i, name := range order {
		if i >= 20 {
			fmt.Fprintf(&sb, "… %d more event types\n", len(order)-i)
			break
		}
		fmt.Fprintf(&sb, "%s (%d): %s\n", name, shapes[name].seen, shapes[name].render("", m))
	}
	return strings.TrimRight(sb.String(), "\n")
}
