package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func tempOpts(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	return Options{
		CertFile: filepath.Join(dir, "ca.pem"), KeyFile: filepath.Join(dir, "ca.key"),
		LeafKeyFile: filepath.Join(dir, "leaf.key"), CacheDir: filepath.Join(dir, "certs"),
	}
}

func TestCreateAndReload(t *testing.T) {
	opts := tempOpts(t)
	a, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Root().IsCA || a.Root().MaxPathLen != 0 || !a.Root().MaxPathLenZero {
		t.Fatalf("root constraints: %+v", a.Root().BasicConstraintsValid)
	}
	if fi, _ := os.Stat(opts.KeyFile); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o", fi.Mode().Perm())
	}
	b, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Root().Equal(a.Root()) {
		t.Fatal("reload produced a different root")
	}
	if len(b.CertPEM()) == 0 || b.Subject() == "" {
		t.Fatal("pem/subject empty")
	}
}

func TestRefusesWorldReadableKey(t *testing.T) {
	opts := tempOpts(t)
	if _, err := Load(opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(opts.KeyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(opts); err == nil {
		t.Fatal("expected refusal for 0644 key")
	}
}

func TestLeafForDNSAndIP(t *testing.T) {
	a, err := Load(tempOpts(t))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(a.Root())
	for _, host := range []string{"api.example.com", "10.0.0.5", "EXAMPLE.org."} {
		c, err := a.CertFor(host)
		if err != nil {
			t.Fatal(err)
		}
		want := host
		if host == "EXAMPLE.org." {
			want = "example.org"
		}
		if _, err := c.Leaf.Verify(x509.VerifyOptions{DNSName: want, Roots: pool}); err != nil {
			t.Fatalf("verify %s: %v", host, err)
		}
		if len(c.Leaf.ExtKeyUsage) != 1 || c.Leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
			t.Fatal("missing serverAuth EKU")
		}
		if time.Until(c.Leaf.NotAfter) > 398*24*time.Hour {
			t.Fatal("leaf lifetime too long for Apple policy")
		}
	}
	ipc, _ := a.CertFor("10.0.0.5")
	if len(ipc.Leaf.IPAddresses) != 1 || !ipc.Leaf.IPAddresses[0].Equal(net.ParseIP("10.0.0.5")) {
		t.Fatal("IP SAN missing")
	}
}

func TestCacheAndDisk(t *testing.T) {
	opts := tempOpts(t)
	a, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	c1, _ := a.CertFor("cached.example")
	c2, _ := a.CertFor("cached.example")
	if c1 != c2 {
		t.Fatal("memory cache miss")
	}
	if _, err := os.Stat(filepath.Join(opts.CacheDir, "cached.example.pem")); err != nil {
		t.Fatal("disk cache not written")
	}
	// A fresh authority with the same files must reuse the disk cert.
	b, _ := Load(opts)
	c3, _ := b.CertFor("cached.example")
	if !c3.Leaf.Equal(c1.Leaf) {
		t.Fatal("disk cache not reused")
	}
	// LRU eviction.
	small, _ := Load(Options{CertFile: opts.CertFile, KeyFile: opts.KeyFile, LeafKeyFile: opts.LeafKeyFile, MemCache: 2})
	for _, h := range []string{"a", "b", "c"} {
		if _, err := small.CertFor(h); err != nil {
			t.Fatal(err)
		}
	}
	if small.cacheGet("a") != nil {
		t.Fatal("expected a to be evicted")
	}
}

func TestSingleflightConcurrent(t *testing.T) {
	a, _ := Load(tempOpts(t))
	var wg sync.WaitGroup
	certs := make([]*tls.Certificate, 32)
	for i := range certs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			certs[i], _ = a.CertFor("burst.example")
		}(i)
	}
	wg.Wait()
	for _, c := range certs {
		if c == nil || !c.Leaf.Equal(certs[0].Leaf) {
			t.Fatal("concurrent callers got different certs")
		}
	}
}

type fakeTargetConn struct {
	net.Conn
	target string
}

func (f fakeTargetConn) ConnectTarget() string { return f.target }

func TestGetCertificateUsesConnectTarget(t *testing.T) {
	a, _ := Load(tempOpts(t))
	// No SNI: fall back to the tunnel target.
	c, err := a.GetCertificate(&tls.ClientHelloInfo{Conn: fakeTargetConn{target: "nosni.example:443"}})
	if err != nil || c.Leaf.DNSNames[0] != "nosni.example" {
		t.Fatalf("fallback: %v %+v", err, c)
	}
	// ECH-style outer SNI that disagrees with the tunnel target: prefer the target.
	c, _ = a.GetCertificate(&tls.ClientHelloInfo{ServerName: "public.cloudflare-ech.com", Conn: fakeTargetConn{target: "real.example:443"}})
	if c.Leaf.DNSNames[0] != "real.example" {
		t.Fatalf("ech: got %v", c.Leaf.DNSNames)
	}
	// Neither: error.
	if _, err := a.GetCertificate(&tls.ClientHelloInfo{}); err == nil {
		t.Fatal("expected error without SNI or target")
	}
	cfg := a.TLSConfig()
	if cfg.NextProtos[0] != "h2" || cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("tls config %+v", cfg)
	}
}

