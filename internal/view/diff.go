package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Diff limits.
const (
	// DefaultMaxChanges is used by DiffJSON and DiffHeaders when maxChanges
	// is zero or negative.
	DefaultMaxChanges = 50
	// DefaultMaxDiffLines is used by DiffText when maxLines is zero or
	// negative.
	DefaultMaxDiffLines = 200
	diffValueLen        = 120
	diffContext         = 2
	diffMaxCells        = 4_000_000 // LCS table bound before falling back
)

// defaultIgnoreHeaders are skipped by DiffHeaders when ignore is nil.
var defaultIgnoreHeaders = []string{
	"date", "age", "etag", "x-request-id", "cf-ray", "set-cookie",
	"content-length", "traceparent", "x-amzn-trace-id", "x-amz-request-id",
}

// DiffJSON compares two JSON documents structurally and lists the changes
// as "+ path: value" (added in b), "- path: value" (removed from b) and
// "~ path: old → new" lines, using gjson-style paths ("$" for the root).
// Arrays are compared positionally, with a note when their lengths differ.
// Values are cut at 120 characters. At most maxChanges lines are listed
// (default 50) followed by "… and N more"; the returned count is the total
// number of changes. If either input is not valid JSON it falls back to
// DiffText.
func DiffJSON(a, b []byte, maxChanges int) (string, int) {
	if maxChanges <= 0 {
		maxChanges = DefaultMaxChanges
	}
	va, errA := parseJSONValue(a)
	vb, errB := parseJSONValue(b)
	if errA != nil || errB != nil {
		text, n := DiffText(string(a), string(b), maxChanges)
		return "(not JSON; text diff)\n" + text, n
	}
	d := &jsonDiffer{max: maxChanges}
	d.walk("$", va, vb)
	if d.total == 0 {
		return "(no changes)", 0
	}
	if d.total > d.max {
		d.lines = append(d.lines, fmt.Sprintf("… and %d more", d.total-d.max))
	}
	return strings.Join(d.lines, "\n"), d.total
}

// parseJSONValue decodes JSON keeping numbers as json.Number.
func parseJSONValue(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

type jsonDiffer struct {
	lines []string
	total int
	max   int
}

func (d *jsonDiffer) add(line string) {
	d.total++
	if d.total <= d.max {
		d.lines = append(d.lines, line)
	}
}

func childPath(parent, key string) string {
	if parent == "$" {
		return key
	}
	return parent + "." + key
}

func (d *jsonDiffer) walk(path string, a, b any) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			break
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			p := childPath(path, escapeKey(k))
			x, inA := av[k]
			y, inB := bv[k]
			switch {
			case inA && !inB:
				d.add("- " + p + ": " + compactValue(x))
			case !inA && inB:
				d.add("+ " + p + ": " + compactValue(y))
			default:
				d.walk(p, x, y)
			}
		}
		return
	case []any:
		bv, ok := b.([]any)
		if !ok {
			break
		}
		if len(av) != len(bv) {
			d.add(fmt.Sprintf("~ %s: array length %d → %d", path, len(av), len(bv)))
		}
		n := min(len(av), len(bv))
		for i := 0; i < n; i++ {
			d.walk(childPath(path, strconv.Itoa(i)), av[i], bv[i])
		}
		for i := n; i < len(av); i++ {
			d.add("- " + childPath(path, strconv.Itoa(i)) + ": " + compactValue(av[i]))
		}
		for i := n; i < len(bv); i++ {
			d.add("+ " + childPath(path, strconv.Itoa(i)) + ": " + compactValue(bv[i]))
		}
		return
	}
	if !reflect.DeepEqual(a, b) {
		d.add("~ " + path + ": " + compactValue(a) + " → " + compactValue(b))
	}
}

// escapeKey makes an object key safe inside a gjson path.
func escapeKey(k string) string {
	if strings.ContainsAny(k, ".*?|#@\\") {
		r := strings.NewReplacer(`\`, `\\`, ".", `\.`, "*", `\*`, "?", `\?`, "|", `\|`, "#", `\#`, "@", `\@`)
		return r.Replace(k)
	}
	return k
}

// compactValue renders a decoded JSON value compactly, cut at diffValueLen.
func compactValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if len(s) > diffValueLen {
		s = cutRunes(s, diffValueLen) + "…"
	}
	return s
}

