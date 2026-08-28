//go:build !darwin

package sysproxy

import (
	"context"
	"log/slog"

	"github.com/orron/pano/internal/api"
)

// New returns a Manager for this platform. Changing the system proxy is only
// implemented on macOS; elsewhere the returned Manager reports
// Supported() == false and Enable returns ErrUnsupported.
func New(statePath string, logger *slog.Logger) Manager {
	return unsupported{}
}

type unsupported struct{}

func (unsupported) Supported() bool { return false }

func (unsupported) Enable(context.Context, string, int, []string) error { return ErrUnsupported }

func (unsupported) Disable(context.Context) error { return nil }

func (unsupported) Status(context.Context, string, int) (api.SysProxy, error) {
	return api.SysProxy{Supported: false, Detail: "system proxy control is only available on macOS"}, nil
}

func (unsupported) RestoreStale(context.Context) (bool, error) { return false, nil }
