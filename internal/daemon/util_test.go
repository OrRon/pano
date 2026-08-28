package daemon

import (
	"crypto/x509"
	"testing"
)

func x509Pool(t *testing.T, pem []byte) *x509.CertPool {
	t.Helper()
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(pem) {
		t.Fatal("bad CA pem")
	}
	return p
}
