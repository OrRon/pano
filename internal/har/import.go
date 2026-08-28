package har

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/mimeclass"
)

// ErrNotHAR is returned when the input parses as JSON but has no "log" object.
var ErrNotHAR = errors.New("har: document has no log object")

// Import parses a HAR document into flows. It tolerates missing or mistyped
// fields, both literal and base64 bodies, and browser-specific extensions
// (Chrome's _initiator, _priority, _resourceType and friends are ignored).
// Bodies are returned decoded; the caller stores them and assigns IDs.
//
// Timing is reconstructed from startedDateTime, time and the timings phases,
// so TTFB and totals survive a round trip. Header maps keep their
// Content-Encoding header even though the returned bytes are decoded; the
// BodyRef.Encoding field is left empty to reflect what the caller stores.
func Import(r io.Reader) ([]Imported, error) {
	br := bufio.NewReader(r)
	if bom, err := br.Peek(3); err == nil && bytes.Equal(bom, []byte("\xef\xbb\xbf")) {
		_, _ = br.Discard(3)
	}

	var doc Document
	dec := json.NewDecoder(br)
	if err := dec.Decode(&doc); err != nil {
		// A type mismatch on one field zeroes that field and keeps going;
		// treat it as tolerable so a single odd value does not sink the file.
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			return nil, fmt.Errorf("har: parse: %w", err)
		}
	}
	if doc.Log == nil {
		return nil, ErrNotHAR
	}

	out := make([]Imported, 0, len(doc.Log.Entries))
	for i := range doc.Log.Entries {
		out = append(out, importEntry(&doc.Log.Entries[i]))
	}
	return out, nil
}

// importEntry converts one HAR entry into a flow plus its decoded bodies.
func importEntry(e *Entry) Imported {
	f := &flow.Flow{
		Kind:   flow.KindHTTP,
		State:  flow.StateDone,
		Method: strings.ToUpper(strings.TrimSpace(e.Request.Method)),
		Proto:  e.Request.HTTPVersion,
		Client: e.Connection,
		Status: e.Response.Status,
	}
	if e.Response.HTTPVersion != "" && e.Response.HTTPVersion != f.Proto {
		f.UpProto = e.Response.HTTPVersion
	}
	setURL(f, e.Request.URL)

	f.ReqHeaders = headerMap(e.Request.Headers)
	if len(f.ReqHeaders["Cookie"]) == 0 && len(e.Request.Cookies) > 0 {
		f.ReqHeaders.Set("Cookie", joinCookies(e.Request.Cookies))
	}
	f.RespHeaders = headerMap(e.Response.Headers)

	var im Imported
	if pd := e.Request.PostData; pd != nil {
		im.ReqBody = decodeBody(pd.Text, pd.Encoding)
		f.ReqBody = bodyRef(im.ReqBody, pd.MimeType, f.ReqHeaders.Get("Content-Type"), 0, e.Request.BodySize)
	} else if e.Request.BodySize > 0 {
		f.ReqBody = bodyRef(nil, "", f.ReqHeaders.Get("Content-Type"), 0, e.Request.BodySize)
	}
	c := e.Response.Content
	im.RespBody = decodeBody(c.Text, c.Encoding)
	if len(im.RespBody) > 0 || c.Size > 0 || e.Response.BodySize > 0 {
		f.RespBody = bodyRef(im.RespBody, c.MimeType, f.RespHeaders.Get("Content-Type"), c.Size, e.Response.BodySize)
	}

	f.T = importTiming(e)

	if p := e.Pano; p != nil {
		if p.Kind != "" {
			f.Kind = p.Kind
		}
		if p.State != "" {
			f.State = p.State
		}
		f.Session = p.Session
		f.Error = p.Error
		f.Tags = append([]string(nil), p.Tags...)
		f.Rules = append([]flow.RuleHit(nil), p.Rules...)
		f.ReqBody.Truncated = p.ReqTruncated
		f.RespBody.Truncated = p.RespTruncated
		f.Replay = p.Replay
		f.ReplayOf = p.ReplayOf
	} else {
		f.Error = e.Response.Error
		switch {
		case f.Method == http.MethodConnect:
			f.Kind = flow.KindTunnel
		case f.Status == http.StatusSwitchingProtocols,
			strings.EqualFold(f.ReqHeaders.Get("Upgrade"), "websocket"):
			f.Kind = flow.KindWebSocket
		}
	}
	if f.Error != "" && f.State == flow.StateDone {
		f.State = flow.StateFailed
	}
	im.Flow = f
	return im
}

