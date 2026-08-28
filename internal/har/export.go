package har

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/mimeclass"
)

// timeLayout is ISO 8601 with millisecond precision, as HAR requires.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// Export writes a HAR 1.2 document for flows, in the given order, to w and
// returns the number of entries written. Nil flows are skipped. The document
// is streamed entry by entry, so memory use is bounded by the largest single
// entry rather than the whole capture.
func Export(w io.Writer, flows []*flow.Flow, opts ExportOptions) (int, error) {
	ex := exporter{opts: opts}
	if ex.opts.MaxBodyBytes == 0 {
		ex.opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if ex.opts.Creator == "" {
		ex.opts.Creator = "pano"
	}

	bw := bufio.NewWriterSize(w, 64<<10)
	creator, err := json.Marshal(Creator{Name: ex.opts.Creator, Version: ex.opts.Version})
	if err != nil {
		return 0, err
	}
	if _, err := fmt.Fprintf(bw, `{"log":{"version":%q,"creator":%s,"entries":[`, Version, creator); err != nil {
		return 0, err
	}

	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	n := 0
	for _, f := range flows {
		if f == nil {
			continue
		}
		if n > 0 {
			if err := bw.WriteByte(','); err != nil {
				return n, err
			}
		}
		if err := enc.Encode(ex.entry(f)); err != nil {
			return n, err
		}
		n++
	}
	if _, err := bw.WriteString("]}}\n"); err != nil {
		return n, err
	}
	return n, bw.Flush()
}

type exporter struct {
	opts ExportOptions
}

// entry converts one flow into a HAR entry.
func (ex *exporter) entry(f *flow.Flow) Entry {
	reqH, respH := f.ReqHeaders, f.RespHeaders
	if ex.opts.Redact != nil && ex.opts.Redact.Headers != nil {
		reqH = ex.opts.Redact.Headers(reqH)
		respH = ex.opts.Redact.Headers(respH)
	}

	method := f.Method
	if method == "" {
		switch f.Kind {
		case flow.KindTunnel:
			method = http.MethodConnect
		case flow.KindWebSocket:
			method = http.MethodGet
		case flow.KindHTTP:
			// HTTP flows always record their method; nothing to infer.
		}
	}

	req := Request{
		Method:      method,
		URL:         f.URL(),
		HTTPVersion: f.Proto,
		Cookies:     requestCookies(reqH),
		Headers:     headerPairs(reqH),
		QueryString: queryPairs(f.Query),
		HeadersSize: -1,
		BodySize:    f.ReqBody.Size,
	}
	if hasBody(f.ReqBody) {
		mime := firstNonEmpty(reqH.Get("Content-Type"), f.ReqBody.MIME)
		b := ex.body(f.ReqBody, mime)
		req.PostData = &PostData{MimeType: mime, Text: b.text, Encoding: b.encoding, Comment: b.comment}
	}

	respProto := firstNonEmpty(f.UpProto, f.Proto)
	resp := Response{
		Status:      f.Status,
		StatusText:  http.StatusText(f.Status),
		HTTPVersion: respProto,
		Cookies:     responseCookies(respH),
		Headers:     headerPairs(respH),
		RedirectURL: respH.Get("Location"),
		HeadersSize: -1,
		BodySize:    f.RespBody.Size,
		Content: Content{
			Size:     f.RespBody.Size,
			MimeType: firstNonEmpty(respH.Get("Content-Type"), f.RespBody.MIME),
		},
	}
	if hasBody(f.RespBody) {
		b := ex.body(f.RespBody, resp.Content.MimeType)
		resp.Content.Text = b.text
		resp.Content.Encoding = b.encoding
		resp.Content.Comment = b.comment
		if b.decodedSize >= 0 {
			resp.Content.Size = b.decodedSize
			if f.RespBody.Encoding != "" && b.decodedSize >= f.RespBody.Size {
				saved := b.decodedSize - f.RespBody.Size
				resp.Content.Compression = &saved
			}
		}
	}

	total := float64(-1)
	if !f.T.End.IsZero() {
		total = ms(f.T.End.Sub(f.T.Start))
	}

	return Entry{
		StartedDateTime: f.T.Start.Format(timeLayout),
		Time:            total,
		Request:         req,
		Response:        resp,
		Timings:         timings(f.T),
		Connection:      f.Client,
		Pano: &PanoExt{
			ID:            f.ID,
			Short:         f.ID.Short(),
			Kind:          f.Kind,
			State:         f.State,
			Session:       f.Session,
			Error:         f.Error,
			Tags:          f.Tags,
			Rules:         f.Rules,
			ReqTruncated:  f.ReqBody.Truncated,
			RespTruncated: f.RespBody.Truncated,
			Replay:        f.Replay,
			ReplayOf:      f.ReplayOf,
		},
	}
}

// exportedBody is the inline form of one body.
type exportedBody struct {
	text        string
	encoding    string // "base64" or ""
	comment     string
	decodedSize int64 // -1 when the bytes were not fetched
}

// body resolves and encodes one body according to the export options.
func (ex *exporter) body(ref flow.BodyRef, mime string) exportedBody {
	out := exportedBody{decodedSize: -1}
	var notes []string
	if ref.Truncated {
		notes = append(notes, fmt.Sprintf("body truncated at capture (%d of %d bytes)", ref.Captured, ref.Size))
	}
	switch {
	case ex.opts.MaxBodyBytes < 0:
		notes = append(notes, "body omitted")
	case ex.opts.Body == nil || ref.Hash == "":
		notes = append(notes, "body not available")
	default:
		b, ok := ex.opts.Body(ref.Hash)
		switch {
		case !ok:
			notes = append(notes, "body not available")
		case len(b) > ex.opts.MaxBodyBytes:
			out.decodedSize = int64(len(b))
			notes = append(notes, fmt.Sprintf("body omitted: %d bytes exceeds inline cap of %d bytes", len(b), ex.opts.MaxBodyBytes))
		default:
			out.decodedSize = int64(len(b))
			if isText(mime) && utf8.Valid(b) {
				out.text = string(b)
				if ex.opts.Redact != nil && ex.opts.Redact.Text != nil {
					out.text = ex.opts.Redact.Text(out.text)
				}
			} else {
				out.text = base64.StdEncoding.EncodeToString(b)
				out.encoding = "base64"
			}
		}
	}
	out.comment = strings.Join(notes, "; ")
	return out
}

// hasBody reports whether a BodyRef describes any body at all.
func hasBody(ref flow.BodyRef) bool {
	return ref.Size > 0 || ref.Captured > 0 || ref.Hash != ""
}

// isText reports whether a MIME type is one pano would show inline as text.
func isText(mime string) bool {
	return mimeclass.IsTextual(mimeclass.Of(mime))
}

// timings derives HAR phase durations from a flow's timestamps. Phases whose
// bounding timestamps were not recorded are reported as -1.
func timings(t flow.Timing) Timings {
	tm := Timings{Blocked: -1, DNS: -1, Connect: -1, Send: -1, Wait: -1, Receive: -1, SSL: -1}
	cursor := t.Start
	if !t.DNSDone.IsZero() {
		tm.DNS = ms(t.DNSDone.Sub(cursor))
		cursor = t.DNSDone
	}
	if !t.Connected.IsZero() {
		end := t.Connected
		if !t.TLSDone.IsZero() {
			tm.SSL = ms(t.TLSDone.Sub(t.Connected))
			end = t.TLSDone // HAR: connect includes the TLS handshake
		}
		tm.Connect = ms(end.Sub(cursor))
		cursor = end
	} else if !t.TLSDone.IsZero() {
		tm.Connect = ms(t.TLSDone.Sub(cursor))
		cursor = t.TLSDone
	}
	if !t.WroteReq.IsZero() {
		tm.Send = ms(t.WroteReq.Sub(cursor))
		cursor = t.WroteReq
	}
	if !t.FirstByte.IsZero() {
		tm.Wait = ms(t.FirstByte.Sub(cursor))
		cursor = t.FirstByte
	}
	if !t.End.IsZero() && !t.FirstByte.IsZero() {
		tm.Receive = ms(t.End.Sub(cursor))
	}
	return tm
}

// ms converts a duration to milliseconds with microsecond precision, clamped
// at zero so clock skew never produces negative phases.
func ms(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return math.Round(float64(d)/float64(time.Microsecond)) / 1000
}

// headerPairs flattens a header map into name/value pairs, sorted by name for
// deterministic output; repeated headers yield repeated pairs.
func headerPairs(h http.Header) []NVP {
	out := make([]NVP, 0, len(h))
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for _, v := range h[k] {
			out = append(out, NVP{Name: k, Value: v})
		}
	}
	return out
}

