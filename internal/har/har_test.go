package har

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/orron/pano/internal/flow"
)

// Body hashes used by the synthetic flows.
const (
	hashJSON  = "h-json"
	hashGzip  = "h-gzip"
	hashPNG   = "h-png"
	hashTrunc = "h-trunc"
	hashBig   = "h-big"
	hashLogin = "h-login"
	hashGone  = "h-gone" // never resolvable
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\xff\xfe")

// bodies is the BodyFunc for the synthetic flows; values are decoded bytes.
var bodies = map[string][]byte{
	hashJSON:  []byte(`{"ok":true,"token":"secret"}`),
	hashGzip:  bytes.Repeat([]byte("abcdefghij"), 10), // 100 bytes decoded, 40 on the wire
	hashPNG:   pngBytes,
	hashTrunc: bytes.Repeat([]byte("x"), 1000),
	hashBig:   bytes.Repeat([]byte("y"), 200),
	hashLogin: []byte(`user=kim&pass=secret`),
}

func lookupBody(h string) ([]byte, bool) {
	b, ok := bodies[h]
	return b, ok
}

var t0 = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)

// fullTiming returns a timing with every phase recorded, at ms precision.
func fullTiming(start time.Time) flow.Timing {
	return flow.Timing{
		Start:     start,
		DNSDone:   start.Add(5 * time.Millisecond),
		Connected: start.Add(20 * time.Millisecond),
		TLSDone:   start.Add(50 * time.Millisecond),
		WroteReq:  start.Add(51 * time.Millisecond),
		FirstByte: start.Add(150 * time.Millisecond),
		End:       start.Add(170 * time.Millisecond),
	}
}

func syntheticFlows() []*flow.Flow {
	return []*flow.Flow{
		{
			ID: 1, Session: "s1", Kind: flow.KindHTTP, Client: "127.0.0.1:50001",
			Proto: "HTTP/1.1", UpProto: "HTTP/2.0", Scheme: "https", Host: "api.example.com", Port: 443,
			Method: "GET", Path: "/v1/items", Query: "limit=10&q=a%20b&flag",
			ReqHeaders: http.Header{
				"Accept":        {"application/json"},
				"Authorization": {"Bearer secret"},
				"Cookie":        {"sid=abc; theme=dark"},
			},
			Status: 200,
			RespHeaders: http.Header{
				"Content-Type": {"application/json; charset=utf-8"},
				"Set-Cookie":   {"sid=def; Path=/; Secure; HttpOnly; Expires=Sat, 27 Sep 2026 09:15:02 GMT"},
				"Vary":         {"Accept", "Origin"},
			},
			RespBody: flow.BodyRef{Hash: hashJSON, Size: 28, Captured: 28, MIME: "application/json"},
			T:        fullTiming(t0),
			Tags:     []string{"api", "auth"},
			Rules:    []flow.RuleHit{{RuleID: "r1", Name: "tag-api", Phase: "request", Action: "tag"}},
			State:    flow.StateDone,
		},
		{
			ID: 2, Session: "s1", Kind: flow.KindHTTP, Client: "127.0.0.1:50002",
			Proto: "HTTP/1.1", Scheme: "https", Host: "api.example.com", Port: 443,
			Method: "GET", Path: "/v1/big",
			ReqHeaders:  http.Header{"Accept-Encoding": {"gzip"}},
			Status:      200,
			RespHeaders: http.Header{"Content-Type": {"text/plain"}, "Content-Encoding": {"gzip"}},
			RespBody:    flow.BodyRef{Hash: hashGzip, Size: 40, Captured: 40, Encoding: "gzip", MIME: "text/plain"},
			T:           flow.Timing{Start: t0.Add(time.Second), FirstByte: t0.Add(1100 * time.Millisecond), End: t0.Add(1200 * time.Millisecond), Reused: true},
			State:       flow.StateDone,
		},
		{
			ID: 3, Session: "s1", Kind: flow.KindHTTP, Client: "127.0.0.1:50003",
			Proto: "HTTP/1.1", Scheme: "https", Host: "cdn.example.com", Port: 8443,
			Method: "GET", Path: "/logo.png",
			Status:      200,
			RespHeaders: http.Header{"Content-Type": {"image/png"}},
			RespBody:    flow.BodyRef{Hash: hashPNG, Size: int64(len(pngBytes)), Captured: int64(len(pngBytes)), MIME: "image/png"},
			T:           flow.Timing{Start: t0.Add(2 * time.Second), End: t0.Add(2100 * time.Millisecond)},
			State:       flow.StateDone,
		},
		{
			ID: 4, Session: "s1", Kind: flow.KindHTTP, Client: "127.0.0.1:50004",
			Proto: "HTTP/1.1", Scheme: "https", Host: "api.example.com", Port: 443,
			Method: "POST", Path: "/upload",
			ReqHeaders:  http.Header{"Content-Type": {"application/octet-stream"}},
			ReqBody:     flow.BodyRef{Hash: hashTrunc, Size: 5000, Captured: 1000, Truncated: true, MIME: "application/octet-stream"},
			Status:      201,
			RespHeaders: http.Header{"Content-Type": {"text/plain"}},
			RespBody:    flow.BodyRef{Hash: hashBig, Size: 200, Captured: 200, MIME: "text/plain"},
			T:           flow.Timing{Start: t0.Add(3 * time.Second), End: t0.Add(3500 * time.Millisecond)},
			State:       flow.StateDone,
		},
		{
			ID: 5, Session: "s1", Kind: flow.KindTunnel, Client: "127.0.0.1:50005",
			Scheme: "https", Host: "tunnel.example.com", Port: 443, Status: 200,
			T:     flow.Timing{Start: t0.Add(4 * time.Second), End: t0.Add(9 * time.Second)},
			State: flow.StateDone,
		},
		{
			ID: 6, Session: "s1", Kind: flow.KindWebSocket, Client: "127.0.0.1:50006",
			Proto: "HTTP/1.1", Scheme: "https", Host: "ws.example.com", Port: 443,
			Method: "GET", Path: "/socket",
			ReqHeaders:  http.Header{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}},
			Status:      101,
			RespHeaders: http.Header{"Upgrade": {"websocket"}},
			T:           flow.Timing{Start: t0.Add(5 * time.Second), End: t0.Add(15 * time.Second)},
			State:       flow.StateDone,
		},
		{
			ID: 7, Session: "s2", Kind: flow.KindHTTP, Client: "127.0.0.1:50007",
			Proto: "HTTP/1.1", Scheme: "http", Host: "down.example.com", Port: 8080,
			Method: "GET", Path: "/health",
			T:     flow.Timing{Start: t0.Add(6 * time.Second), End: t0.Add(6010 * time.Millisecond)},
			Error: "dial tcp 10.0.0.1:8080: connection refused",
			State: flow.StateFailed,
		},
		{
			ID: 8, Session: "s2", Kind: flow.KindHTTP, Client: "127.0.0.1:50008",
			Proto: "HTTP/1.1", Scheme: "http", Host: "form.example.com", Port: 80,
			Method: "POST", Path: "/login",
			ReqHeaders: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
			ReqBody:    flow.BodyRef{Hash: hashLogin, Size: 20, Captured: 20, MIME: "application/x-www-form-urlencoded"},
			Status:     204,
			RespBody:   flow.BodyRef{Hash: hashGone, Size: 10, Captured: 10},
			T:          flow.Timing{Start: t0.Add(7 * time.Second), End: t0.Add(7020 * time.Millisecond)},
			State:      flow.StateDone,
			Replay:     true, ReplayOf: 4,
		},
	}
}