// writeRoot puts a self-signed root with the given validity at opts' paths so
// tests can exercise expiry without waiting.
func writeRoot(t *testing.T, opts Options, notBefore, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: SubjectPrefix + "test, 2000-01-01)"},
		NotBefore:    notBefore, NotAfter: notAfter,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	if err := os.MkdirAll(filepath.Dir(opts.CertFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return tmpl.Subject.CommonName
}

func TestRootLifetimeIsBounded(t *testing.T) {
	a, err := Load(tempOpts(t))
	if err != nil {
		t.Fatal(err)
	}
	left := time.Until(a.NotAfter())
	if left > DefaultRootTTL || left < DefaultRootTTL-time.Hour {
		t.Fatalf("root validity %v, want ≈ %v", left, DefaultRootTTL)
	}
	if !strings.HasPrefix(a.Subject(), SubjectPrefix) {
		t.Fatalf("subject %q lacks prefix %q", a.Subject(), SubjectPrefix)
	}
	if a.RotatedFrom() != "" || a.ExpiryWarning() != "" {
		t.Fatalf("fresh root: rotated=%q warning=%q", a.RotatedFrom(), a.ExpiryWarning())
	}
	// Callers cannot ask for a longer-lived root than MaxRootTTL.
	opts := tempOpts(t)
	opts.RootTTL = 50 * 365 * 24 * time.Hour
	b, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(b.NotAfter()) > MaxRootTTL {
		t.Fatalf("root TTL not capped: %v", time.Until(b.NotAfter()))
	}
}

func TestRootIsUniquePerInstall(t *testing.T) {
	a, _ := Load(tempOpts(t))
	b, _ := Load(tempOpts(t))
	if a.Root().Equal(b.Root()) || a.caKey.Equal(b.caKey) || a.leafKey.Equal(b.leafKey) {
		t.Fatal("two installs share key material")
	}
}

func TestLeafNeverOutlivesRoot(t *testing.T) {
	opts := tempOpts(t)
	opts.RootTTL = 10 * 24 * time.Hour // shorter than the 30-day leaf default
	a, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.CertFor("short.example")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Leaf.NotAfter.Equal(a.NotAfter()) {
		t.Fatalf("leaf NotAfter %v, root %v", c.Leaf.NotAfter, a.NotAfter())
	}
	// A leaf pinned to root expiry is still cacheable even though it has
	// fewer than five days of slack relative to a normal leaf.
	a.opts.RootTTL = 2 * 24 * time.Hour
	if c2, _ := a.CertFor("short.example"); c2 != c {
		t.Fatal("pinned leaf was re-minted instead of served from cache")
	}
	if a.ExpiryWarning() == "" || !strings.Contains(a.ExpiryWarning(), "9 days") {
		t.Fatalf("expected a renewal warning, got %q", a.ExpiryWarning())
	}
}

func TestExpiredRootIsRotated(t *testing.T) {
	opts := tempOpts(t)
	old := writeRoot(t, opts, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	if err := os.MkdirAll(opts.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(opts.CacheDir, "stale.example.pem")
	if err := os.WriteFile(stale, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.RotatedFrom() != old {
		t.Fatalf("RotatedFrom = %q, want %q", a.RotatedFrom(), old)
	}
	if a.Subject() == old || !time.Now().Before(a.NotAfter()) {
		t.Fatalf("root not replaced: %s until %v", a.Subject(), a.NotAfter())
	}
	if !strings.Contains(a.ExpiryWarning(), "pano ca install") {
		t.Fatalf("warning should tell the user to re-trust: %q", a.ExpiryWarning())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("leaf cache signed by the old root survived rotation")
	}
	if fi, _ := os.Stat(opts.KeyFile); fi.Mode().Perm() != 0o600 {
		t.Fatalf("new key mode %o", fi.Mode().Perm())
	}
	// The replacement is persisted: a second Load reuses it quietly.
	b, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Root().Equal(a.Root()) || b.RotatedFrom() != "" {
		t.Fatal("rotated root not persisted")
	}
}

func TestExpiryWarningNearEnd(t *testing.T) {
	opts := tempOpts(t)
	writeRoot(t, opts, time.Now().Add(-time.Hour), time.Now().Add(10*24*time.Hour+time.Hour))
	a, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if w := a.ExpiryWarning(); !strings.Contains(w, "10 days") || !strings.Contains(w, "pano ca reset") {
		t.Fatalf("warning %q", w)
	}
	if a.RotatedFrom() != "" {
		t.Fatal("a still-valid root must not be rotated")
	}
}
