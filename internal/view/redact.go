package view

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Extra holds user-configured additions to the built-in redaction rules.
// Headers are extra header names to mask (case-insensitive) and Patterns are
// extra regular expressions whose whole match is masked by RedactText.
// Set it once at start-up, before any concurrent use of this package.
var Extra struct {
	Headers  []string
	Patterns []*regexp.Regexp
}

// secretHeaders are always masked by RedactHeaders (lower-case names).
var secretHeaders = map[string]bool{
	"authorization":        true,
	"proxy-authorization":  true,
	"cookie":               true,
	"set-cookie":           true,
	"x-api-key":            true,
	"api-key":              true,
	"x-goog-api-key":       true,
	"x-auth-token":         true,
	"openai-organization":  true,
	"x-amz-security-token": true,
	"x-csrf-token":         true,
}

// knownPrefixes are kept visible by Mask so a reader can still tell what
// kind of credential was masked.
var knownPrefixes = []string{
	"sk-ant-", "sk-proj-", "sk-",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_",
	"xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-",
	"AKIA", "AIza", "key-", "eyJ",
}

const maskMark = "…"

var (
	// Bearer tokens (RFC 6750 b64token characters). The mask marker is part
	// of the class so an already-masked token is recognised and skipped.
	reBearer = regexp.MustCompile(`(?i)\b(bearer)[ \t]+([A-Za-z0-9._~+/=…-]+)`)
	// Well-known credential shapes.
	reToken = regexp.MustCompile(`\b(?:` +
		`sk-ant-[A-Za-z0-9_-]{8,}` +
		`|sk-[A-Za-z0-9_-]{16,}` +
		`|AKIA[0-9A-Z]{16}` +
		`|AIza[0-9A-Za-z_-]{35}` +
		`|gh[pousr]_[A-Za-z0-9]{36}` +
		`|xox[baprs]-[A-Za-z0-9-]{10,}` +
		`|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+` +
		`|key-[A-Za-z0-9]{32}` +
		`)`)
	secretKeys = `password|passwd|secret|token|access_token|refresh_token|client_secret|api_key|apikey|private_key`
	// "key": "value" in JSON.
	reJSONKV = regexp.MustCompile(`(?i)"(` + secretKeys + `)"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	// \"key\":\"value\" — JSON embedded in a JSON string (echo endpoints,
	// logged payloads); the value runs until the next escaped quote.
	reJSONKVEsc = regexp.MustCompile(`(?i)\\"(` + secretKeys + `)\\"\s*:\s*\\"((?:[^"\\]|\\[^"])*)\\"`)
	// key=value in query strings and forms.
	reFormKV = regexp.MustCompile(`(?i)(^|[?&;,\s])(` + secretKeys + `)=([^&\s"'<>]+)`)
	// scheme://user:password@host
	reURLAuth = regexp.MustCompile(`://([^/\s:@]+):([^/\s@]+)@`)
	// Rendered "key: value", "key: str "value"" and "Header: value" lines,
	// as produced by the summary, form and header views. The value is the
	// quoted string when present, otherwise the rest of the line.
	lineKeys = secretKeys + `|authorization|cookie|set-cookie|x-api-key|api-key`
	quoted   = `"(?:[^"\\]|\\.)*"`
	reLineKV = regexp.MustCompile(`(?im)(^[ \t]*|[\s,;{(\[])(` + lineKeys + `)[ \t]*:[ \t]*(?:(?:str|int|float|bool)[ \t]+)?(` + quoted + `(?:\|` + quoted + `)*|[^\n]+)`)
	reQuoted = regexp.MustCompile(quoted)
	// Unquoted values that are type descriptors, shapes or punctuation
	// (schema and summary output), not secrets.
	reTypeWord = regexp.MustCompile(`^(?:(?:str|int|float|bool|null|object|array)\b|[{}\[\],|])`)
)

// sensitiveKeys are JSON/form keys whose values are masked at render time.
var sensitiveKeys = map[string]bool{
	"password": true, "passwd": true, "secret": true, "token": true,
	"access_token": true, "refresh_token": true, "client_secret": true,
	"api_key": true, "apikey": true, "private_key": true,
	"authorization": true, "cookie": true, "set-cookie": true,
}

// isSensitiveKey reports whether a key's value must never be shown.
func isSensitiveKey(k string) bool { return sensitiveKeys[strings.ToLower(k)] }

// masker masks values of sensitive keys while a view is being rendered,
// before the rendered text passes through RedactText, so that value
// formats RedactText cannot recognise never reach the output.
type masker struct {
	on bool // opts.redacting()
	n  int  // values masked
}

// value returns the masked form of v when masking is on and key is
// sensitive; otherwise v unchanged. The second result reports masking.
func (m *masker) value(key, v string) (string, bool) {
	if m == nil || !m.on || v == "" || !isSensitiveKey(key) || isMasked(v) {
		return v, false
	}
	m.n++
	return Mask(v), true
}

// Mask replaces a secret with a short, stable placeholder: a recognisable
// prefix (when the value has one), the last four characters, and the first
// four hex digits of the value's SHA-256, e.g. "sk-ant-…a1b2 hash:9f3c".
// Short values keep no prefix or suffix so nothing meaningful leaks.
func Mask(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	h := hex.EncodeToString(sum[:2])

	r := []rune(value)
	prefix := ""
	for _, p := range knownPrefixes {
		if strings.HasPrefix(value, p) {
			prefix = p
			break
		}
	}
	if prefix == "" && len(r) > 12 {
		prefix = string(r[:3])
	}
	suffix := ""
	if rest := len(r) - len([]rune(prefix)); rest >= 8 {
		suffix = string(r[len(r)-4:])
	}
	return prefix + maskMark + suffix + " hash:" + h
}

// isMasked reports whether s already carries a Mask placeholder.
func isMasked(s string) bool {
	return strings.Contains(s, maskMark) || strings.Contains(s, "hash:")
}

// RedactText masks credentials found anywhere in s: bearer tokens, common
// API-key shapes (OpenAI, Anthropic, AWS, Google, GitHub, Slack, Mailgun),
// JWTs, basic-auth userinfo in URLs, JSON and form fields named like
// secrets, and any Extra.Patterns. It returns the masked text and the
// number of replacements. Running it over its own output changes nothing.
func RedactText(s string) (string, int) {
	n := 0
	s = reBearer.ReplaceAllStringFunc(s, func(m string) string {
		idx := reBearer.FindStringSubmatchIndex(m)
		tok := m[idx[4]:idx[5]]
		if isMasked(tok) {
			return m
		}
		n++
		return m[:idx[4]] + Mask(tok)
	})
	s = reToken.ReplaceAllStringFunc(s, func(m string) string {
		n++
		return Mask(m)
	})
	s = reJSONKV.ReplaceAllStringFunc(s, func(m string) string {
		return maskGroup(reJSONKV, m, 2, &n)
	})
	s = reJSONKVEsc.ReplaceAllStringFunc(s, func(m string) string {
		return maskGroup(reJSONKVEsc, m, 2, &n)
	})
	s = reFormKV.ReplaceAllStringFunc(s, func(m string) string {
		return maskGroup(reFormKV, m, 3, &n)
	})
	s = reURLAuth.ReplaceAllStringFunc(s, func(m string) string {
		return maskGroup(reURLAuth, m, 2, &n)
	})
	s = reLineKV.ReplaceAllStringFunc(s, func(m string) string {
		idx := reLineKV.FindStringSubmatchIndex(m)
		if idx == nil {
			return m
		}
		val := strings.TrimRight(m[idx[6]:idx[7]], " \t\r")
		if val == "" || isMasked(val) {
			return m
		}
		if val[0] == '"' {
			// One or more quoted values ("a"|"b"): mask each.
			masked := reQuoted.ReplaceAllStringFunc(val, func(q string) string {
				if len(q) < 3 {
					return q
				}
				n++
				return `"` + Mask(q[1:len(q)-1]) + `"`
			})
			return m[:idx[6]] + masked + m[idx[6]+len(val):]
		}
		if reTypeWord.MatchString(val) {
			return m
		}
		n++
		return m[:idx[6]] + Mask(val) + m[idx[6]+len(val):]
	})
	for _, re := range Extra.Patterns {
		if re == nil {
			continue
		}
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if m == "" || isMasked(m) {
				return m
			}
			n++
			return Mask(m)
		})
	}
	return s, n
}

// maskGroup masks capture group g of match m, leaving the rest intact.
func maskGroup(re *regexp.Regexp, m string, g int, n *int) string {
	idx := re.FindStringSubmatchIndex(m)
	if idx == nil || idx[2*g] < 0 {
		return m
	}
	val := m[idx[2*g]:idx[2*g+1]]
	if val == "" || isMasked(val) {
		return m
	}
	*n++
	return m[:idx[2*g]] + Mask(val) + m[idx[2*g+1]:]
}

// isSecretHeader reports whether a header name is masked wholesale.
func isSecretHeader(name string) bool {
	lname := strings.ToLower(name)
	if secretHeaders[lname] {
		return true
	}
	for _, h := range Extra.Headers {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// RedactHeaders returns a clone of h with credential-bearing values masked
// and the number of values changed. Authorization keeps its scheme, cookies
// keep their names and Set-Cookie keeps its attributes, so the shape of the
// header stays readable. Values of other headers are passed through
// RedactText. When reveal is true the clone is returned untouched.
func RedactHeaders(h http.Header, reveal bool) (http.Header, int) {
	out := h.Clone()
	if reveal || out == nil {
		return out, 0
	}
	n := 0
	for name, vals := range out {
		secret := isSecretHeader(name)
		for i, v := range vals {
			if v == "" {
				continue
			}
			if secret {
				if isMasked(v) {
					continue
				}
				if m := maskHeaderValue(name, v); m != v {
					vals[i] = m
					n++
				}
				continue
			}
			r, k := RedactText(v)
			vals[i] = r
			n += k
		}
	}
	return out, n
}

// maskHeaderValue masks a single secret header value, preserving structure.
func maskHeaderValue(name, v string) string {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization":
		if scheme, rest, ok := strings.Cut(v, " "); ok && rest != "" {
			return scheme + " " + Mask(strings.TrimSpace(rest))
		}
	case "cookie":
		parts := strings.Split(v, ";")
		for i, p := range parts {
			parts[i] = maskCookiePair(strings.TrimSpace(p))
		}
		return strings.Join(parts, "; ")
	case "set-cookie":
		first, attrs, hasAttrs := strings.Cut(v, ";")
		first = maskCookiePair(first)
		if hasAttrs {
			return first + ";" + attrs
		}
		return first
	}
	return Mask(v)
}

// maskCookiePair masks the value of a "name=value" pair, keeping the name.
// Empty values are left alone; a bare token without "=" is masked whole.
func maskCookiePair(p string) string {
	k, val, ok := strings.Cut(p, "=")
	switch {
	case !ok:
		if p == "" {
			return p
		}
		return Mask(p)
	case val == "":
		return p
	}
	return k + "=" + Mask(val)
}

// FormatHeaders renders headers as "Name: value" lines, one per value,
// sorted by name (case-insensitively).
func FormatHeaders(h http.Header) string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		li, lj := strings.ToLower(names[i]), strings.ToLower(names[j])
		if li != lj {
			return li < lj
		}
		return names[i] < names[j]
	})
	var sb strings.Builder
	for _, k := range names {
		for _, v := range h[k] {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v)
		}
	}
	return sb.String()
}
