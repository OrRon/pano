package mobile

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/orron/pano/internal/api"
)

//go:embed site.html
var siteHTML string

var siteTmpl = template.Must(template.New("site").Parse(siteHTML))

// SiteOptions wire the setup site to the daemon.
type SiteOptions struct {
	Cert    Certificate
	Machine string            // the Mac's name, shown on the page and in the profile
	Version string            // pano version
	Mobile  func() api.Mobile // current LAN listener state (IP, port)
	Device  func(addr string) (api.Device, bool)
}

// Site serves pano's own pages on the proxy port.
type Site struct {
	opts SiteOptions
	mux  *http.ServeMux
}

// NewSite builds the handler mounted as proxy.Options.Local.
func NewSite(opts SiteOptions) *Site {
	s := &Site{opts: opts, mux: http.NewServeMux()}
	m := s.mux
	m.HandleFunc("GET /{$}", s.page)
	m.HandleFunc("GET /_pano", s.page)
	m.HandleFunc("GET /_pano/{$}", s.page)
	m.HandleFunc("GET /_pano/setup.json", s.status)
	m.HandleFunc("GET /_pano/ok", s.ok)
	m.HandleFunc("GET /_pano/pano-ca.mobileconfig", s.mobileconfig)
	m.HandleFunc("GET /_pano/pano-ca.crt", s.der)
	m.HandleFunc("GET /_pano/pano-ca.pem", s.pem)
	m.HandleFunc("GET /_pano/ca.pem", s.pem) // pre-mobile path, kept
	m.HandleFunc("GET /ca.pem", s.pem)
	m.HandleFunc("GET /ssl", s.smartCert) // the URL other proxies taught people: proxy.man/ssl, chls.pro/ssl
	m.HandleFunc("GET /cert", s.smartCert)
	m.HandleFunc("GET /ca", s.smartCert)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

type pageData struct {
	Machine, Version, IP, Port, Subject, Fingerprint, Expires, Platform string
	Enabled                                                             bool
}

func (s *Site) page(w http.ResponseWriter, r *http.Request) {
	m := s.opts.Mobile()
	d := pageData{
		Machine: s.opts.Machine, Version: s.opts.Version, IP: m.IP, Enabled: m.Enabled,
		Subject: s.opts.Cert.Subject, Fingerprint: s.opts.Cert.Fingerprint(), Platform: platform(r.UserAgent()),
	}
	if m.Port > 0 {
		d.Port = itoa(m.Port)
	}
	if !m.Enabled {
		// Reached over loopback (the Mac itself) with the LAN listener off:
		// show the page for the Mac's own address so the links still work.
		host, port, _ := net.SplitHostPort(r.Host)
		if host == "" {
			host = r.Host
		}
		d.IP, d.Port = host, port
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = siteTmpl.Execute(w, d)
}

// setupStatus is what the page polls.
type setupStatus struct {
	IP        string `json:"ip"`
	Name      string `json:"name,omitempty"`
	Proxy     bool   `json:"proxy"`
	TLS       bool   `json:"tls"`
	Rejected  int    `json:"rejected"`
	Requests  int    `json:"requests"`
	Decrypted int    `json:"decrypted"`
	Via       string `json:"via"` // "direct" or "proxy": how this very request arrived
	Machine   string `json:"machine"`
	ProxyIP   string `json:"proxy_ip"`
	ProxyPort int    `json:"proxy_port"`
}

func (s *Site) status(w http.ResponseWriter, r *http.Request) {
	st := setupStatus{Machine: s.opts.Machine, Via: "direct"}
	if r.URL.IsAbs() {
		st.Via = "proxy"
	}
	m := s.opts.Mobile()
	st.ProxyIP, st.ProxyPort = m.IP, m.Port
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		st.IP = ip
	}
	if d, ok := s.opts.Device(r.RemoteAddr); ok {
		st.Name, st.Proxy, st.TLS = d.Name, d.Proxy, d.TLS
		st.Rejected, st.Requests, st.Decrypted = d.Rejected, d.Requests, d.Decrypted
	}
	if st.Via == "proxy" {
		st.Proxy = true
	}
	cors(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// ok answers the page's HTTPS probe: a request for https://pano.internal/_pano/ok
// only gets here if the client completed a TLS handshake with pano's
// certificate, so a 204 means "trusted".
func (s *Site) ok(w http.ResponseWriter, r *http.Request) {
	cors(w)
	w.WriteHeader(http.StatusNoContent)
}

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET")
}

func (s *Site) mobileconfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", `attachment; filename="pano-ca.mobileconfig"`)
	_, _ = w.Write(MobileConfig(s.opts.Cert, s.opts.Machine))
}

func (s *Site) der(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="pano-ca.crt"`)
	_, _ = w.Write(s.opts.Cert.DER)
}

func (s *Site) pem(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="pano-ca.pem"`)
	_, _ = w.Write(s.opts.Cert.PEM)
}

// smartCert hands out the format the requesting platform installs from a
// browser: a profile for Apple devices, DER for Android, PEM otherwise.
func (s *Site) smartCert(w http.ResponseWriter, r *http.Request) {
	switch platform(r.UserAgent()) {
	case "ios":
		s.mobileconfig(w, r)
	case "android":
		s.der(w, r)
	default:
		s.pem(w, r)
	}
}

// platform classifies a User-Agent into the setup flow to show.
func platform(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "ios"
	case strings.Contains(ua, "Macintosh") && strings.Contains(ua, "Mobile"): // iPadOS Safari asks for desktop sites
		return "ios"
	case strings.Contains(ua, "Android"):
		return "android"
	}
	return "other"
}

func itoa(n int) string {
	if n == 0 {
		return ""
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Expires formats the CA's end of validity for the page.
func Expires(t time.Time) string { return t.Format("2 Jan 2006") }
