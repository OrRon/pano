package har

import (
	"net/http"

	"github.com/orron/pano/internal/flow"
)

// Version is the HAR format version this package writes.
const Version = "1.2"

// DefaultMaxBodyBytes is the per-body inline cap used when
// [ExportOptions.MaxBodyBytes] is zero.
const DefaultMaxBodyBytes = 1 << 20

// BodyFunc fetches a body's decoded bytes (Content-Encoding removed) by hash.
// It returns ok=false when the body is unavailable.
type BodyFunc func(hash string) (b []byte, ok bool)

// Redactor masks secrets in header values and text bodies. Either function may
// be nil; a nil *Redactor disables redaction entirely.
type Redactor struct {
	// Headers returns a redacted copy of h. It must not modify h.
	Headers func(http.Header) http.Header
	// Text returns a redacted copy of a text body.
	Text func(string) string
}

// ExportOptions controls [Export].
type ExportOptions struct {
	// Creator and Version populate log.creator.
	Creator, Version string
	// Body resolves body hashes to decoded bytes. When nil no bodies are
	// inlined and each body carries an explanatory comment.
	Body BodyFunc
	// Redact, when non-nil, masks header values and text bodies.
	Redact *Redactor
	// MaxBodyBytes caps the size of a single inlined body. Zero selects
	// [DefaultMaxBodyBytes]; a negative value omits all bodies. Larger bodies
	// are left out and the content/postData comment explains why.
	MaxBodyBytes int
}

// Imported is one flow parsed from a HAR document. Bodies are returned
// separately as decoded bytes so the caller can store them and fill in the
// BodyRef hashes; Flow.ID is 0 and is assigned by the caller.
type Imported struct {
	Flow     *flow.Flow
	ReqBody  []byte
	RespBody []byte
}

// Document is a HAR file: a single "log" object.
type Document struct {
	Log *Log `json:"log"`
}

// Log is the HAR 1.2 log object.
type Log struct {
	Version string   `json:"version"`
	Creator Creator  `json:"creator"`
	Browser *Creator `json:"browser,omitempty"`
	Pages   []Page   `json:"pages,omitempty"`
	Entries []Entry  `json:"entries"`
	Comment string   `json:"comment,omitempty"`
}

// Creator identifies the application that produced the log.
type Creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Comment string `json:"comment,omitempty"`
}

// Page is a browser page; pano never writes pages but tolerates them on import.
type Page struct {
	StartedDateTime string      `json:"startedDateTime"`
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	PageTimings     PageTimings `json:"pageTimings"`
	Comment         string      `json:"comment,omitempty"`
}

// PageTimings holds page load milestones in milliseconds.
type PageTimings struct {
	OnContentLoad float64 `json:"onContentLoad,omitempty"`
	OnLoad        float64 `json:"onLoad,omitempty"`
	Comment       string  `json:"comment,omitempty"`
}

// Entry is one exchange.
type Entry struct {
	Pageref         string   `json:"pageref,omitempty"`
	StartedDateTime string   `json:"startedDateTime"`
	Time            float64  `json:"time"`
	Request         Request  `json:"request"`
	Response        Response `json:"response"`
	Cache           Cache    `json:"cache"`
	Timings         Timings  `json:"timings"`
	ServerIPAddress string   `json:"serverIPAddress"`
	Connection      string   `json:"connection,omitempty"`
	Comment         string   `json:"comment,omitempty"`
	// Pano is the "_pano" extension carrying flow metadata that has no HAR
	// equivalent. It is nil on documents written by other tools.
	Pano *PanoExt `json:"_pano,omitempty"`
}

// PanoExt is the per-entry "_pano" extension object.
type PanoExt struct {
	ID            flow.ID        `json:"id"`
	Short         string         `json:"short"`
	Kind          flow.Kind      `json:"kind"`
	State         flow.State     `json:"state,omitempty"`
	Session       string         `json:"session,omitempty"`
	Error         string         `json:"error,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	Rules         []flow.RuleHit `json:"rules,omitempty"`
	ReqTruncated  bool           `json:"reqTruncated,omitempty"`
	RespTruncated bool           `json:"respTruncated,omitempty"`
	Replay        bool           `json:"replay,omitempty"`
	ReplayOf      flow.ID        `json:"replayOf,omitempty"`
}

// Request is the request half of an entry.
type Request struct {
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	HTTPVersion string    `json:"httpVersion"`
	Cookies     []Cookie  `json:"cookies"`
	Headers     []NVP     `json:"headers"`
	QueryString []NVP     `json:"queryString"`
	PostData    *PostData `json:"postData,omitempty"`
	HeadersSize int64     `json:"headersSize"`
	BodySize    int64     `json:"bodySize"`
	Comment     string    `json:"comment,omitempty"`
}

// Response is the response half of an entry.
type Response struct {
	Status      int      `json:"status"`
	StatusText  string   `json:"statusText"`
	HTTPVersion string   `json:"httpVersion"`
	Cookies     []Cookie `json:"cookies"`
	Headers     []NVP    `json:"headers"`
	Content     Content  `json:"content"`
	RedirectURL string   `json:"redirectURL"`
	HeadersSize int64    `json:"headersSize"`
	BodySize    int64    `json:"bodySize"`
	Comment     string   `json:"comment,omitempty"`
	// Error is Chrome's "_error" extension (e.g. "net::ERR_CONNECTION_RESET").
	// pano never writes it but uses it on import when no "_pano" is present.
	Error string `json:"_error,omitempty"`
}

// NVP is a name/value pair used for headers and query parameters.
type NVP struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

// Cookie is a parsed request or response cookie.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// PostData is a request body. Encoding is a pano extension (HAR 1.2 has no
// base64 flag for request bodies): it is "base64" when Text is not UTF-8 text.
type PostData struct {
	MimeType string  `json:"mimeType"`
	Params   []Param `json:"params,omitempty"`
	Text     string  `json:"text,omitempty"`
	Encoding string  `json:"encoding,omitempty"`
	Comment  string  `json:"comment,omitempty"`
}

// Param is one posted form parameter.
type Param struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	FileName    string `json:"fileName,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

// Content is a response body. Size is the decoded length; Encoding is
// "base64" when Text is base64 rather than literal text.
type Content struct {
	Size        int64  `json:"size"`
	Compression *int64 `json:"compression,omitempty"`
	MimeType    string `json:"mimeType"`
	Text        string `json:"text,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

// Cache describes cache usage; pano always writes an empty object.
type Cache struct {
	BeforeRequest *CacheState `json:"beforeRequest,omitempty"`
	AfterRequest  *CacheState `json:"afterRequest,omitempty"`
	Comment       string      `json:"comment,omitempty"`
}

// CacheState is a cache entry snapshot.
type CacheState struct {
	Expires    string `json:"expires,omitempty"`
	LastAccess string `json:"lastAccess"`
	ETag       string `json:"eTag"`
	HitCount   int64  `json:"hitCount"`
	Comment    string `json:"comment,omitempty"`
}

// Timings breaks the entry down by phase, in milliseconds. -1 marks a phase
// that does not apply or was not measured.
type Timings struct {
	Blocked float64 `json:"blocked"`
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
	SSL     float64 `json:"ssl"`
	Comment string  `json:"comment,omitempty"`
}
