//go:build linux

package watchdog

import (
	"bytes"
	"os"
	"strconv"
)

// exited reports whether pid has terminated but not yet been reaped. Such a
// zombie still answers kill(pid, 0), so the null-signal probe alone would
// wait for it forever. The state is the field after the parenthesised
// command name in /proc/<pid>/stat; the name may itself contain parentheses,
// so parsing starts after the last ')'. Any read or parse failure reports
// false and leaves the decision to the signal probe.
func exited(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return false
	}
	state := b[i+2]
	return state == 'Z' || state == 'X' || state == 'x'
}
