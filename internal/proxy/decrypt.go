package proxy

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orron/pano/internal/glob"
)

// DecryptMode says which CONNECT tunnels are TLS-terminated.
type DecryptMode string

// Decrypt modes.
const (
	// DecryptAll decrypts every host except those on the never list.
	DecryptAll DecryptMode = "all"
	// DecryptOnly decrypts only hosts on the only list (never still wins).
	DecryptOnly DecryptMode = "only"
	// DecryptOff decrypts nothing: every tunnel is spliced through.
	DecryptOff DecryptMode = "off"
)

// Tunnel reasons recorded as the tag on undecrypted tunnel flows.
const (
	ReasonNever    = "never"    // host is on the never list
	ReasonUnlisted = "unlisted" // mode "only" and host is not on the only list
	ReasonOff      = "off"      // mode "off"
)

// DecryptPolicy decides, per host, whether a tunnel is decrypted. Never wins
// in every mode; Only is consulted only in mode "only".
type DecryptPolicy struct {
	Mode  DecryptMode
	Only  []string
	Never []string
}

// ErrBadPattern is wrapped by NormalizeHost for entries that cannot match anything.
var ErrBadPattern = errors.New("bad host pattern")

// ParseDecryptMode validates a mode string.
func ParseDecryptMode(s string) (DecryptMode, error) {
	switch m := DecryptMode(strings.ToLower(strings.TrimSpace(s))); m {
	case DecryptAll, DecryptOnly, DecryptOff:
		return m, nil
	default:
		return "", fmt.Errorf("decrypt mode must be all, only or off (got %q)", s)
	}
}

// Decide reports whether host is decrypted and, when it is not, the reason
// tag for the tunnel flow.
func (p DecryptPolicy) Decide(host string) (decrypt bool, reason string) {
	if HostMatchAny(p.Never, host) {
		return false, ReasonNever
	}
	switch p.Mode {
	case DecryptOff:
		return false, ReasonOff
	case DecryptOnly:
		if HostMatchAny(p.Only, host) {
			return true, ""
		}
		return false, ReasonUnlisted
	default:
		return true, ""
	}
}

// Clone returns a deep copy.
func (p DecryptPolicy) Clone() DecryptPolicy {
	return DecryptPolicy{
		Mode:  p.Mode,
		Only:  append([]string(nil), p.Only...),
		Never: append([]string(nil), p.Never...),
	}
}

// HostMatch reports whether host matches pattern. A pattern containing '*'
// or '?' is a glob (see package glob). A bare domain matches itself and every
// subdomain: "whatsapp.net" covers "mmg.whatsapp.net". Matching is
// case-insensitive.
func HostMatch(pattern, host string) bool {
	if glob.IsPattern(pattern) {
		return glob.Match(pattern, host)
	}
	p, h := strings.ToLower(pattern), strings.ToLower(host)
	return h == p || strings.HasSuffix(h, "."+p)
}

// HostMatchAny reports whether host matches any pattern.
func HostMatchAny(patterns []string, host string) bool {
	for _, p := range patterns {
		if HostMatch(p, host) {
			return true
		}
	}
	return false
}

// NormalizeHost canonicalises a list entry: trims space, lowercases, strips a
// trailing dot and a :port suffix. It rejects empty entries and entries with
// spaces or slashes (someone pasted a URL).
func NormalizeHost(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(h, "://") || strings.ContainsAny(h, " \t/") {
		return "", fmt.Errorf("%w: %q (use a host or glob like api.example.com or *.example.com)", ErrBadPattern, s)
	}
	if hp, _, err := net.SplitHostPort(h); err == nil {
		h = hp
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", fmt.Errorf("%w: empty", ErrBadPattern)
	}
	return h, nil
}

// RejectedHost is a host whose client refused pano's certificate recently —
// the usual sign of certificate pinning. It is a suggestion for the never
// list, never applied automatically.
type RejectedHost struct {
	Host  string
	Count int
	First time.Time
	Last  time.Time
	Error string
}

// rejectedRing remembers recent handshake rejections per host.
type rejectedRing struct {
	mu     sync.Mutex
	hosts  map[string]*RejectedHost
	window time.Duration
	limit  int
	now    func() time.Time
}

const (
	rejectedWindow = time.Hour
	rejectedLimit  = 256
)

func newRejectedRing() *rejectedRing {
	return &rejectedRing{hosts: map[string]*RejectedHost{}, window: rejectedWindow, limit: rejectedLimit, now: time.Now}
}

// add records one rejection for host.
func (r *rejectedRing) add(host, errText string) {
	if host == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.pruneLocked(now)
	if e, ok := r.hosts[host]; ok {
		e.Count++
		e.Last = now
		e.Error = errText
		return
	}
	if len(r.hosts) >= r.limit {
		// Evict the stalest entry.
		var oldest string
		var oldestAt time.Time
		for h, e := range r.hosts {
			if oldest == "" || e.Last.Before(oldestAt) {
				oldest, oldestAt = h, e.Last
			}
		}
		delete(r.hosts, oldest)
	}
	r.hosts[host] = &RejectedHost{Host: host, Count: 1, First: now, Last: now, Error: errText}
}

// list returns the entries seen within the window, most frequent first.
func (r *rejectedRing) list() []RejectedHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now())
	out := make([]RejectedHost, 0, len(r.hosts))
	for _, e := range r.hosts {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// forget drops every host matched by patterns (called when the never list grows).
func (r *rejectedRing) forget(patterns []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h := range r.hosts {
		if HostMatchAny(patterns, h) {
			delete(r.hosts, h)
		}
	}
}

func (r *rejectedRing) pruneLocked(now time.Time) {
	for h, e := range r.hosts {
		if now.Sub(e.Last) > r.window {
			delete(r.hosts, h)
		}
	}
}
