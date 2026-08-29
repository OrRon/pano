package proxy

import (
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// MagicHost is the name a client can open through the proxy — any scheme,
// any port — to reach pano's own setup site instead of an origin. It never
// resolves in DNS on purpose: a browser only sees it once its requests are
// already routed through pano, so the same URL works on every device and on
// every network. `.internal` is ICANN-reserved for private use.
const MagicHost = "pano.internal"

// Device is one remote client of the proxy (a phone, a tablet, another
// machine), identified by its IP. It summarises how far that client has got
// through setup: has it sent anything via the proxy, has it accepted pano's
// certificate, or refused it.
type Device struct {
	IP        string
	Name      string // derived from the first User-Agent seen, e.g. "iPhone · iOS 17.5"
	UserAgent string
	FirstSeen time.Time
	LastSeen  time.Time
	Requests  int // proxied requests and tunnels
	Decrypted int // TLS handshakes with pano's certificate that succeeded
	Rejected  int // handshakes the client refused (certificate not trusted yet, or pinning)
}

// ProxyOK reports whether the device has routed anything through pano.
func (d Device) ProxyOK() bool { return d.Requests > 0 }

// TLSOK reports whether the device has ever accepted pano's certificate.
func (d Device) TLSOK() bool { return d.Decrypted > 0 }

const maxDevices = 64

// deviceTable remembers the most recently seen remote clients.
type deviceTable struct {
	mu sync.Mutex
	m  map[string]*Device
}

func newDeviceTable() *deviceTable { return &deviceTable{m: make(map[string]*Device)} }

// clientIP strips the port from a client address. Loopback clients are the
// Mac itself and are not tracked.
func clientIP(addr string) (string, bool) {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		h = addr
	}
	ip := net.ParseIP(h)
	if ip == nil || ip.IsLoopback() {
		return "", false
	}
	return ip.String(), true
}

func (t *deviceTable) touch(addr string, fn func(d *Device)) {
	ip, ok := clientIP(addr)
	if !ok {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.m[ip]
	if d == nil {
		if len(t.m) >= maxDevices {
			t.evictLocked()
		}
		d = &Device{IP: ip, FirstSeen: now}
		t.m[ip] = d
	}
	d.LastSeen = now
	if fn != nil {
		fn(d)
	}
}

func (t *deviceTable) evictLocked() {
	var oldest *Device
	for _, d := range t.m {
		if oldest == nil || d.LastSeen.Before(oldest.LastSeen) {
			oldest = d
		}
	}
	if oldest != nil {
		delete(t.m, oldest.IP)
	}
}

func (t *deviceTable) get(addr string) (Device, bool) {
	ip, ok := clientIP(addr)
	if !ok {
		return Device{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.m[ip]
	if d == nil {
		return Device{}, false
	}
	return *d, true
}

func (t *deviceTable) list() []Device {
	t.mu.Lock()
	out := make([]Device, 0, len(t.m))
	for _, d := range t.m {
		out = append(out, *d)
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// Devices lists remote clients seen by the proxy, most recent first. Loopback
// clients are never included.
func (s *Server) Devices() []Device { return s.devices.list() }

// Device looks up one remote client by address or IP.
func (s *Server) Device(addr string) (Device, bool) { return s.devices.get(addr) }

// noteRequest records a proxied request or tunnel from a client. count is
// false for requests inside a tunnel that was already counted; they still
// contribute a User-Agent, which names the device the first time a good one
// shows up.
func (s *Server) noteRequest(addr, userAgent string, count bool) {
	s.devices.touch(addr, func(d *Device) {
		if count {
			d.Requests++
		}
		if name := DeviceName(userAgent); name != "" && (d.UserAgent == "" || betterName(name, d.Name)) {
			d.UserAgent, d.Name = userAgent, name
		}
	})
}

func (s *Server) noteHandshake(addr string, ok bool) {
	s.devices.touch(addr, func(d *Device) {
		if ok {
			d.Decrypted++
		} else {
			d.Rejected++
		}
	})
}

// betterName prefers a name that says what the device is over a generic one.
func betterName(candidate, current string) bool {
	return current == "" || (strings.HasPrefix(current, "iOS") || current == "Android" || current == "device") && !strings.HasPrefix(candidate, "iOS") && candidate != "Android" && candidate != "device"
}

// DeviceName guesses a short human name for a client from its User-Agent:
// "iPhone · iOS 17.5", "Pixel 8 · Android 14", "iPad", "Mac", or "" when the
// agent says nothing useful.
func DeviceName(ua string) string {
	if ua == "" {
		return ""
	}
	switch {
	case strings.Contains(ua, "iPhone"):
		return withVersion("iPhone", iosVersion(ua))
	case strings.Contains(ua, "iPad"):
		return withVersion("iPad", iosVersion(ua))
	case strings.Contains(ua, "Android"):
		return androidName(ua)
	case strings.Contains(ua, "Macintosh"):
		return "Mac"
	case strings.Contains(ua, "Windows"):
		return "Windows PC"
	case strings.Contains(ua, "CFNetwork") && strings.Contains(ua, "Darwin"):
		// Native Apple app: "MyApp/1.0 CFNetwork/1494.0.7 Darwin/23.4.0".
		return "iOS device"
	case strings.Contains(ua, "okhttp") || strings.Contains(ua, "Dalvik"):
		return "Android"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	}
	return ""
}

func withVersion(name, v string) string {
	if v == "" {
		return name
	}
	return name + " · iOS " + v
}

// iosVersion pulls "17.5" out of "... CPU iPhone OS 17_5 like Mac OS X ...".
func iosVersion(ua string) string {
	i := strings.Index(ua, " OS ")
	if i < 0 {
		return ""
	}
	rest := ua[i+4:]
	end := strings.IndexAny(rest, " ;)")
	if end < 0 {
		end = len(rest)
	}
	v := strings.ReplaceAll(rest[:end], "_", ".")
	if v == "" || v[0] < '0' || v[0] > '9' {
		return ""
	}
	return v
}

// androidName turns "Mozilla/5.0 (Linux; Android 14; Pixel 8 Build/…) …"
// into "Pixel 8 · Android 14".
func androidName(ua string) string {
	i := strings.Index(ua, "Android ")
	if i < 0 {
		return "Android"
	}
	rest := ua[i+len("Android "):]
	end := strings.IndexAny(rest, ";)")
	if end < 0 {
		return "Android"
	}
	ver := strings.TrimSpace(rest[:end])
	model := ""
	if rest[end] == ';' {
		after := rest[end+1:]
		if j := strings.IndexAny(after, ";)"); j >= 0 {
			model = strings.TrimSpace(after[:j])
		}
		model = strings.TrimSpace(strings.TrimSuffix(model, "Build"))
		if k := strings.Index(model, " Build/"); k >= 0 {
			model = model[:k]
		}
		if model == "K" || strings.EqualFold(model, "wv") || strings.Contains(model, "/") {
			model = "" // Chrome's reduced UA reports "K"
		}
	}
	out := "Android"
	if ver != "" {
		out += " " + ver
	}
	if model != "" {
		return model + " · " + out
	}
	return out
}
