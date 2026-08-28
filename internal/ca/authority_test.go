package ca

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
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
