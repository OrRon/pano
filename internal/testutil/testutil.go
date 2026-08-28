package testutil

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/orron/pano/internal/ca"
)

// TempCA creates an authority in a temp dir.
func TempCA(t testing.TB) *ca.Authority {
	t.Helper()
	dir := t.TempDir()
	a, err := ca.Load(ca.Options{
		CertFile: filepath.Join(dir, "ca.pem"), KeyFile: filepath.Join(dir, "ca.key"),
		LeafKeyFile: filepath.Join(dir, "leaf.key"), CacheDir: filepath.Join(dir, "certs"),
	})
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	return a
}

// Pool returns a cert pool trusting only a.
func Pool(a *ca.Authority) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(a.Root())
	return p
}

// ProxiedClient returns an http.Client that routes via proxyAddr and trusts
// only the given pool. h2 selects HTTP/2 to the proxy's TLS endpoint.
func ProxiedClient(proxyAddr string, pool *x509.CertPool, h2 bool) *http.Client {
	tr := &http.Transport{
		Proxy:               http.ProxyURL(&url.URL{Scheme: "http", Host: proxyAddr}),
		TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   h2,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 4,
	}
	if !h2 {
		tr.TLSClientConfig.NextProtos = []string{"http/1.1"}
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}
}
