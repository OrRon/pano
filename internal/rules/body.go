package rules

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/orron/pano/internal/flow"
)

var errBodyTooLarge = errors.New("body exceeds 8 MiB")

// slurp reads rc fully, up to MaxBodyBytes. On success replay is nil. On a
// read error or an oversized body, replay yields the consumed bytes followed
// by the rest of rc so the exchange can continue untouched.
func slurp(rc io.ReadCloser) (data []byte, replay io.ReadCloser, err error) {
	if rc == nil || rc == http.NoBody {
		return nil, nil, nil
	}
	data, err = io.ReadAll(io.LimitReader(rc, MaxBodyBytes+1))
	if err == nil && len(data) > MaxBodyBytes {
		err = errBodyTooLarge
	}
	if err != nil {
		return data, readCloser{io.MultiReader(bytes.NewReader(data), rc), rc}, err
	}
	return data, nil, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// decodeBody undoes a Content-Encoding. decoded reports whether the bytes
// changed representation (and the header must be dropped when re-serving).
func decodeBody(data []byte, encoding string) (plain []byte, decoded bool, err error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return data, false, nil
	case "gzip", "x-gzip":
		if len(data) == 0 {
			return data, true, nil
		}
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, false, fmt.Errorf("gunzip: %w", err)
		}
		out, err := io.ReadAll(io.LimitReader(zr, MaxBodyBytes+1))
		if err != nil {
			return nil, false, fmt.Errorf("gunzip: %w", err)
		}
		if len(out) > MaxBodyBytes {
			return nil, false, errBodyTooLarge
		}
		return out, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported content-encoding %q", encoding)
	}
}

// bodySide adapts either half of an exchange so body edits work on both.
// mirror is the flow's captured copy of the headers, kept in sync.
type bodySide struct {
	header http.Header
	mirror http.Header
	body   *io.ReadCloser
	length *int64
	te     *[]string
	ref    *flow.BodyRef
	req    *http.Request
}

func requestSide(f *flow.Flow, r *http.Request) bodySide {
	return bodySide{header: r.Header, mirror: f.ReqHeaders, body: &r.Body, length: &r.ContentLength, te: &r.TransferEncoding, ref: &f.ReqBody, req: r}
}

func responseSide(f *flow.Flow, resp *http.Response) bodySide {
	return bodySide{header: resp.Header, mirror: f.RespHeaders, body: &resp.Body, length: &resp.ContentLength, te: &resp.TransferEncoding, ref: &f.RespBody}
}

func (s bodySide) setHeader(name, value string) {
	s.header.Set(name, value)
	if s.mirror != nil {
		s.mirror.Set(name, value)
	}
}

func (s bodySide) delHeader(name string) {
	s.header.Del(name)
	if s.mirror != nil {
		s.mirror.Del(name)
	}
}

// restore puts already-consumed bytes back without touching headers.
func (s bodySide) restore(data []byte) {
	if len(data) == 0 {
		*s.body = http.NoBody
		return
	}
	*s.body = io.NopCloser(bytes.NewReader(data))
}

// set replaces the body with data and fixes the framing headers. decoded
// drops Content-Encoding because data is now plain.
func (s bodySide) set(data []byte, decoded bool) {
	s.restore(data)
	*s.length = int64(len(data))
	*s.te = nil
	s.setHeader("Content-Length", strconv.Itoa(len(data)))
	if decoded {
		s.delHeader("Content-Encoding")
		if s.ref != nil {
			s.ref.Encoding = ""
		}
	}
	if s.req != nil {
		s.req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	}
}

// rewrite reads, decodes, transforms and re-installs the body. The returned
// note describes the outcome for the RuleHit; on failure the body is left as
// it was.
func (s bodySide) rewrite(fn func([]byte) ([]byte, error)) string {
	data, replay, err := slurp(*s.body)
	if err != nil {
		*s.body = replay
		return "skipped: " + err.Error()
	}
	plain, decoded, err := decodeBody(data, s.header.Get("Content-Encoding"))
	if err != nil {
		s.restore(data)
		return "skipped: " + err.Error()
	}
	out, err := fn(plain)
	if err != nil {
		s.restore(data)
		return "skipped: " + err.Error()
	}
	s.set(out, decoded)
	return fmt.Sprintf("%d -> %d bytes", len(plain), len(out))
}

