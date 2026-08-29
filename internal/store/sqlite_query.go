package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/glob"
)

// MaxQueryTotal caps FlowList.Total: counting stops at this many matches.
const MaxQueryTotal = 10_000

// maxQueryScan bounds how many SQL candidates are hydrated and run through
// the Matcher when a filter has parts SQL cannot express.
const maxQueryScan = 100_000

// Query lists persisted flows newest-first with the same filter semantics as
// Query over Mem. Cheap predicates (host, method, status, since/until,
// session, kind, state, has_error, min_bytes, cursor) become SQL; the rest
// (path patterns, content_type, tag, rule, mocked/blocked) run the compiled
// Matcher over candidates. A non-empty Q is a full-text search over host,
// path, headers and decoded bodies of finished flows (FTS5, token based;
// results are ranked by bm25 then newest). Total is capped at MaxQueryTotal.
func (s *SQLite) Query(ctx context.Context, f api.FlowFilter, limit int, now time.Time) (api.FlowList, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	out := api.FlowList{}
	var lastID sql.NullInt64
	if err := s.r.QueryRowContext(ctx, "SELECT MAX(id) FROM flows").Scan(&lastID); err != nil {
		return out, fmt.Errorf("store: sqlite: query: %w", err)
	}
	out.LastID = idFrom(lastID.Int64)

	q := translateFilter(f, now)

	// The Matcher re-checks everything SQL could only approximate; Q is
	// handled entirely by FTS so it must not fall through to substring
	// matching on URL/headers.
	mf := f
	mf.Q = ""
	mt := Compile(mf, now)

	from := "FROM flows"
	order := "ORDER BY flows.id DESC"
	if q.fts != "" {
		from = "FROM flows JOIN flows_fts ON flows_fts.rowid = flows.id"
		q.conds = append([]string{"flows_fts MATCH ?"}, q.conds...)
		q.args = append([]any{q.fts}, q.args...)
		order = "ORDER BY bm25(flows_fts), flows.id DESC"
	}
	where := ""
	if len(q.conds) > 0 {
		where = "WHERE " + strings.Join(q.conds, " AND ")
	}

	var last flow.ID
	if !q.residual {
		countSQL := "SELECT COUNT(*) FROM (SELECT flows.id " + from + " " + where + " LIMIT " + strconv.Itoa(MaxQueryTotal) + ")" //nolint:gosec // structure only; values are bound
		if err := s.r.QueryRowContext(ctx, countSQL, q.args...).Scan(&out.Total); err != nil {
			return out, fmt.Errorf("store: sqlite: count: %w", err)
		}
		if out.Total == 0 {
			return out, nil
		}
		rows, err := s.r.QueryContext(ctx, selectFlows(from, where, order, limit), q.args...)
		if err != nil {
			return out, fmt.Errorf("store: sqlite: query: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			fl, err := scanFlow(rows)
			if err != nil {
				return out, fmt.Errorf("store: sqlite: scan: %w", err)
			}
			out.Flows = append(out.Flows, Row(fl))
			last = fl.ID
		}
		if err := rows.Err(); err != nil {
			return out, err
		}
	} else {
		rows, err := s.r.QueryContext(ctx, selectFlows(from, where, order, maxQueryScan), q.args...)
		if err != nil {
			return out, fmt.Errorf("store: sqlite: query: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			fl, err := scanFlow(rows)
			if err != nil {
				return out, fmt.Errorf("store: sqlite: scan: %w", err)
			}
			if !mt.Match(fl) {
				continue
			}
			out.Total++
			if len(out.Flows) < limit {
				out.Flows = append(out.Flows, Row(fl))
				last = fl.ID
			}
			if out.Total >= MaxQueryTotal {
				break
			}
		}
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	if out.Total > len(out.Flows) && last != 0 {
		out.Cursor = "before:" + last.Short()
	}
	return out, nil
}

// selectFlows assembles the flow page statement. Only fixed SQL fragments
// are joined; user input is always passed as bound parameters.
func selectFlows(from, where, order string, limit int) string {
	var sb strings.Builder
	sb.WriteString("SELECT ")
	sb.WriteString(flowColsQualified)
	sb.WriteByte(' ')
	sb.WriteString(from)
	sb.WriteByte(' ')
	sb.WriteString(where)
	sb.WriteByte(' ')
	sb.WriteString(order)
	sb.WriteString(" LIMIT ")
	sb.WriteString(strconv.Itoa(limit))
	return sb.String()
}

// WSMessages returns up to limit captured WebSocket messages of a flow in
// sequence order (limit <= 0 means 1000).
func (s *SQLite) WSMessages(ctx context.Context, id flow.ID, limit int) ([]flow.WSMessage, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.r.QueryContext(ctx,
		"SELECT seq, ts, dir, opcode, len, payload, masked FROM ws_messages WHERE flow_id = ? ORDER BY seq LIMIT ?",
		idArg(id), limit)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite: ws messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []flow.WSMessage
	for rows.Next() {
		var (
			m      flow.WSMessage
			ts     int64
			masked int64
		)
		if err := rows.Scan(&m.Seq, &ts, &m.Dir, &m.Opcode, &m.Len, &m.Payload, &masked); err != nil {
			return nil, fmt.Errorf("store: sqlite: ws messages: %w", err)
		}
		m.FlowID = id
		m.TS = time.UnixMicro(ts).UTC()
		m.Masked = masked != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// sqlFilter is the SQL translation of an api.FlowFilter.
type sqlFilter struct {
	conds []string
	args  []any
	fts   string
	// residual is true when the Matcher must still run over candidates.
	residual bool
}

func (q *sqlFilter) add(cond string, args ...any) {
	q.conds = append(q.conds, cond)
	q.args = append(q.args, args...)
}

// translateFilter converts the cheap parts of f into WHERE clauses and
// notes whether the Matcher is still required.
func translateFilter(f api.FlowFilter, now time.Time) sqlFilter {
	var q sqlFilter
	if strings.HasPrefix(f.Cursor, "before:") {
		if id, ok := flow.ParseShort(strings.TrimPrefix(f.Cursor, "before:")); ok {
			q.add("flows.id < ?", idArg(id))
		}
	}
	if f.Since != "" {
		if t, ok := parseTime(f.Since, now); ok {
			q.add("flows.ts_start >= ?", t.UnixMicro())
		} else if id, ok := flow.ParseShort(f.Since); ok {
			q.add("flows.id > ?", idArg(id))
		}
	}
	if f.Until != "" {
		if t, ok := parseTime(f.Until, now); ok {
			q.add("flows.ts_start <= ?", t.UnixMicro())
		}
	}
	if f.Host != "" {
		if glob.IsPattern(f.Host) {
			q.add("flows.host LIKE ? ESCAPE '\\'", globToLike(f.Host))
			q.residual = true // exact Unicode case folding is the Matcher's
		} else {
			q.add("flows.host = ? COLLATE NOCASE", f.Host)
		}
	}
	if f.Path != "" {
		switch {
		case isRegexish(f.Path):
			// matchPath's substring fallback: leave it to the Matcher.
		case !glob.IsPattern(f.Path):
			q.add("flows.path LIKE ? ESCAPE '\\'", likeEscape(f.Path)+"%")
		default:
			q.add("flows.path LIKE ? ESCAPE '\\'", globToLike(f.Path))
		}
		q.residual = true
	}
	if len(f.Method) > 0 {
		var ms []string
		var args []any
		for _, mm := range f.Method {
			for _, p := range strings.Split(mm, "|") {
				ms = append(ms, "?")
				args = append(args, strings.ToUpper(strings.TrimSpace(p)))
			}
		}
		q.add("flows.method IN ("+strings.Join(ms, ", ")+")", args...)
	}
	if cond, ok := statusSQL(f.Status); ok {
		q.add(cond)
	}
	if f.MinBytes > 0 {
		q.add("COALESCE(flows.req_size, 0) + COALESCE(flows.resp_size, 0) >= ?", f.MinBytes)
	}
	if f.HasError {
		q.add("(COALESCE(flows.error, '') != '' OR COALESCE(flows.status, 0) >= 400)")
	}
	if f.Tag != "" {
		if tagSafeForLike(f.Tag) {
			q.add("flows.tags LIKE ? ESCAPE '\\'", "%\""+likeEscape(f.Tag)+"\"%")
		}
		q.residual = true
	}
	if f.Rule != "" {
		q.residual = true
	}
	if f.Kind != "" {
		q.add("flows.kind = ?", f.Kind)
	}
	if f.Client != "" {
		if f.Client == "remote" {
			q.add("COALESCE(flows.client, '') != '' AND flows.client NOT LIKE '127.%' AND flows.client NOT LIKE '[::1]%'")
		} else {
			q.add("flows.client LIKE ? ESCAPE '\\'", likeEscape(f.Client)+":%")
		}
		q.residual = true // exact IP compare is the Matcher's
	}
	if f.Session != "" {
		q.add("flows.session = ?", f.Session)
	}
	switch f.State {
	case "", "all":
	case "held", "active", "done", "failed":
		q.add("flows.state = ?", f.State)
	case "replayed":
		q.add("flows.replay = 1")
	case "mocked", "blocked":
		q.add("flows.rules IS NOT NULL")
		q.residual = true
	}
	if f.ContentType != "" {
		q.residual = true
	}
	if f.Q != "" {
		q.fts = ftsQuery(f.Q)
	}
	return q
}

// statusSQL mirrors StatusMatcher for the forms it accepts.
func statusSQL(spec string) (string, bool) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		return "", false
	}
	neg := strings.HasPrefix(spec, "!")
	spec = strings.TrimPrefix(spec, "!")
	const col = "COALESCE(flows.status, 0)"
	var parts []string
	for _, part := range strings.Split(spec, "|") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasSuffix(part, "xx") && len(part) == 3:
			if part[0] < '0' || part[0] > '9' {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s / 100 = %d", col, part[0]-'0'))
		case strings.Contains(part, "-"):
			lo, hi, ok := strings.Cut(part, "-")
			l, e1 := strconv.Atoi(lo)
			h, e2 := strconv.Atoi(hi)
			if ok && e1 == nil && e2 == nil {
				parts = append(parts, fmt.Sprintf("%s BETWEEN %d AND %d", col, l, h))
			}
		default:
			if n, err := strconv.Atoi(part); err == nil {
				parts = append(parts, fmt.Sprintf("%s = %d", col, n))
			}
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	cond := "(" + strings.Join(parts, " OR ") + ")"
	if neg {
		cond = "NOT " + cond
	}
	// Flows without a status (tunnels, transport failures) never match a
	// status filter, mirroring Matcher.
	return "(" + col + " > 0 AND " + cond + ")", true
}

func isRegexish(pattern string) bool {
	return strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 &&
		strings.ContainsAny(pattern, "^$[](){}|+\\")
}

// likeEscape escapes LIKE metacharacters using '\' as the ESCAPE char.
func likeEscape(s string) string {
	var sb strings.Builder
	for _, c := range s {
		switch c {
		case '\\', '%', '_':
			sb.WriteByte('\\')
		}
		sb.WriteRune(c)
	}
	return sb.String()
}

// globToLike converts pano's glob ('*' any run, '?' one char) to LIKE.
func globToLike(p string) string {
	var sb strings.Builder
	for _, c := range p {
		switch c {
		case '*':
			sb.WriteByte('%')
		case '?':
			sb.WriteByte('_')
		case '\\', '%', '_':
			sb.WriteByte('\\')
			sb.WriteRune(c)
		default:
			sb.WriteRune(c)
		}
	}
	return sb.String()
}

// tagSafeForLike reports whether a tag is stored verbatim by encoding/json
// (no escaping), so a LIKE over the JSON array is a sound prefilter.
func tagSafeForLike(tag string) bool {
	for _, c := range tag {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("_-.:/@ ", c):
		default:
			return false
		}
	}
	return true
}

// ftsQuery turns free text into a safe FTS5 expression: each whitespace
// separated term becomes a quoted phrase (so punctuation such as '.' or '/'
// is tokenized rather than parsed as syntax); a trailing '*' keeps prefix
// semantics; terms are implicitly ANDed.
func ftsQuery(q string) string {
	var terms []string
	for _, tok := range strings.Fields(q) {
		prefix := strings.HasSuffix(tok, "*")
		tok = strings.TrimRight(tok, "*")
		if tok == "" {
			continue
		}
		t := `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
		if prefix {
			t += "*"
		}
		terms = append(terms, t)
	}
	if len(terms) == 0 {
		return `""`
	}
	return strings.Join(terms, " ")
}
