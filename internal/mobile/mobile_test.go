package mobile

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/testutil"
)

func testCert(t *testing.T) Certificate {
	t.Helper()
	a := testutil.TempCA(t)
	c, err := ParseCertificate(a.CertPEM())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMobileConfig(t *testing.T) {
	c := testCert(t)
	if !strings.Contains(c.Subject, "pano") {
		t.Fatalf("subject %q", c.Subject)
	}
	if fp := c.Fingerprint(); len(fp) != 95 || strings.Count(fp, ":") != 31 {
		t.Fatalf("fingerprint %q", fp)
	}
	prof := MobileConfig(c, "Dev's Mac <test>")
	// Well-formed XML, escaped machine name, and the DER round-trips.
	dec := xml.NewDecoder(bytes.NewReader(prof))
	var data string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "data" {
			var v string
			if err := dec.DecodeElement(&v, &se); err != nil {
				t.Fatal(err)
			}
			data = strings.Join(strings.Fields(v), "")
		}
	}
	der, err := base64.StdEncoding.DecodeString(data)
	if err != nil || !bytes.Equal(der, c.DER) {
		t.Fatalf("DER payload mismatch: %v", err)
	}
	s := string(prof)
	for _, want := range []string{"com.apple.security.root", "<string>pano CA</string>", "Dev&#39;s Mac &lt;test&gt;", "PayloadRemovalDisallowed"} {
		if !strings.Contains(s, want) {
			t.Errorf("profile lacks %q", want)
		}
	}
	if MobileConfig(c, "x")[0] != prof[0] || !bytes.Equal(MobileConfig(c, "Dev's Mac <test>"), prof) {
		t.Fatal("profile should be deterministic")
	}
}

func TestParseCertificateRejectsGarbage(t *testing.T) {
	if _, err := ParseCertificate([]byte("not pem")); err == nil {
		t.Fatal("expected error")
	}
}

func TestPlatform(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X)":                                  "ios",
		"Mozilla/5.0 (iPad; CPU OS 16_2 like Mac OS X)":                                           "ios",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Version/17.5 Mobile/15E148 Safari/604.1": "ios",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8)":                                                "android",
		"curl/8.4.0": "other",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) Safari/605.1.15": "other",
	}
	for ua, want := range cases {
		if got := platform(ua); got != want {
			t.Errorf("platform(%q) = %q, want %q", ua, got, want)
		}
	}
}

func newTestSite(t *testing.T, enabled bool) *Site {
	t.Helper()
	dev := api.Device{IP: "192.168.1.40", Name: "iPhone · iOS 17.5", Proxy: true, Requests: 3}
	return NewSite(SiteOptions{
		Cert: testCert(t), Machine: "Dev's Mac", Version: "test",
		Mobile: func() api.Mobile {
			if !enabled {
				return api.Mobile{}
			}
			return api.Mobile{Enabled: true, IP: "192.168.1.23", Port: 9091, Addr: "192.168.1.23:9091"}
		},
		Device: func(addr string) (api.Device, bool) {
			if strings.HasPrefix(addr, "192.168.1.40:") {
				return dev, true
			}
			return api.Device{}, false
		},
	})
}

