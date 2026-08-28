//go:build race

package rules

// raceEnabled reports whether the race detector is on. Under the race
// detector sync.Pool deliberately drops a quarter of Puts, so anything that
// pools scratch space (regexp matching) allocates on some calls; the
// zero-allocation assertion excludes regex rules in that build only.
const raceEnabled = true