// Strict mirror of the HAR 1.2 shape (plus _pano) used with
// DisallowUnknownFields to catch stray or misspelled keys.
type (
	strictDoc struct {
		Log strictLog `json:"log"`
	}
	strictLog struct {
		Version string        `json:"version"`
		Creator strictCreator `json:"creator"`
		Entries []strictEntry `json:"entries"`
	}
	strictCreator struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	strictEntry struct {
		StartedDateTime string         `json:"startedDateTime"`
		Time            float64        `json:"time"`
		Request         strictRequest  `json:"request"`
		Response        strictResponse `json:"response"`
		Cache           struct{}       `json:"cache"`
		Timings         strictTimings  `json:"timings"`
		ServerIPAddress string         `json:"serverIPAddress"`
		Connection      string         `json:"connection"`
		Pano            strictPano     `json:"_pano"`
	}
	strictRequest struct {
		Method      string          `json:"method"`
		URL         string          `json:"url"`
		HTTPVersion string          `json:"httpVersion"`
		Cookies     []strictCookie  `json:"cookies"`
		Headers     []strictNVP     `json:"headers"`
		QueryString []strictNVP     `json:"queryString"`
		PostData    *strictPostData `json:"postData"`
		HeadersSize int64           `json:"headersSize"`
		BodySize    int64           `json:"bodySize"`
	}
	strictResponse struct {
		Status      int            `json:"status"`
		StatusText  string         `json:"statusText"`
		HTTPVersion string         `json:"httpVersion"`
		Cookies     []strictCookie `json:"cookies"`
		Headers     []strictNVP    `json:"headers"`
		Content     strictContent  `json:"content"`
		RedirectURL string         `json:"redirectURL"`
		HeadersSize int64          `json:"headersSize"`
		BodySize    int64          `json:"bodySize"`
	}
	strictNVP struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	strictCookie struct {
		Name     string `json:"name"`
		Value    string `json:"value"`
		Path     string `json:"path"`
		Domain   string `json:"domain"`
		Expires  string `json:"expires"`
		HTTPOnly bool   `json:"httpOnly"`
		Secure   bool   `json:"secure"`
	}
	strictPostData struct {
		MimeType string `json:"mimeType"`
		Text     string `json:"text"`
		Encoding string `json:"encoding"`
		Comment  string `json:"comment"`
	}
	strictContent struct {
		Size        int64  `json:"size"`
		Compression *int64 `json:"compression"`
		MimeType    string `json:"mimeType"`
		Text        string `json:"text"`
		Encoding    string `json:"encoding"`
		Comment     string `json:"comment"`
	}
	strictTimings struct {
		Blocked float64 `json:"blocked"`
		DNS     float64 `json:"dns"`
		Connect float64 `json:"connect"`
		Send    float64 `json:"send"`
		Wait    float64 `json:"wait"`
		Receive float64 `json:"receive"`
		SSL     float64 `json:"ssl"`
	}
	strictPano struct {
		ID            uint64         `json:"id"`
		Short         string         `json:"short"`
		Kind          string         `json:"kind"`
		State         string         `json:"state"`
		Session       string         `json:"session"`
		Error         string         `json:"error"`
		Tags          []string       `json:"tags"`
		Rules         []flow.RuleHit `json:"rules"`
		ReqTruncated  bool           `json:"reqTruncated"`
		RespTruncated bool           `json:"respTruncated"`
		Replay        bool           `json:"replay"`
		ReplayOf      uint64         `json:"replayOf"`
	}
)

