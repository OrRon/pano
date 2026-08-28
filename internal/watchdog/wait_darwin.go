//go:build darwin

package watchdog

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// pollInterval is how often the kevent wait wakes up to check ctx.
const pollInterval = 100 * time.Millisecond

// WaitExit blocks until process pid exits or ctx is done. It registers a
// kqueue EVFILT_PROC filter with NOTE_EXIT, so it works for any process the
// caller may observe, not only children. A pid that no longer exists returns
// immediately with nil.
func WaitExit(ctx context.Context, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	kq, err := syscall.Kqueue()
	if err != nil {
		return fmt.Errorf("kqueue: %w", err)
	}
	defer func() { _ = syscall.Close(kq) }()

	change := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_EXIT,
	}
	if _, err := syscall.Kevent(kq, []syscall.Kevent_t{change}, nil, nil); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone
		}
		return fmt.Errorf("kevent add: %w", err)
	}

	var events [1]syscall.Kevent_t
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		timeout := syscall.NsecToTimespec(int64(pollInterval))
		n, err := syscall.Kevent(kq, nil, events[:], &timeout)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return fmt.Errorf("kevent wait: %w", err)
		}
		if n == 0 {
			continue
		}
		ev := events[0]
		if ev.Flags&syscall.EV_ERROR != 0 {
			errno := syscall.Errno(ev.Data) //nolint:gosec // kevent stores the errno in Data
			if errno == syscall.ESRCH {
				return nil
			}
			return fmt.Errorf("kevent: %w", errno)
		}
		if ev.Fflags&syscall.NOTE_EXIT != 0 {
			return nil
		}
	}
}
