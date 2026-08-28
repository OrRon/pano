//go:build !darwin && !linux

package watchdog

// exited reports whether pid has terminated but not yet been reaped. Without
// a portable way to tell, it reports false and the signal probe decides.
func exited(int) bool { return false }
