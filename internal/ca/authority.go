package ca

import (
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SubjectKeyId per RFC 5280 uses SHA-1; not a security use.
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Options tune the authority.
type Options struct {
	CertFile, KeyFile, LeafKeyFile, CacheDir string
	LeafTTL                                  time.Duration // default 30d
	MemCache                                 int           // default 4096
	Organization                             string        // default "pano"
}

// Authority mints leaf certificates signed by a local root.
type Authority struct {
	opts    Options
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	caPEM   []byte
	leafKey *ecdsa.PrivateKey

	mu    sync.Mutex
	lru   *list.List
	index map[string]*list.Element
	sf    singleflight.Group
}

type entry struct {
	host string
	cert *tls.Certificate
}

// TargetConn is implemented by connections that know the CONNECT target, so a
// certificate can be chosen even when the ClientHello carries no SNI.
type TargetConn interface {
	net.Conn
	ConnectTarget() string
}

// Load opens the authority at the given paths, generating a root CA and leaf
// key if they do not exist. Key files must be mode 0600.
func Load(opts Options) (*Authority, error) {
	if opts.LeafTTL == 0 {
		opts.LeafTTL = 30 * 24 * time.Hour
	}
	if opts.LeafTTL > 397*24*time.Hour {
		opts.LeafTTL = 397 * 24 * time.Hour
	}
	if opts.MemCache == 0 {
		opts.MemCache = 4096
	}
	if opts.Organization == "" {
		opts.Organization = "pano"
	}
	a := &Authority{opts: opts, lru: list.New(), index: make(map[string]*list.Element)}

	if err := a.loadOrCreateRoot(); err != nil {
		return nil, err
	}
	if err := a.loadOrCreateLeafKey(); err != nil {
		return nil, err
	}
	if opts.CacheDir != "" {
		if err := os.MkdirAll(opts.CacheDir, 0o700); err != nil {
			return nil, fmt.Errorf("ca: cache dir: %w", err)
		}
	}
	return a, nil
}

// CertPEM returns the root certificate in PEM form.
func (a *Authority) CertPEM() []byte { return append([]byte(nil), a.caPEM...) }

// Root returns the parsed root certificate.
func (a *Authority) Root() *x509.Certificate { return a.caCert }

// Subject returns the root CN, used to find it in trust stores.
func (a *Authority) Subject() string { return a.caCert.Subject.CommonName }

// TLSConfig returns a server config that mints certificates on demand and
// offers HTTP/2 and HTTP/1.1 via ALPN.
func (a *Authority) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: a.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}
}

// TLSConfigForClient returns a client config that trusts this authority,
// used when pano sends requests through its own proxy (replays).
func (a *Authority) TLSConfigForClient() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(a.caCert)
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// GetCertificate implements tls.Config.GetCertificate.
func (a *Authority) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	if tc, ok := hello.Conn.(TargetConn); ok {
		if t := tc.ConnectTarget(); t != "" {
			th, _, err := net.SplitHostPort(t)
			if err != nil {
				th = t
			}
			// Prefer the CONNECT target when SNI is missing or is an ECH
			// outer name that differs from the tunnel destination.
			if host == "" || (!strings.EqualFold(host, th) && net.ParseIP(th) == nil) {
				host = th
			}
		}
	}
	if host == "" {
		return nil, errors.New("ca: no server name and no CONNECT target")
	}
	return a.CertFor(host)
}

