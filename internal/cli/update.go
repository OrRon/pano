package cli

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/update"
)

// noUpdateCheck lists commands where the release check must not run: the
// ones whose stdout is a protocol (mcp), that are not run by a person
// (daemon, _watchdog, shell completion) or whose output is consumed by a
// shell (env).
var noUpdateCheck = map[string]bool{
	"mcp": true, "daemon": true, "_watchdog": true, "env": true,
	"completion": true, cobra.ShellCompRequestCmd: true, cobra.ShellCompNoDescRequestCmd: true, "help": true,
}

// startUpdateCheck kicks off the once-a-day release check in the background
// when this invocation is one a person will read: a terminal on both stdout
// and stderr, no --json, not one of the excluded commands, and nothing in the
// build, environment or config says no (update.Disabled).
func (a *App) startUpdateCheck(cmd *cobra.Command) {
	if a.jsonOut || a.quiet || noUpdateCheck[cmd.Name()] || !isTTY(os.Stdout) || !isTTY(os.Stderr) {
		return
	}
	if update.Disabled(Version(), nil, a.paths) != "" {
		return
	}
	a.upd = update.Start(context.Background(), a.updateOptions(false))
}

func (a *App) updateOptions(force bool) update.Options {
	return update.Options{Current: Version(), StatePath: a.paths.UpdateState(), Force: force}
}

// printUpdateNotice writes the one-line hint after a command's own output.
// It waits briefly for a check that is still in flight — the network call
// has its own 3 s timeout — so an unlucky first run of the day cannot hang.
func (a *App) printUpdateNotice() {
	info := a.upd.Wait(1500 * time.Millisecond)
	if info == nil || !info.Available {
		return
	}
	a.printf("\n%s pano %s is available (you have %s) · %s\n  %s\n",
		a.c(yellow, "↑"), a.c(bold, info.Latest), info.Current, a.c(bold, info.Hint),
		a.c(dim, info.URL+"  ·  disable: "+update.EnvDisable+"=1"))
}
