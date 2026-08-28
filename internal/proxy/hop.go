package proxy

import (
	"net/http"
	"strings"
)

// hopHeaders are never forwarded end-to-end.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// stripHop removes hop-by-hop headers, including those named by Connection.
// Upgrade/Connection are kept when websocket is true (handled separately).
func stripHop(h http.Header, websocket bool) {
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if websocket && strings.EqualFold(name, "upgrade") {
				continue
			}
			h.Del(name)
		}
	}
	for _, name := range hopHeaders {
		if websocket && (name == "Connection" || name == "Upgrade") {
			continue
		}
		if name == "Te" {
			// RFC 7230: TE may carry "trailers" end-to-end.
			if strings.EqualFold(strings.TrimSpace(h.Get("Te")), "trailers") {
				continue
			}
		}
		h.Del(name)
	}
}

// isWebSocket reports whether r is an HTTP/1.1 WebSocket upgrade.
func isWebSocket(r *http.Request) bool {
	if r.ProtoMajor != 1 {
		return false
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), "upgrade") {
				return true
			}
		}
	}
	return false
}