// CertFor returns a certificate valid for host (DNS name or IP).
func (a *Authority) CertFor(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if c := a.cacheGet(host); c != nil {
		return c, nil
	}
	v, err, _ := a.sf.Do(host, func() (any, error) {
		if c := a.cacheGet(host); c != nil {
			return c, nil
		}
		if c := a.diskGet(host); c != nil {
			a.cachePut(host, c)
			return c, nil
		}
		c, err := a.issue(host)
		if err != nil {
			return nil, err
		}
		a.diskPut(host, c)
		a.cachePut(host, c)
		return c, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*tls.Certificate), nil
}

func (a *Authority) issue(host string) (*tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("ca: serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{a.opts.Organization}},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(a.opts.LeafTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		AuthorityKeyId:        a.caCert.SubjectKeyId,
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.caCert, &a.leafKey.PublicKey, a.caKey)
	if err != nil {
		return nil, fmt.Errorf("ca: sign %s: %w", host, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse leaf: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der, a.caCert.Raw}, PrivateKey: a.leafKey, Leaf: leaf}, nil
}

func (a *Authority) cacheGet(host string) *tls.Certificate {
	a.mu.Lock()
	defer a.mu.Unlock()
	el, ok := a.index[host]
	if !ok {
		return nil
	}
	e := el.Value.(*entry)
	if !fresh(e.cert.Leaf) {
		a.lru.Remove(el)
		delete(a.index, host)
		return nil
	}
	a.lru.MoveToFront(el)
	return e.cert
}

func (a *Authority) cachePut(host string, c *tls.Certificate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if el, ok := a.index[host]; ok {
		el.Value.(*entry).cert = c
		a.lru.MoveToFront(el)
		return
	}
	a.index[host] = a.lru.PushFront(&entry{host: host, cert: c})
	for a.lru.Len() > a.opts.MemCache {
		last := a.lru.Back()
		delete(a.index, last.Value.(*entry).host)
		a.lru.Remove(last)
	}
}

func fresh(c *x509.Certificate) bool {
	return c != nil && time.Until(c.NotAfter) > 5*24*time.Hour
}

func (a *Authority) diskPath(host string) string {
	if a.opts.CacheDir == "" {
		return ""
	}
	safe := strings.NewReplacer("/", "_", ":", "_", "\\", "_", "*", "_").Replace(host)
	return filepath.Join(a.opts.CacheDir, safe+".pem")
}

func (a *Authority) diskGet(host string) *tls.Certificate {
	p := a.diskPath(host)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil || !fresh(leaf) {
		return nil
	}
	// Ensure the cached cert was signed by the current root and key.
	if err := leaf.CheckSignatureFrom(a.caCert); err != nil {
		return nil
	}
	if pub, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&a.leafKey.PublicKey) {
		return nil
	}
	return &tls.Certificate{Certificate: [][]byte{leaf.Raw, a.caCert.Raw}, PrivateKey: a.leafKey, Leaf: leaf}
}

func (a *Authority) diskPut(host string, c *tls.Certificate) {
	p := a.diskPath(host)
	if p == "" {
		return
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Leaf.Raw})
	_ = os.WriteFile(p, b, 0o600)
}

func (a *Authority) loadOrCreateRoot() error {
	certB, errC := os.ReadFile(a.opts.CertFile)
	keyB, errK := os.ReadFile(a.opts.KeyFile)
	switch {
	case errC == nil && errK == nil:
		if err := checkMode(a.opts.KeyFile); err != nil {
			return err
		}
		cert, key, err := parseRoot(certB, keyB)
		if err != nil {
			return err
		}
		a.caCert, a.caKey, a.caPEM = cert, key, certB
		return nil
	case errors.Is(errC, os.ErrNotExist) && errors.Is(errK, os.ErrNotExist):
		return a.createRoot()
	default:
		if errC != nil {
			return fmt.Errorf("ca: read cert: %w", errC)
		}
		return fmt.Errorf("ca: read key: %w", errK)
	}
}

func (a *Authority) createRoot() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("ca: keygen: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return fmt.Errorf("ca: serial: %w", err)
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "local"
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("ca: marshal pub: %w", err)
	}
	skid := sha1.Sum(pubDER) //nolint:gosec // SKID, not a security hash.
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("pano Root CA (%s, %s)", host, now.Format("2006-01-02")),
			Organization: []string{a.opts.Organization},
		},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          skid[:],
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("ca: self-sign: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("ca: parse root: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("ca: marshal key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(filepath.Dir(a.opts.CertFile), 0o700); err != nil {
		return fmt.Errorf("ca: mkdir: %w", err)
	}
	if err := os.WriteFile(a.opts.KeyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("ca: write key: %w", err)
	}
	if err := os.WriteFile(a.opts.CertFile, certPEM, 0o644); err != nil { //nolint:gosec // public cert
		return fmt.Errorf("ca: write cert: %w", err)
	}
	a.caCert, a.caKey, a.caPEM = cert, key, certPEM
	return nil
}

func (a *Authority) loadOrCreateLeafKey() error {
	b, err := os.ReadFile(a.opts.LeafKeyFile)
	if err == nil {
		if err := checkMode(a.opts.LeafKeyFile); err != nil {
			return err
		}
		blk, _ := pem.Decode(b)
		if blk == nil {
			return errors.New("ca: leaf key: bad PEM")
		}
		k, err := x509.ParseECPrivateKey(blk.Bytes)
		if err != nil {
			return fmt.Errorf("ca: leaf key: %w", err)
		}
		a.leafKey = k
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("ca: read leaf key: %w", err)
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("ca: leaf keygen: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return fmt.Errorf("ca: marshal leaf key: %w", err)
	}
	if err := os.WriteFile(a.opts.LeafKeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return fmt.Errorf("ca: write leaf key: %w", err)
	}
	a.leafKey = k
	return nil
}

func parseRoot(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, nil, errors.New("ca: root cert: bad PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("ca: root cert: %w", err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, errors.New("ca: root key: bad PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("ca: root key: %w", err)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, nil, errors.New("ca: root key does not match certificate")
	}
	return cert, key, nil
}

func checkMode(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("ca: %s is group/world accessible (%o); run `chmod 600 %s`", path, fi.Mode().Perm(), path)
	}
	return nil
}
