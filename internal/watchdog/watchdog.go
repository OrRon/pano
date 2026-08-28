package watchdog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/orron/pano/internal/sysproxy"
)

// restoreTimeout bounds the restore after the daemon has exited. It is
// generous because the admin-privileges path may show a password prompt.
const restoreTimeout = 2 * time.Minute

// newManager builds the sysproxy.Manager used by Run; tests replace it.
var newManager = sysproxy.New

// Run blocks until process pid exits, then restores the system proxy settings
// via sysproxy if the state file at statePath still exists. It returns after
// the restore, or early with ctx.Err() if ctx is cancelled while waiting.
// A nil logger discards log output.
func Run(ctx context.Context, pid int, statePath string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger = logger.With("component", "watchdog", "pid", pid)

	logger.Info("watching daemon")
	if err := WaitExit(ctx, pid); err != nil {
		return fmt.Errorf("watchdog: wait for pid %d: %w", pid, err)
	}
	logger.Info("daemon exited; checking system proxy state", "state", statePath)

	// The daemon is gone; restore even if the parent context is finished.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()
	restored, err := newManager(statePath, logger).RestoreStale(rctx)
	if err != nil {
		return fmt.Errorf("watchdog: %w", err)
	}
	if restored {
		logger.Info("system proxy settings restored")
	} else {
		logger.Info("no stale state; nothing to restore")
	}
	return nil
}

// Spawn starts a detached watchdog process by executing self (normally the
// running pano binary, see os.Executable) with the arguments
//
//	_watchdog --pid <pid> --state <statePath>
//
// in its own session, with stdin from /dev/null and stdout/stderr appended to
// logPath, or discarded when logPath is empty. It returns the child's pid.
// The child is not waited for; it outlives the caller by design.
func Spawn(self string, pid int, statePath, logPath string) (int, error) {
	if self == "" {
		return 0, errors.New("watchdog: empty executable path")
	}
	if pid <= 0 {
		return 0, fmt.Errorf("watchdog: invalid pid %d", pid)
	}
	if statePath == "" {
		return 0, errors.New("watchdog: empty state path")
	}

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("watchdog: open %s: %w", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()

	out := devnull
	if logPath != "" {
		out, err = os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return 0, fmt.Errorf("watchdog: open log: %w", err)
		}
		defer func() { _ = out.Close() }()
	}

	// The child must outlive the caller, so it is deliberately not tied to a
	// cancellable context.
	cmd := exec.CommandContext(context.Background(), self, "_watchdog", "--pid", strconv.Itoa(pid), "--state", statePath)
	cmd.Stdin = devnull
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("watchdog: start %s: %w", self, err)
	}
	child := cmd.Process.Pid
	// Do not wait: the watchdog must keep running after this process exits.
	_ = cmd.Process.Release()
	return child, nil
}