// queryPairs splits a raw query string into ordered, unescaped pairs. Pairs
// that fail to unescape are kept verbatim.
func queryPairs(raw string) []NVP {
	out := []NVP{}
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		if n, err := url.QueryUnescape(name); err == nil {
			name = n
		}
		if v, err := url.QueryUnescape(value); err == nil {
			value = v
		}
		out = append(out, NVP{Name: name, Value: value})
	}
	return out
}

// requestCookies parses Cookie headers.
func requestCookies(h http.Header) []Cookie {
	out := []Cookie{}
	if h == nil {
		return out
	}
	for _, c := range (&http.Request{Header: h}).Cookies() {
		out = append(out, Cookie{Name: c.Name, Value: c.Value})
	}
	return out
}

// responseCookies parses Set-Cookie headers.
func responseCookies(h http.Header) []Cookie {
	out := []Cookie{}
	if h == nil {
		return out
	}
	for _, c := range (&http.Response{Header: h}).Cookies() {
		hc := Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Path:     c.Path,
			Domain:   c.Domain,
			HTTPOnly: c.HttpOnly,
			Secure:   c.Secure,
		}
		if !c.Expires.IsZero() {
			hc.Expires = c.Expires.UTC().Format(timeLayout)
		}
		out = append(out, hc)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
