//go:build unix

package watchdog

import "syscall"

// detachAttr puts the watchdog in a new session so that signals aimed at the
// daemon's process group (or its controlling terminal) do not reach it.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
