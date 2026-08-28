//go:build windows

package watchdog

import "syscall"

// detachAttr starts the watchdog in its own process group so console events
// aimed at the daemon do not reach it.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
