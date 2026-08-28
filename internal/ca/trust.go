package ca

import (
	"context"
	"errors"
)

// ErrUnsupported is returned when the OS trust store is not automated.
var ErrUnsupported = errors.New("ca: automatic trust install is not supported on this OS")

// TrustStatus describes whether the root is trusted by the OS.
type TrustStatus struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Detail    string `json:"detail,omitempty"`
}

// TrustStore installs the root into the operating system trust store.
type TrustStore interface {
	Install(ctx context.Context, certPath, subject string, system bool) error
	Uninstall(ctx context.Context, certPath, subject string) error
	Status(ctx context.Context, certPath, subject string) TrustStatus
	// ManualInstructions returns human steps for platforms without automation.
	ManualInstructions(certPath string) string
}
