package sysproxy

import (
	"context"
	"errors"
	"strings"

	"github.com/orron/pano/internal/api"
)

// Manager toggles the OS-level HTTP/HTTPS proxy. Implementations are safe for
// concurrent use; Enable, Disable and RestoreStale are serialised.
type Manager interface {
	// Supported reports whether this build and host can change the system
	// proxy at all.
	Supported() bool

	// Enable snapshots the current settings of every enabled network service
	// to the state file BEFORE changing anything, then points the HTTP and
	// HTTPS proxies of each service at host:port and sets the bypass domain
	// list. The bypass list written is the union of the service's existing
	// list, DefaultBypass and bypass.
	//
	// If a snapshot already exists (pano is already enabled, or a previous
	// daemon crashed without restoring) it is kept rather than overwritten,
	// so a later Disable still restores the settings that predate pano.
	Enable(ctx context.Context, host string, port int, bypass []string) error

	// Disable restores the snapshot — the exact previous host, port and
	// enabled state of every service — and deletes the state file. It is
	// idempotent: with no state file there is nothing to do. Per-service
	// failures do not stop the restore of the remaining services; they are
	// joined into the returned error and the state file is kept so the
	// restore can be retried.
	Disable(ctx context.Context) error

	// Status reports the current state: whether a snapshot exists (pano set
	// the proxy), which services currently point at host:port, and a short
	// human-readable detail line.
	Status(ctx context.Context, host string, port int) (api.SysProxy, error)

	// RestoreStale restores the previous settings if a state file exists. It
	// is used at daemon start and by `pano doctor` to clean up after a crash.
	// It reports whether anything was restored.
	RestoreStale(ctx context.Context) (bool, error)
}

// ErrUnsupported is returned by Enable on platforms where pano cannot change
// the system proxy.
var ErrUnsupported = errors.New("sysproxy: changing the system proxy is not supported on this OS; " +
	"use `pano run -- <cmd>` or set HTTP_PROXY/HTTPS_PROXY for the processes you want to capture")

// DefaultBypass lists the destinations that must never be routed through the
// proxy. They are always merged into the bypass list written by Enable.
var DefaultBypass = []string{"localhost", "127.0.0.1", "*.local", "169.254/16"}

// mergeBypass returns existing ∪ DefaultBypass ∪ extra, preserving first
// occurrence order and dropping blanks and duplicates.
func mergeBypass(existing, extra []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range [][]string{existing, DefaultBypass, extra} {
		for _, d := range list {
			d = strings.TrimSpace(d)
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
