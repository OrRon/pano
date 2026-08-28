package explain

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// decodeJSON decodes b into generic values, keeping numbers as json.Number so
// that ids and counts round-trip byte-for-byte.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// decodeObject is decodeJSON restricted to a top-level object.
func decodeObject(b []byte) (map[string]any, error) {
	v, err := decodeJSON(b)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// marshalJSON encodes v without HTML escaping. indent is the indent string
// ("" for compact output).
func marshalJSON(v any, indent string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }
func str(v any) string         { s, _ := v.(string); return s }

// num reads any JSON number representation.
func num(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func toInt(v any) (int, bool) {
	f, ok := num(v)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func intOr(v any, def int) int {
	if n, ok := toInt(v); ok {
		return n
	}
	return def
}

// deepCopy clones decoded JSON so reassembly never mutates event payloads
// shared between events.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = deepCopy(x)
		}
		return m
	case []any:
		a := make([]any, len(t))
		for i, x := range t {
			a[i] = deepCopy(x)
		}
		return a
	}
	return v
}

// compactJSONString renders v as compact JSON; when v is a string that itself
// holds JSON (OpenAI tool arguments) it is compacted rather than re-quoted.
func compactJSONString(v any) string {
	if s, ok := v.(string); ok {
		t := strings.TrimSpace(s)
		if t == "" {
			return ""
		}
		if parsed, err := decodeJSON([]byte(t)); err == nil {
			return string(marshalJSON(parsed, ""))
		}
		return t
	}
	if v == nil {
		return ""
	}
	return string(marshalJSON(v, ""))
}