// DiffText compares two texts line by line and renders a unified-style
// diff with two lines of context per hunk. It returns the text and the
// number of changed (+/-) lines; at most maxLines diff lines are rendered
// (default 200) followed by "… and N more lines".
func DiffText(a, b string, maxLines int) (string, int) {
	if maxLines <= 0 {
		maxLines = DefaultMaxDiffLines
	}
	if a == b {
		return "(no changes)", 0
	}
	al := splitLines(a)
	bl := splitLines(b)
	ops := diffLines(al, bl)

	changes := 0
	for _, op := range ops {
		if op.kind != ' ' {
			changes++
		}
	}
	if changes == 0 {
		return "(no changes)", 0
	}

	var (
		lines   []string
		printed int
		i       int
	)
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// Hunk: from i back diffContext, forward until a run of >2*context
		// unchanged lines.
		start := max(i-diffContext, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := 0
			for end+run < len(ops) && ops[end+run].kind == ' ' {
				run++
			}
			if run > 2*diffContext {
				end += diffContext
				break
			}
			end += run
		}
		end = min(end, len(ops))
		aStart, bStart := ops[start].aIdx+1, ops[start].bIdx+1
		aLen, bLen := 0, 0
		for _, op := range ops[start:end] {
			if op.kind != '+' {
				aLen++
			}
			if op.kind != '-' {
				bLen++
			}
		}
		lines = append(lines, fmt.Sprintf("@@ -%d,%d +%d,%d @@", aStart, aLen, bStart, bLen))
		capped := false
		for _, op := range ops[start:end] {
			if op.kind != ' ' {
				if printed >= maxLines {
					capped = true
					break
				}
				printed++
			}
			lines = append(lines, string(op.kind)+op.text)
		}
		if capped {
			lines = append(lines, fmt.Sprintf("… and %d more lines", changes-maxLines))
			break
		}
		i = end
	}
	return strings.Join(lines, "\n"), changes
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// diffOp is one line of an edit script.
type diffOp struct {
	kind       byte // ' ', '-', '+'
	text       string
	aIdx, bIdx int // 0-based positions in a and b (next index for the other side)
}

// diffLines computes a line-level edit script. Common prefix and suffix
// are trimmed first; the middle is solved with an LCS table when small
// enough and otherwise emitted as a whole-block replacement.
func diffLines(a, b []string) []diffOp {
	var ops []diffOp
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		ops = append(ops, diffOp{' ', a[pre], pre, pre})
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma := a[pre : len(a)-suf]
	mb := b[pre : len(b)-suf]

	ai, bi := pre, pre
	emit := func(kind byte, text string) {
		ops = append(ops, diffOp{kind, text, ai, bi})
		if kind != '+' {
			ai++
		}
		if kind != '-' {
			bi++
		}
	}

	if len(ma)*len(mb) > diffMaxCells || len(ma) == 0 || len(mb) == 0 {
		for _, l := range ma {
			emit('-', l)
		}
		for _, l := range mb {
			emit('+', l)
		}
	} else {
		n, m := len(ma), len(mb)
		table := make([]int32, (n+1)*(m+1))
		at := func(i, j int) *int32 { return &table[i*(m+1)+j] }
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					*at(i, j) = *at(i+1, j+1) + 1
				} else {
					*at(i, j) = max(*at(i+1, j), *at(i, j+1))
				}
			}
		}
		i, j := 0, 0
		for i < n && j < m {
			switch {
			case ma[i] == mb[j]:
				emit(' ', ma[i])
				i++
				j++
			case *at(i+1, j) >= *at(i, j+1):
				emit('-', ma[i])
				i++
			default:
				emit('+', mb[j])
				j++
			}
		}
		for ; i < n; i++ {
			emit('-', ma[i])
		}
		for ; j < m; j++ {
			emit('+', mb[j])
		}
	}
	for k := 0; k < suf; k++ {
		emit(' ', a[len(a)-suf+k])
	}
	return ops
}

// DiffHeaders compares two header sets and lists added ("+ Name: value"),
// removed ("- Name: value") and changed ("~ Name: old → new") headers,
// sorted by name. Names listed in ignore are skipped; when ignore is nil a
// default set of volatile headers (Date, Age, ETag, X-Request-Id, CF-Ray,
// Set-Cookie, Content-Length, Traceparent, X-Amzn-Trace-Id,
// X-Amz-Request-Id) is used. Pass an empty, non-nil slice to compare all
// headers. Values are compared as given, so pass RedactHeaders output when
// the result will be shown.
func DiffHeaders(a, b http.Header, ignore []string) (string, int) {
	if ignore == nil {
		ignore = defaultIgnoreHeaders
	}
	skip := map[string]bool{}
	for _, n := range ignore {
		skip[strings.ToLower(n)] = true
	}
	type entry struct {
		name string
		vals []string
	}
	collect := func(h http.Header) map[string]entry {
		out := map[string]entry{}
		for k, v := range h {
			lk := strings.ToLower(k)
			if skip[lk] {
				continue
			}
			e := out[lk]
			e.name = http.CanonicalHeaderKey(k)
			e.vals = append(e.vals, v...)
			out[lk] = e
		}
		return out
	}
	ea, eb := collect(a), collect(b)
	names := map[string]bool{}
	for k := range ea {
		names[k] = true
	}
	for k := range eb {
		names[k] = true
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var lines []string
	for _, k := range sorted {
		x, inA := ea[k]
		y, inB := eb[k]
		switch {
		case inA && !inB:
			lines = append(lines, "- "+x.name+": "+strings.Join(x.vals, ", "))
		case !inA && inB:
			lines = append(lines, "+ "+y.name+": "+strings.Join(y.vals, ", "))
		default:
			xv, yv := strings.Join(x.vals, ", "), strings.Join(y.vals, ", ")
			if xv != yv {
				lines = append(lines, "~ "+x.name+": "+xv+" → "+yv)
			}
		}
	}
	if len(lines) == 0 {
		return "(no header changes)", 0
	}
	return strings.Join(lines, "\n"), len(lines)
}