// setURL splits an absolute URL into the flow's scheme/host/port/path/query.
// WebSocket schemes are normalised to http(s) and mark the flow as a
// WebSocket; an unparsable URL is kept verbatim in Path.
func setURL(f *flow.Flow, raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		f.Path = raw
		return
	}
	f.Scheme = strings.ToLower(u.Scheme)
	switch f.Scheme {
	case "ws":
		f.Scheme, f.Kind = "http", flow.KindWebSocket
	case "wss":
		f.Scheme, f.Kind = "https", flow.KindWebSocket
	}
	f.Host = u.Hostname()
	if p, err := strconv.Atoi(u.Port()); err == nil && p > 0 {
		f.Port = p
	} else if f.Scheme == "https" {
		f.Port = 443
	} else if f.Scheme == "http" {
		f.Port = 80
	}
	f.Path = u.EscapedPath()
	f.Query = u.RawQuery
}

// headerMap builds an http.Header, dropping HTTP/2 pseudo-headers that some
// browsers include verbatim.
func headerMap(pairs []NVP) http.Header {
	h := make(http.Header, len(pairs))
	for _, p := range pairs {
		if p.Name == "" || strings.HasPrefix(p.Name, ":") {
			continue
		}
		h.Add(p.Name, p.Value)
	}
	return h
}

// joinCookies renders cookies as a Cookie header value.
func joinCookies(cs []Cookie) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.Name == "" {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// decodeBody turns HAR body text into bytes. Base64 that fails to decode is
// kept as the literal text rather than dropped.
func decodeBody(text, encoding string) []byte {
	if text == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(encoding), "base64") {
		return []byte(text)
	}
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		}
		return r
	}, text)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(clean); err == nil {
			return b
		}
	}
	return []byte(text)
}

// bodyRef describes an imported body. The declared sizes win over the byte
// count when present so a body the exporter omitted still reports its size.
func bodyRef(b []byte, mime, contentType string, declared, wire int64) flow.BodyRef {
	ref := flow.BodyRef{Captured: int64(len(b))}
	switch {
	case declared > 0:
		ref.Size = declared
	case wire > 0:
		ref.Size = wire
	default:
		ref.Size = int64(len(b))
	}
	ref.MIME = mimeclass.MediaType(firstNonEmpty(mime, contentType))
	return ref
}

// importTiming rebuilds phase timestamps from startedDateTime, time and the
// timings object. Phases reported as -1 leave their timestamps zero.
func importTiming(e *Entry) flow.Timing {
	var t flow.Timing
	start, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(e.StartedDateTime))
	if err != nil {
		return t
	}
	t.Start = start
	tm := e.Timings
	cursor := start
	advance := func(v float64) bool {
		if v < 0 {
			return false
		}
		cursor = cursor.Add(time.Duration(v * float64(time.Millisecond)))
		return true
	}
	advance(tm.Blocked)
	if advance(tm.DNS) {
		t.DNSDone = cursor
	}
	if advance(tm.Connect) {
		if tm.SSL >= 0 {
			t.TLSDone = cursor
			t.Connected = cursor.Add(-time.Duration(tm.SSL * float64(time.Millisecond)))
		} else {
			t.Connected = cursor
		}
	}
	if advance(tm.Send) {
		t.WroteReq = cursor
	}
	if advance(tm.Wait) {
		t.FirstByte = cursor
	}
	switch {
	case e.Time > 0:
		t.End = start.Add(time.Duration(e.Time * float64(time.Millisecond)))
	case advance(tm.Receive):
		t.End = cursor
	case e.Time == 0:
		// A zero (or absent) total with no receive phase: the exchange
		// finished as soon as it started, as far as the HAR can tell.
		t.End = start
	}
	return t
}