func get(t *testing.T, s *Site, path, ua, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("User-Agent", ua)
	if remote != "" {
		r.RemoteAddr = remote
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestSitePage(t *testing.T) {
	s := newTestSite(t, true)
	w := get(t, s, "/", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X)", "192.168.1.40:5000")
	b := w.Body.String()
	for _, want := range []string{`data-platform="ios"`, "192.168.1.23", "9091", "Dev&#39;s Mac", "pano-ca.mobileconfig", "pano-ca.crt", "pano.internal"} {
		if !strings.Contains(b, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(b, "LAN listener is off") {
		t.Error("enabled page should not warn")
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control %q", got)
	}
	// Off: reached over loopback, the page shows the address it was opened at.
	off := get(t, newTestSite(t, false), "/", "curl", "127.0.0.1:1")
	if b := off.Body.String(); !strings.Contains(b, "LAN listener is off") || !strings.Contains(b, `data-platform="other"`) {
		t.Errorf("off page: %.200s", b)
	}
}

func TestSiteStatus(t *testing.T) {
	s := newTestSite(t, true)
	w := get(t, s, "/_pano/setup.json", "x", "192.168.1.40:5000")
	b := w.Body.String()
	for _, want := range []string{`"ip":"192.168.1.40"`, `"proxy":true`, `"tls":false`, `"via":"direct"`, `"name":"iPhone · iOS 17.5"`, `"proxy_port":9091`} {
		if !strings.Contains(b, want) {
			t.Errorf("status lacks %q in %s", want, b)
		}
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("status needs CORS for the https probe origin")
	}
	// Arriving as a proxy request proves the proxy setting works.
	w = get(t, s, "http://pano.internal/_pano/setup.json", "x", "192.168.1.50:5000")
	if b := w.Body.String(); !strings.Contains(b, `"via":"proxy"`) || !strings.Contains(b, `"proxy":true`) {
		t.Errorf("proxied status: %s", b)
	}
	if w := get(t, s, "/_pano/ok", "x", ""); w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("ok: %d", w.Code)
	}
}

func TestSiteCertificates(t *testing.T) {
	s := newTestSite(t, true)
	c := s.opts.Cert
	cases := []struct{ path, ua, ctype, name string }{
		{"/_pano/pano-ca.mobileconfig", "x", "application/x-apple-aspen-config", "pano-ca.mobileconfig"},
		{"/_pano/pano-ca.crt", "x", "application/x-x509-ca-cert", "pano-ca.crt"},
		{"/_pano/pano-ca.pem", "x", "application/x-pem-file", "pano-ca.pem"},
		{"/ca.pem", "x", "application/x-pem-file", "pano-ca.pem"},
		{"/ssl", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X)", "application/x-apple-aspen-config", "pano-ca.mobileconfig"},
		{"/ssl", "Mozilla/5.0 (Linux; Android 14; Pixel 8)", "application/x-x509-ca-cert", "pano-ca.crt"},
		{"/cert", "curl", "application/x-pem-file", "pano-ca.pem"},
	}
	for _, tc := range cases {
		w := get(t, s, tc.path, tc.ua, "")
		if w.Code != 200 || w.Header().Get("Content-Type") != tc.ctype || !strings.Contains(w.Header().Get("Content-Disposition"), tc.name) {
			t.Errorf("%s (%s): %d %s %s", tc.path, tc.ua, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Content-Disposition"))
		}
		if tc.ctype == "application/x-x509-ca-cert" && !bytes.Equal(w.Body.Bytes(), c.DER) {
			t.Errorf("%s: DER mismatch", tc.path)
		}
	}
	if w := get(t, s, "/nope", "x", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown path: %d", w.Code)
	}
}

func TestPrivateOnly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := PrivateOnly(ln)
	defer p.Close()
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			c.Close()
		}
	}()
	c, err := p.Accept()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, ok := p.(*privateListener); !ok {
		t.Fatal("wrapper type")
	}
}

func TestDetect(t *testing.T) {
	l, err := Detect()
	if errors.Is(err, ErrNoLAN) {
		t.Skip("no LAN on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(l.IP); ip == nil || !ip.IsPrivate() || l.Interface == "" {
		t.Fatalf("lan %+v", l)
	}
	if MachineName() == "" {
		t.Fatal("machine name")
	}
}

func TestInterfaceRanking(t *testing.T) {
	if ifaceRank("en0") != 0 || ifaceRank("wlan0") != 0 || ifaceRank("en5") != 1 || ifaceRank("eth0") != 1 || ifaceRank("bond0") != 2 {
		t.Fatal("ranking")
	}
	for _, n := range []string{"utun3", "awdl0", "bridge100", "docker0", "vmnet8"} {
		if !skipInterface(n) {
			t.Errorf("%s should be skipped", n)
		}
	}
	if skipInterface("en0") {
		t.Error("en0 skipped")
	}
}
