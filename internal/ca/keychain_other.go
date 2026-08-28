//go:build !darwin

package ca

import (
	"context"
	"fmt"
)

type noStore struct{}

// NewTrustStore returns a store that only prints instructions.
func NewTrustStore() TrustStore { return noStore{} }

func (noStore) Install(context.Context, string, string, bool) error { return ErrUnsupported }
func (noStore) Uninstall(context.Context, string, string) error     { return ErrUnsupported }
func (noStore) Status(context.Context, string, string) TrustStatus {
	return TrustStatus{Supported: false, Detail: "use `pano run --` or set SSL_CERT_FILE"}
}

func (noStore) ManualInstructions(certPath string) string {
	return fmt.Sprintf(`Debian/Ubuntu: sudo cp %s /usr/local/share/ca-certificates/pano.crt && sudo update-ca-certificates
Fedora/Arch:   sudo trust anchor %s
Per-process:   pano run -- <cmd>   (sets SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, ...)`, certPath, certPath)
}