// templateData is what rewrite_body templates see.
type templateData struct {
	Host   string
	Path   string
	Method string
	Status int
	Body   string
	header http.Header
}

// Header returns a header value (request headers in the request phase,
// response headers in the response phase).
func (d templateData) Header(name string) string {
	if d.header == nil {
		return ""
	}
	return d.header.Get(name)
}

// transform builds the body function for a rewrite_body action.
func (a *action) transform(td templateData) func([]byte) ([]byte, error) {
	switch {
	case a.re != nil:
		repl := []byte(a.spec.Replace)
		return func(b []byte) ([]byte, error) { return a.re.ReplaceAll(b, repl), nil }
	case a.tmpl != nil:
		return func(b []byte) ([]byte, error) {
			td.Body = string(b)
			var buf bytes.Buffer
			if err := a.tmpl.Execute(&buf, td); err != nil {
				return nil, fmt.Errorf("template: %w", err)
			}
			return buf.Bytes(), nil
		}
	default:
		patch := a.spec.JSONPatch
		return func(b []byte) ([]byte, error) { return jsonPatch(b, patch) }
	}
}

// jsonPatch sets dotted paths ("choices.0.message.content") in a JSON body.
// Missing objects are created; a numeric segment indexes an array and may
// equal its length to append. An empty body is treated as {}.
func jsonPatch(data []byte, patch map[string]any) ([]byte, error) {
	var doc any
	if len(bytes.TrimSpace(data)) == 0 {
		doc = map[string]any{}
	} else if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("body is not JSON: %w", err)
	}
	paths := slices.Sorted(func(yield func(string) bool) {
		for k := range patch {
			if !yield(k) {
				return
			}
		}
	})
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return nil, errors.New("json_patch: empty path")
		}
		var err error
		if doc, err = setPath(doc, strings.Split(p, "."), patch[p], p); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func setPath(node any, segs []string, v any, full string) (any, error) {
	if len(segs) == 0 {
		return v, nil
	}
	seg, rest := segs[0], segs[1:]
	idx, isIndex := strconv.Atoi(seg)
	switch n := node.(type) {
	case nil:
		if isIndex == nil && idx == 0 {
			child, err := setPath(nil, rest, v, full)
			if err != nil {
				return nil, err
			}
			return []any{child}, nil
		}
		m := map[string]any{}
		child, err := setPath(nil, rest, v, full)
		if err != nil {
			return nil, err
		}
		m[seg] = child
		return m, nil
	case map[string]any:
		child, err := setPath(n[seg], rest, v, full)
		if err != nil {
			return nil, err
		}
		n[seg] = child
		return n, nil
	case []any:
		if isIndex != nil || idx < 0 || idx > len(n) {
			return nil, fmt.Errorf("json_patch: %s: %q is not a valid index into an array of %d", full, seg, len(n))
		}
		if idx == len(n) {
			child, err := setPath(nil, rest, v, full)
			if err != nil {
				return nil, err
			}
			return append(n, child), nil
		}
		child, err := setPath(n[idx], rest, v, full)
		if err != nil {
			return nil, err
		}
		n[idx] = child
		return n, nil
	default:
		return nil, fmt.Errorf("json_patch: %s: cannot descend into %T at %q", full, node, seg)
	}
}

// buildMock assembles a synthetic response. Content-Type defaults to JSON when
// the body parses as JSON, text/plain otherwise.
func buildMock(status int, headers map[string]string, body string, r *http.Request) *http.Response {
	if status == 0 {
		status = http.StatusOK
	}
	h := make(http.Header, len(headers)+2)
	for k, v := range headers {
		h.Set(k, v)
	}
	if h.Get("Content-Type") == "" {
		if looksJSON(body) {
			h.Set("Content-Type", "application/json")
		} else {
			h.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	return &http.Response{
		Status:        strconv.Itoa(status) + " " + http.StatusText(status),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}
}

func looksJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return false
	}
	return json.Valid([]byte(t))
}
