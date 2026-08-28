package flow

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// ID is a monotonic flow identifier.
type ID uint64

const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz" // Crockford base32, lowercase

// Short renders the ID as a compact Crockford base32 string ("1", "z", "2k7").
func (id ID) Short() string {
	if id == 0 {
		return "0"
	}
	var buf [13]byte
	i := len(buf)
	for v := uint64(id); v > 0; v /= 32 {
		i--
		buf[i] = idAlphabet[v%32]
	}
	return string(buf[i:])
}

// ParseShort inverts Short. It accepts upper case and the Crockford
// confusables (i/l → 1, o → 0).
func ParseShort(s string) (ID, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 13 {
		return 0, false
	}
	var v uint64
	for _, c := range s {
		switch c {
		case 'i', 'l':
			c = '1'
		case 'o':
			c = '0'
		}
		d := strings.IndexRune(idAlphabet, c)
		if d < 0 {
			return 0, false
		}
		v = v*32 + uint64(d)
	}
	return ID(v), true
}

// Kind classifies a flow.
type Kind string

// Flow kinds.
const (
	KindHTTP      Kind = "http"
	KindWebSocket Kind = "websocket"
	KindTunnel    Kind = "tunnel" // bypassed CONNECT, not decrypted
)

// State is the lifecycle state.
type State string

// Flow states.
const (
	StateActive State = "active"
	StateHeld   State = "held"
	StateDone   State = "done"
	StateFailed State = "failed"
)

// BodyRef describes a captured body stored in the blob store.
type BodyRef struct {
	Hash      string `json:"hash,omitempty"`
	Size      int64  `json:"size"`
	Captured  int64  `json:"captured"`
	Truncated bool   `json:"truncated,omitempty"`
	Encoding  string `json:"encoding,omitempty"`
	MIME      string `json:"mime,omitempty"`
}

// Timing records the phases of an exchange.
type Timing struct {
	Start       time.Time `json:"start"`
	DNSDone     time.Time `json:"dns_done,omitempty"`
	Connected   time.Time `json:"connected,omitempty"`
	TLSDone     time.Time `json:"tls_done,omitempty"`
	WroteReq    time.Time `json:"wrote_req,omitempty"`
	FirstByte   time.Time `json:"first_byte,omitempty"`
	HeadersSent time.Time `json:"headers_sent,omitempty"`
	End         time.Time `json:"end,omitempty"`
	Reused      bool      `json:"reused,omitempty"`
}

// TTFB is time to first response byte.
func (t Timing) TTFB() time.Duration {
	if t.FirstByte.IsZero() {
		return 0
	}
	return t.FirstByte.Sub(t.Start)
}

// Total is the full duration (or elapsed so far).
func (t Timing) Total() time.Duration {
	if t.End.IsZero() {
		return time.Since(t.Start)
	}
	return t.End.Sub(t.Start)
}

// RuleHit records that a rule acted on the flow.
type RuleHit struct {
	RuleID string `json:"rule_id"`
	Name   string `json:"name,omitempty"`
	Phase  string `json:"phase"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

// Flow is one captured exchange. Once State is Done/Failed it is immutable.
type Flow struct {
	ID          ID          `json:"id"`
	Session     string      `json:"session"`
	Kind        Kind        `json:"kind"`
	Client      string      `json:"client"`
	Proto       string      `json:"proto,omitempty"`
	UpProto     string      `json:"up_proto,omitempty"`
	Scheme      string      `json:"scheme"`
	Host        string      `json:"host"`
	Port        int         `json:"port"`
	Method      string      `json:"method,omitempty"`
	Path        string      `json:"path,omitempty"`
	Query       string      `json:"query,omitempty"`
	ReqHeaders  http.Header `json:"req_headers,omitempty"`
	ReqBody     BodyRef     `json:"req_body"`
	Status      int         `json:"status,omitempty"`
	RespHeaders http.Header `json:"resp_headers,omitempty"`
	RespBody    BodyRef     `json:"resp_body"`
	Trailers    http.Header `json:"trailers,omitempty"`
	T           Timing      `json:"timing"`
	Error       string      `json:"error,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Rules       []RuleHit   `json:"rules,omitempty"`
	State       State       `json:"state"`
	Replay      bool        `json:"replay,omitempty"`
	ReplayOf    ID          `json:"replay_of,omitempty"`
}

// URL reconstructs the absolute URL.
func (f *Flow) URL() string {
	var sb strings.Builder
	sb.WriteString(f.Scheme)
	sb.WriteString("://")
	sb.WriteString(f.Host)
	if (f.Scheme == "https" && f.Port != 443) || (f.Scheme == "http" && f.Port != 80) {
		if f.Port != 0 {
			sb.WriteString(":")
			sb.WriteString(itoa(f.Port))
		}
	}
	sb.WriteString(f.Path)
	if f.Query != "" {
		sb.WriteString("?")
		sb.WriteString(f.Query)
	}
	return sb.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

// Clone returns a deep-enough copy for safe publication (headers are cloned).
func (f *Flow) Clone() *Flow {
	c := *f
	c.ReqHeaders = f.ReqHeaders.Clone()
	c.RespHeaders = f.RespHeaders.Clone()
	c.Trailers = f.Trailers.Clone()
	c.Tags = append([]string(nil), f.Tags...)
	c.Rules = append([]RuleHit(nil), f.Rules...)
	return &c
}

// IDGen hands out monotonic IDs.
type IDGen struct{ last atomic.Uint64 }

// NewIDGen starts after the given last-used ID.
func NewIDGen(last ID) *IDGen {
	g := &IDGen{}
	g.last.Store(uint64(last))
	return g
}

// Next returns the next ID.
func (g *IDGen) Next() ID { return ID(g.last.Add(1)) }

// Last returns the most recently issued ID.
func (g *IDGen) Last() ID { return ID(g.last.Load()) }
