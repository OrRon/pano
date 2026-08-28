//go:build !darwin

package watchdog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// pollInterval is how often the process is probed.
const pollInterval = 500 * time.Millisecond

// WaitExit blocks until process pid exits or ctx is done. Without kqueue it
// probes the process with a null signal every 500ms; a pid that no longer
// exists returns immediately with nil.
func WaitExit(ctx context.Context, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if !alive(pid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// alive reports whether pid still exists and has not terminated. A process
// that has exited but not been reaped (a zombie) counts as gone, matching
// the kqueue NOTE_EXIT semantics on darwin. A permission error means the
// process exists but belongs to someone else, which counts as alive.
func alive(pid int) bool {
	if exited(pid) {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return !errors.Is(err, os.ErrProcessDone)
}