func exportStrict(t *testing.T, flows []*flow.Flow, opts ExportOptions) (strictDoc, []byte) {
	t.Helper()
	var buf bytes.Buffer
	n, err := Export(&buf, flows, opts)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if n != len(flows) {
		t.Fatalf("Export wrote %d entries, want %d", n, len(flows))
	}
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	dec.DisallowUnknownFields()
	var doc strictDoc
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("strict decode: %v\n%s", err, buf.Bytes())
	}
	if len(doc.Log.Entries) != n {
		t.Fatalf("decoded %d entries, want %d", len(doc.Log.Entries), n)
	}
	return doc, buf.Bytes()
}

func TestExportShape(t *testing.T) {
	flows := syntheticFlows()
	doc, raw := exportStrict(t, flows, ExportOptions{
		Creator: "pano-test", Version: "0.1",
		Body:         lookupBody,
		MaxBodyBytes: 128,
	})

	if doc.Log.Version != "1.2" || doc.Log.Creator.Name != "pano-test" || doc.Log.Creator.Version != "0.1" {
		t.Errorf("log header = %+v", doc.Log)
	}
	if bytes.Contains(raw, []byte(`<`)) {
		t.Error("HTML escaping should be off")
	}
	started := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-]\d{2}:\d{2})$`)
	for i, e := range doc.Log.Entries {
		if !started.MatchString(e.StartedDateTime) {
			t.Errorf("entry %d startedDateTime %q not ISO 8601 with ms", i, e.StartedDateTime)
		}
		if e.Request.Cookies == nil || e.Request.Headers == nil || e.Request.QueryString == nil ||
			e.Response.Cookies == nil || e.Response.Headers == nil {
			t.Errorf("entry %d: HAR arrays must never be null", i)
		}
		if e.Request.HeadersSize != -1 || e.Response.HeadersSize != -1 {
			t.Errorf("entry %d headersSize should be -1", i)
		}
		if e.Timings.Blocked != -1 {
			t.Errorf("entry %d blocked = %v, want -1", i, e.Timings.Blocked)
		}
		if e.Pano.ID != uint64(flows[i].ID) || e.Pano.Short != flows[i].ID.Short() || e.Pano.Kind != string(flows[i].Kind) {
			t.Errorf("entry %d _pano = %+v", i, e.Pano)
		}
		if e.Connection != flows[i].Client {
			t.Errorf("entry %d connection = %q", i, e.Connection)
		}
	}

	// 1: JSON with cookies, query string, multi-valued headers, full timings.
	e := doc.Log.Entries[0]
	if e.StartedDateTime != "2026-08-27T10:00:00.000Z" || e.Time != 170 {
		t.Errorf("entry 0 start/time = %q/%v", e.StartedDateTime, e.Time)
	}
	if e.Request.URL != "https://api.example.com/v1/items?limit=10&q=a%20b&flag" || e.Request.Method != "GET" || e.Request.HTTPVersion != "HTTP/1.1" {
		t.Errorf("entry 0 request = %+v", e.Request)
	}
	wantQS := []strictNVP{{"limit", "10"}, {"q", "a b"}, {"flag", ""}}
	if !reflect.DeepEqual(e.Request.QueryString, wantQS) {
		t.Errorf("queryString = %+v, want %+v", e.Request.QueryString, wantQS)
	}
	wantCookies := []strictCookie{{Name: "sid", Value: "abc"}, {Name: "theme", Value: "dark"}}
	if !reflect.DeepEqual(e.Request.Cookies, wantCookies) {
		t.Errorf("request cookies = %+v", e.Request.Cookies)
	}
	wantHdr := []strictNVP{{"Accept", "application/json"}, {"Authorization", "Bearer secret"}, {"Cookie", "sid=abc; theme=dark"}}
	if !reflect.DeepEqual(e.Request.Headers, wantHdr) {
		t.Errorf("request headers = %+v", e.Request.Headers)
	}
	if e.Request.PostData != nil || e.Request.BodySize != 0 {
		t.Errorf("GET should have no postData: %+v", e.Request)
	}
	if e.Response.Status != 200 || e.Response.StatusText != "OK" || e.Response.HTTPVersion != "HTTP/2.0" {
		t.Errorf("entry 0 response = %+v", e.Response)
	}
	if len(e.Response.Cookies) != 1 || e.Response.Cookies[0].Name != "sid" || !e.Response.Cookies[0].Secure ||
		!e.Response.Cookies[0].HTTPOnly || e.Response.Cookies[0].Path != "/" || e.Response.Cookies[0].Expires != "2026-09-27T09:15:02.000Z" {
		t.Errorf("response cookies = %+v", e.Response.Cookies)
	}
	wantRespHdr := []strictNVP{
		{"Content-Type", "application/json; charset=utf-8"},
		{"Set-Cookie", "sid=def; Path=/; Secure; HttpOnly; Expires=Sat, 27 Sep 2026 09:15:02 GMT"},
		{"Vary", "Accept"},
		{"Vary", "Origin"},
	}
	if !reflect.DeepEqual(e.Response.Headers, wantRespHdr) {
		t.Errorf("response headers = %+v", e.Response.Headers)
	}
	if c := e.Response.Content; c.Text != string(bodies[hashJSON]) || c.Encoding != "" || c.Size != 28 || c.MimeType != "application/json; charset=utf-8" || c.Compression != nil {
		t.Errorf("entry 0 content = %+v", c)
	}
	if e.Response.BodySize != 28 {
		t.Errorf("bodySize = %d", e.Response.BodySize)
	}
	wantT := strictTimings{Blocked: -1, DNS: 5, Connect: 45, SSL: 30, Send: 1, Wait: 99, Receive: 20}
	if e.Timings != wantT {
		t.Errorf("timings = %+v, want %+v", e.Timings, wantT)
	}
	if !reflect.DeepEqual(e.Pano.Tags, []string{"api", "auth"}) || len(e.Pano.Rules) != 1 || e.Pano.Rules[0].RuleID != "r1" || e.Pano.Session != "s1" || e.Pano.State != "done" {
		t.Errorf("_pano = %+v", e.Pano)
	}

	// 2: gzip on the wire, decoded via BodyFunc.
	e = doc.Log.Entries[1]
	if c := e.Response.Content; c.Text != string(bodies[hashGzip]) || c.Size != 100 || c.Compression == nil || *c.Compression != 60 {
		t.Errorf("gzip content = %+v", c)
	}
	if e.Response.BodySize != 40 {
		t.Errorf("gzip bodySize = %d, want wire size 40", e.Response.BodySize)
	}
	if e.Timings.DNS != -1 || e.Timings.Connect != -1 || e.Timings.SSL != -1 || e.Timings.Send != -1 || e.Timings.Wait != 100 || e.Timings.Receive != 100 {
		t.Errorf("reused-connection timings = %+v", e.Timings)
	}

	// 3: binary image → base64.
	e = doc.Log.Entries[2]
	if c := e.Response.Content; c.Encoding != "base64" || c.Text != base64.StdEncoding.EncodeToString(pngBytes) || c.MimeType != "image/png" {
		t.Errorf("png content = %+v", c)
	}
	if e.Request.URL != "https://cdn.example.com:8443/logo.png" {
		t.Errorf("non-default port url = %q", e.Request.URL)
	}
	if e.Timings.Wait != -1 || e.Timings.Receive != -1 {
		t.Errorf("timings without first byte = %+v", e.Timings)
	}

	// 4: truncated request body (binary → base64) and capped response body.
	e = doc.Log.Entries[3]
	if pd := e.Request.PostData; pd == nil || pd.Encoding != "" || !strings.Contains(pd.Comment, "truncated at capture (1000 of 5000 bytes)") ||
		!strings.Contains(pd.Comment, "exceeds inline cap of 128") || pd.Text != "" || pd.MimeType != "application/octet-stream" {
		t.Errorf("truncated postData = %+v", pd)
	}
	if e.Request.BodySize != 5000 || !e.Pano.ReqTruncated || e.Pano.RespTruncated {
		t.Errorf("truncated flags: bodySize=%d pano=%+v", e.Request.BodySize, e.Pano)
	}
	if c := e.Response.Content; c.Text != "" || !strings.Contains(c.Comment, "200 bytes exceeds inline cap of 128 bytes") || c.Size != 200 {
		t.Errorf("capped content = %+v", c)
	}

	// 5: tunnel.
	e = doc.Log.Entries[4]
	if e.Request.Method != "CONNECT" || e.Request.URL != "https://tunnel.example.com" || e.Pano.Kind != "tunnel" || e.Time != 5000 {
		t.Errorf("tunnel entry = %+v", e)
	}
	if e.Response.Content.MimeType != "" || e.Response.Content.Text != "" {
		t.Errorf("tunnel content = %+v", e.Response.Content)
	}

	// 6: websocket.
	e = doc.Log.Entries[5]
	if e.Request.Method != "GET" || e.Response.Status != 101 || e.Response.StatusText != "Switching Protocols" || e.Pano.Kind != "websocket" {
		t.Errorf("websocket entry = %+v", e)
	}

	// 7: error flow.
	e = doc.Log.Entries[6]
	if e.Response.Status != 0 || e.Response.StatusText != "" || e.Pano.Error == "" || e.Pano.State != "failed" || e.Request.URL != "http://down.example.com:8080/health" {
		t.Errorf("error entry = %+v", e)
	}

	// 8: form post, unresolvable response body, replay marker.
	e = doc.Log.Entries[7]
	if pd := e.Request.PostData; pd == nil || pd.Text != "user=kim&pass=secret" || pd.MimeType != "application/x-www-form-urlencoded" {
		t.Errorf("form postData = %+v", pd)
	}
	if c := e.Response.Content; c.Text != "" || c.Comment != "body not available" || c.Size != 10 {
		t.Errorf("unavailable content = %+v", c)
	}
	if !e.Pano.Replay || e.Pano.ReplayOf != 4 {
		t.Errorf("replay = %+v", e.Pano)
	}
}

func TestExportRedaction(t *testing.T) {
	flows := syntheticFlows()
	red := &Redactor{
		Headers: func(h http.Header) http.Header {
			c := h.Clone()
			for _, k := range []string{"Authorization", "Cookie", "Set-Cookie"} {
				if c.Get(k) != "" {
					c.Set(k, "[redacted]")
				}
			}
			return c
		},
		Text: func(s string) string { return strings.ReplaceAll(s, "secret", "***") },
	}
	doc, raw := exportStrict(t, flows, ExportOptions{Body: lookupBody, Redact: red})
	if bytes.Contains(raw, []byte("secret")) {
		t.Fatalf("secret leaked:\n%s", raw)
	}
	e := doc.Log.Entries[0]
	if len(e.Request.Cookies) != 0 || len(e.Response.Cookies) != 0 {
		t.Errorf("cookies must derive from redacted headers: %+v / %+v", e.Request.Cookies, e.Response.Cookies)
	}
	if !strings.Contains(e.Response.Content.Text, `"token":"***"`) {
		t.Errorf("text body not redacted: %q", e.Response.Content.Text)
	}
	if doc.Log.Entries[7].Request.PostData.Text != "user=kim&pass=***" {
		t.Errorf("postData not redacted: %+v", doc.Log.Entries[7].Request.PostData)
	}
	// Binary bodies are left alone by the text redactor.
	if doc.Log.Entries[2].Response.Content.Encoding != "base64" {
		t.Errorf("binary body changed under redaction")
	}
	// The caller's headers must not be mutated.
	if flows[0].ReqHeaders.Get("Authorization") != "Bearer secret" {
		t.Error("Export mutated the flow's headers")
	}
}

func TestExportEdgeCases(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		var buf bytes.Buffer
		n, err := Export(&buf, nil, ExportOptions{})
		if err != nil || n != 0 {
			t.Fatalf("Export(nil) = %d, %v", n, err)
		}
		if !json.Valid(buf.Bytes()) {
			t.Fatalf("invalid JSON: %s", buf.String())
		}
		got, err := Import(&buf)
		if err != nil || len(got) != 0 {
			t.Fatalf("Import(empty export) = %v, %v", got, err)
		}
	})
	t.Run("nil flows skipped", func(t *testing.T) {
		var buf bytes.Buffer
		n, err := Export(&buf, []*flow.Flow{nil, syntheticFlows()[0], nil}, ExportOptions{})
		if err != nil || n != 1 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		if !json.Valid(buf.Bytes()) {
			t.Fatalf("invalid JSON: %s", buf.String())
		}
	})
	t.Run("default creator and cap", func(t *testing.T) {
		var buf bytes.Buffer
		big := &flow.Flow{
			ID: 9, Kind: flow.KindHTTP, Scheme: "https", Host: "h", Port: 443, Method: "GET", Path: "/",
			RespBody: flow.BodyRef{Hash: "big", Size: DefaultMaxBodyBytes + 1},
			T:        flow.Timing{Start: t0, End: t0},
		}
		body := func(string) ([]byte, bool) { return make([]byte, DefaultMaxBodyBytes+1), true }
		if _, err := Export(&buf, []*flow.Flow{big}, ExportOptions{Body: body}); err != nil {
			t.Fatal(err)
		}
		var doc Document
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Log.Creator.Name != "pano" {
			t.Errorf("creator = %+v", doc.Log.Creator)
		}
		if c := doc.Log.Entries[0].Response.Content; c.Text != "" || !strings.Contains(c.Comment, "exceeds inline cap") {
			t.Errorf("content = %+v", c)
		}
	})
	t.Run("negative cap omits bodies", func(t *testing.T) {
		var buf bytes.Buffer
		if _, err := Export(&buf, syntheticFlows()[:1], ExportOptions{Body: lookupBody, MaxBodyBytes: -1}); err != nil {
			t.Fatal(err)
		}
		var doc Document
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if c := doc.Log.Entries[0].Response.Content; c.Text != "" || c.Comment != "body omitted" {
			t.Errorf("content = %+v", c)
		}
	})
	t.Run("no BodyFunc", func(t *testing.T) {
		var buf bytes.Buffer
		if _, err := Export(&buf, syntheticFlows()[:1], ExportOptions{}); err != nil {
			t.Fatal(err)
		}
		var doc Document
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if c := doc.Log.Entries[0].Response.Content; c.Text != "" || c.Comment != "body not available" || c.Size != 28 {
			t.Errorf("content = %+v", c)
		}
	})
	t.Run("active flow", func(t *testing.T) {
		f := syntheticFlows()[0]
		f.T.End = time.Time{}
		f.State = flow.StateActive
		var buf bytes.Buffer
		if _, err := Export(&buf, []*flow.Flow{f}, ExportOptions{}); err != nil {
			t.Fatal(err)
		}
		var doc Document
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if e := doc.Log.Entries[0]; e.Time != -1 || e.Timings.Receive != -1 {
			t.Errorf("active entry time=%v timings=%+v", e.Time, e.Timings)
		}
	})
	t.Run("writer error", func(t *testing.T) {
		_, err := Export(failWriter{}, syntheticFlows(), ExportOptions{})
		if err == nil {
			t.Fatal("expected write error")
		}
	})
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestRoundTrip(t *testing.T) {
	flows := syntheticFlows()
	var buf bytes.Buffer
	if _, err := Export(&buf, flows, ExportOptions{Body: lookupBody}); err != nil {
		t.Fatal(err)
	}
	got, err := Import(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(flows) {
		t.Fatalf("imported %d flows, want %d", len(got), len(flows))
	}
	for i, want := range flows {
		im := got[i]
		g := im.Flow
		if g.ID != 0 {
			t.Errorf("[%d] imported ID = %d, want 0", i, g.ID)
		}
		wantMethod := want.Method
		if wantMethod == "" && want.Kind == flow.KindTunnel {
			wantMethod = "CONNECT"
		}
		if wantMethod == "" && want.Kind == flow.KindWebSocket {
			wantMethod = "GET"
		}
		wantUp := want.UpProto
		if wantUp == want.Proto {
			wantUp = ""
		}
		checks := []struct {
			name      string
			got, want any
		}{
			{"Scheme", g.Scheme, want.Scheme},
			{"Host", g.Host, want.Host},
			{"Port", g.Port, want.Port},
			{"Path", g.Path, want.Path},
			{"Query", g.Query, want.Query},
			{"Method", g.Method, wantMethod},
			{"Proto", g.Proto, want.Proto},
			{"UpProto", g.UpProto, wantUp},
			{"Status", g.Status, want.Status},
			{"Kind", g.Kind, want.Kind},
			{"State", g.State, want.State},
			{"Session", g.Session, want.Session},
			{"Client", g.Client, want.Client},
			{"Error", g.Error, want.Error},
			{"Tags", g.Tags, want.Tags},
			{"Rules", g.Rules, want.Rules},
			{"Replay", g.Replay, want.Replay},
			{"ReplayOf", g.ReplayOf, want.ReplayOf},
			{"ReqHeaders", g.ReqHeaders, nonNil(want.ReqHeaders)},
			{"RespHeaders", g.RespHeaders, nonNil(want.RespHeaders)},
			{"ReqBody.Size", g.ReqBody.Size, want.ReqBody.Size},
			{"ReqBody.Truncated", g.ReqBody.Truncated, want.ReqBody.Truncated},
			{"ReqBody.MIME", g.ReqBody.MIME, want.ReqBody.MIME},
			{"RespBody.Truncated", g.RespBody.Truncated, want.RespBody.Truncated},
			{"RespBody.MIME", g.RespBody.MIME, want.RespBody.MIME},
			{"ReqBody bytes", im.ReqBody, bodies[want.ReqBody.Hash]},
			{"RespBody bytes", im.RespBody, bodies[want.RespBody.Hash]},
		}
		for _, c := range checks {
			if !reflect.DeepEqual(c.got, c.want) {
				t.Errorf("[%d] %s = %#v, want %#v", i, c.name, c.got, c.want)
			}
		}
		if want.RespBody.Encoding == "" && g.RespBody.Size != want.RespBody.Size {
			t.Errorf("[%d] RespBody.Size = %d, want %d", i, g.RespBody.Size, want.RespBody.Size)
		}
		if want.RespBody.Encoding != "" {
			// Bodies travel decoded, so the imported ref describes decoded bytes.
			if g.RespBody.Size != int64(len(bodies[want.RespBody.Hash])) || g.RespBody.Encoding != "" {
				t.Errorf("[%d] decoded RespBody = %+v", i, g.RespBody)
			}
		}
		times := []struct {
			name      string
			got, want time.Time
		}{
			{"Start", g.T.Start, want.T.Start},
			{"DNSDone", g.T.DNSDone, want.T.DNSDone},
			{"Connected", g.T.Connected, want.T.Connected},
			{"TLSDone", g.T.TLSDone, want.T.TLSDone},
			{"WroteReq", g.T.WroteReq, want.T.WroteReq},
			{"FirstByte", g.T.FirstByte, want.T.FirstByte},
			{"End", g.T.End, want.T.End},
		}
		for _, c := range times {
			if !c.got.Equal(c.want) {
				t.Errorf("[%d] T.%s = %v, want %v", i, c.name, c.got, c.want)
			}
		}
	}
}

func nonNil(h http.Header) http.Header {
	if h == nil {
		return http.Header{}
	}
	return h
}

func TestImportChrome(t *testing.T) {
	fh, err := os.Open("testdata/chrome.har")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	got, err := Import(fh)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4", len(got))
	}

	f := got[0].Flow
	if f.Scheme != "https" || f.Host != "api.example.com" || f.Port != 443 || f.Path != "/v1/items" || f.Query != "limit=10&q=a%20b" {
		t.Errorf("url parts = %s %s %d %s %s", f.Scheme, f.Host, f.Port, f.Path, f.Query)
	}
	if f.URL() != "https://api.example.com/v1/items?limit=10&q=a%20b" {
		t.Errorf("URL() = %q", f.URL())
	}
	if f.Method != "GET" || f.Proto != "h2" || f.UpProto != "" || f.Status != 200 || f.Kind != flow.KindHTTP || f.State != flow.StateDone || f.Client != "4821" {
		t.Errorf("flow = %+v", f)
	}
	for k := range f.ReqHeaders {
		if strings.HasPrefix(k, ":") {
			t.Errorf("pseudo-header %q leaked", k)
		}
	}
	if f.ReqHeaders.Get("Cookie") != "sid=abc123; theme=dark" || f.ReqHeaders.Get("User-Agent") == "" {
		t.Errorf("req headers = %v", f.ReqHeaders)
	}
	if string(got[0].RespBody) != `{"items":[{"id":1}],"n":1}` {
		t.Errorf("resp body = %q", got[0].RespBody)
	}
	if f.RespBody.Size != 27 || f.RespBody.Captured != 26 || f.RespBody.MIME != "application/json" || f.RespBody.Encoding != "" {
		t.Errorf("resp ref = %+v", f.RespBody)
	}
	if f.RespHeaders.Get("Content-Encoding") != "br" {
		t.Errorf("resp headers = %v", f.RespHeaders)
	}
	wantStart := time.Date(2026, 8, 27, 9, 15, 2, 500e6, time.UTC)
	if !f.T.Start.Equal(wantStart) {
		t.Errorf("Start = %v", f.T.Start)
	}
	if d := f.T.End.Sub(f.T.Start); d < 84*time.Millisecond || d > 85*time.Millisecond {
		t.Errorf("total = %v", d)
	}
	// blocked 2.104 + dns .512 + connect 31.7 + send .19 + wait 41.2 = 75.706ms
	if ttfb := f.T.TTFB(); ttfb < 75*time.Millisecond || ttfb > 76*time.Millisecond {
		t.Errorf("TTFB = %v", ttfb)
	}
	if f.T.Connected.IsZero() || f.T.TLSDone.Sub(f.T.Connected) != 18900*time.Microsecond {
		t.Errorf("ssl phase = %v..%v", f.T.Connected, f.T.TLSDone)
	}

	f = got[1].Flow
	if f.Method != "POST" || string(got[1].ReqBody) != `{"user":"kim","pass":"hunter2"}` || f.ReqBody.Size != 31 || f.ReqBody.MIME != "application/json" {
		t.Errorf("login = %+v body=%q", f.ReqBody, got[1].ReqBody)
	}
	if f.Status != 302 || f.RespHeaders.Get("Location") != "https://app.example.com/home" || len(got[1].RespBody) != 0 || f.RespBody.Size != 0 {
		t.Errorf("redirect = %+v", f)
	}
	if !f.T.Start.Equal(time.Date(2026, 8, 27, 7, 15, 2, 700e6, time.UTC)) {
		t.Errorf("offset start = %v", f.T.Start)
	}

	f = got[2].Flow
	if f.Host != "cdn.example.com" || f.Port != 8443 || f.Proto != "http/1.1" {
		t.Errorf("image flow = %+v", f)
	}
	if b := got[2].RespBody; len(b) != 70 || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Errorf("png bytes = %x", b)
	}
	if f.RespBody.MIME != "image/png" || f.RespBody.Size != 70 || f.RespBody.Captured != 70 {
		t.Errorf("png ref = %+v", f.RespBody)
	}

	f = got[3].Flow
	if f.Error != "net::ERR_CONNECTION_REFUSED" || f.State != flow.StateFailed || f.Status != 0 || f.Host != "localhost" || f.Port != 9999 {
		t.Errorf("failed flow = %+v", f)
	}
}

func TestImportKinds(t *testing.T) {
	doc := `{"log":{"version":"1.2","creator":{"name":"x","version":"1"},"entries":[
	 {"startedDateTime":"2026-08-27T10:00:00.000Z","time":1,"request":{"method":"connect","url":"https://t.example.com:443"},"response":{"status":200}},
	 {"startedDateTime":"2026-08-27T10:00:00.000Z","time":1,"request":{"method":"GET","url":"wss://ws.example.com/chat","headers":[{"name":"Upgrade","value":"websocket"}]},"response":{"status":101}},
	 {"startedDateTime":"2026-08-27T10:00:00.000Z","time":1,"request":{"method":"GET","url":"ws://ws.example.com:8080/x"},"response":{"status":101}},
	 {"startedDateTime":"bogus","request":{"method":"GET","url":"http://a/b","cookies":[{"name":"a","value":"1"},{"name":"b","value":"2"}]},"response":{"status":200}},
	 {"request":{"method":"GET","url":"::not a url"},"response":{}},
	 {"startedDateTime":"2026-08-27T10:00:00.000Z","request":{"method":"GET","url":"http://a/","headers":[{"name":"Content-Type","value":"text/plain"}],"bodySize":12},"response":{"status":200,"headers":[{"name":"Content-Type","value":"text/html"}],"bodySize":7},"timings":{"blocked":-1,"dns":-1,"connect":-1,"send":1,"wait":2,"receive":3,"ssl":-1}}
	]}}`
	got, err := Import(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d", len(got))
	}
	if f := got[0].Flow; f.Kind != flow.KindTunnel || f.Method != "CONNECT" || f.Host != "t.example.com" || f.Port != 443 || f.URL() != "https://t.example.com" {
		t.Errorf("tunnel = %+v", f)
	}
	if f := got[1].Flow; f.Kind != flow.KindWebSocket || f.Scheme != "https" || f.Port != 443 || f.Path != "/chat" {
		t.Errorf("wss = %+v", f)
	}
	if f := got[2].Flow; f.Kind != flow.KindWebSocket || f.Scheme != "http" || f.Port != 8080 {
		t.Errorf("ws = %+v", f)
	}
	if f := got[3].Flow; !f.T.Start.IsZero() || f.ReqHeaders.Get("Cookie") != "a=1; b=2" || f.Port != 80 {
		t.Errorf("bogus time / synthesized cookie = %+v", f)
	}
	if f := got[4].Flow; f.Path != "::not a url" || f.Host != "" || f.Kind != flow.KindHTTP {
		t.Errorf("unparsable url = %+v", f)
	}
	f := got[5].Flow
	if f.ReqBody.Size != 12 || f.ReqBody.Captured != 0 || f.ReqBody.MIME != "text/plain" || f.RespBody.Size != 7 || f.RespBody.MIME != "text/html" {
		t.Errorf("declared sizes = %+v / %+v", f.ReqBody, f.RespBody)
	}
	// No "time" but a receive phase: End comes from the phases.
	if d := f.T.End.Sub(f.T.Start); d != 6*time.Millisecond || f.T.TTFB() != 3*time.Millisecond || !f.T.DNSDone.IsZero() {
		t.Errorf("phase-derived timing = %+v", f.T)
	}
}

func TestImportErrors(t *testing.T) {
	cases := map[string]string{
		"empty":     "",
		"not json":  "hello",
		"truncated": `{"log":{"entries":[{"request":`,
	}
	for name, in := range cases {
		if _, err := Import(strings.NewReader(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Import(strings.NewReader(`{"foo":1}`)); !errors.Is(err, ErrNotHAR) {
		t.Errorf("no log: err = %v", err)
	}
	if _, err := Import(strings.NewReader(`[1,2]`)); err == nil {
		t.Error("array: expected error")
	}
}

func TestImportLenient(t *testing.T) {
	// A mistyped field zeroes that field but keeps the rest of the file.
	doc := `{"log":{"version":"1.2","entries":[{"startedDateTime":"2026-08-27T10:00:00Z","time":"fast","request":{"method":"GET","url":"http://a/"},"response":{"status":"200","content":{"size":"3","text":"abc"}}}]}}`
	got, err := Import(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Flow.Host != "a" || string(got[0].RespBody) != "abc" || got[0].Flow.RespBody.Size != 3 {
		t.Errorf("got %+v", got)
	}
	// UTF-8 BOM prefix is tolerated.
	got, err = Import(strings.NewReader("\xef\xbb\xbf" + `{"log":{"entries":[]}}`))
	if err != nil || len(got) != 0 {
		t.Errorf("BOM: %v %v", got, err)
	}
	// Type mismatches must not be reported for well-formed documents.
	if _, err := Import(strings.NewReader(`{"log":{"entries":[{"request":{"url":"http://x/"},"response":{}}]}}`)); err != nil {
		t.Errorf("minimal: %v", err)
	}
}

func TestDecodeBody(t *testing.T) {
	cases := []struct {
		text, enc string
		want      []byte
	}{
		{"", "", nil},
		{"plain", "", []byte("plain")},
		{"aGVsbG8=", "base64", []byte("hello")},
		{"aGVsbG8", "base64", []byte("hello")},           // unpadded
		{"aGVs\nbG8=\n", "BASE64", []byte("hello")},      // wrapped lines, odd case
		{"-_8=", "base64", []byte{0xfb, 0xff}},           // URL alphabet
		{"not base64!", "base64", []byte("not base64!")}, // kept verbatim
	}
	for _, c := range cases {
		if got := decodeBody(c.text, c.enc); !bytes.Equal(got, c.want) {
			t.Errorf("decodeBody(%q,%q) = %q, want %q", c.text, c.enc, got, c.want)
		}
	}
}

func TestQueryPairs(t *testing.T) {
	got := queryPairs("a=1&b=%zz&&c&=v&d=x%3Dy")
	want := []NVP{{Name: "a", Value: "1"}, {Name: "b", Value: "%zz"}, {Name: "c"}, {Name: "", Value: "v"}, {Name: "d", Value: "x=y"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if got := queryPairs(""); len(got) != 0 || got == nil {
		t.Errorf("empty query = %#v", got)
	}
}

func TestExportStreams(t *testing.T) {
	// Entries reach the writer before the export finishes: the document is
	// not buffered as a whole.
	flows := make([]*flow.Flow, 2000)
	for i := range flows {
		f := *syntheticFlows()[0]
		f.ID = flow.ID(i + 1)
		flows[i] = &f
	}
	cw := &countingWriter{}
	n, err := Export(cw, flows, ExportOptions{Body: lookupBody})
	if err != nil || n != len(flows) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if cw.writes < 10 {
		t.Errorf("expected many incremental writes, got %d", cw.writes)
	}
	got, err := Import(bytes.NewReader(cw.buf.Bytes()))
	if err != nil || len(got) != len(flows) {
		t.Fatalf("re-import: %d, %v", len(got), err)
	}
}

type countingWriter struct {
	buf    bytes.Buffer
	writes int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	return c.buf.Write(p)
}

func FuzzImport(f *testing.F) {
	fixture, err := os.ReadFile("testdata/chrome.har")
	if err != nil {
		f.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := Export(&buf, syntheticFlows(), ExportOptions{Body: lookupBody}); err != nil {
		f.Fatal(err)
	}
	seeds := [][]byte{
		fixture,
		buf.Bytes(),
		[]byte(`{"log":{"entries":[]}}`),
		[]byte(`{"log":{"entries":[{"request":{"url":"http://x/","postData":{"text":"AA==","encoding":"base64"}},"response":{"content":{"text":"hi"}}}]}}`),
		[]byte(`{"log":{"entries":[{"startedDateTime":"2026-08-27T10:00:00Z","time":-5,"timings":{"dns":1e308,"connect":-1e308}}]}}`),
		[]byte(`{"log":null}`),
		[]byte(`{"log":{"entries":[{"_pano":{"id":18446744073709551615,"kind":"tunnel","tags":["a"]}}]}}`),
		[]byte("\xef\xbb\xbf{}"),
		[]byte("{"),
		[]byte(""),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := Import(bytes.NewReader(data))
		if err != nil {
			return
		}
		flows := make([]*flow.Flow, len(got))
		store := map[string][]byte{}
		for i, im := range got {
			im.Flow.ReqBody.Hash, im.Flow.RespBody.Hash = "r", "s"
			store["r"], store["s"] = im.ReqBody, im.RespBody
			flows[i] = im.Flow
		}
		body := func(h string) ([]byte, bool) { b, ok := store[h]; return b, ok }
		if _, err := Export(io.Discard, flows, ExportOptions{Body: body}); err != nil {
			t.Fatalf("re-export failed: %v", err)
		}
	})
}
