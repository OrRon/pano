package cli

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/tui"
)

func (a *App) cmdUI() *cobra.Command {
	return &cobra.Command{
		Use:     "ui",
		Aliases: []string{"tui", "watch"},
		Short:   "Open the terminal UI (starts pano like `pano on` if it is off)",
		Long: `Opens the interactive UI: live flows, details, explain, diff, rules,
decrypt and mobile drawers.

If pano is already running, this attaches a window to it: quitting the
window leaves pano running (q offers to turn it off too). If pano is off,
'pano ui' does exactly what 'pano on' does — starts the daemon, routes the
Mac's traffic through it and opens the UI as its owner, so closing the
window turns pano off again.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if !a.uiPossible() {
				return errors.New("pano ui needs a terminal; use `pano on -b` and `pano tail` in scripts")
			}
			if !a.client().Ping(ctx) {
				return a.on(ctx, false, false)
			}
			return a.runUI(ctx, tui.Options{Own: false})
		},
	}
}

// uiPossible reports whether the UI can take over this terminal: keys come
// from stdin and the screen is stdout. Piped, redirected, --json or an
// agent's shell all fail this and get the background behaviour instead.
func (a *App) uiPossible() bool {
	return !a.jsonOut && isTTY(os.Stdin) && isTTY(os.Stdout)
}

// runUI runs the UI and prints one line about what its exit meant, so the
// terminal that opened pano is never left guessing whether it is still on.
func (a *App) runUI(ctx context.Context, opts tui.Options) error {
	exit, err := tui.Run(ctx, a.client(), Version(), opts)
	if err != nil {
		return err
	}
	if exit == tui.ExitInterrupt && opts.Own {
		// The daemon saw our attachment drop and is turning itself off.
		exit = tui.ExitOff
	}
	switch exit {
	case tui.ExitOff:
		a.waitStopped(ctx, 15*time.Second)
		a.mascotPrint(eyesShut, [3]string{
			"",
			a.c(green, "✓") + " pano is off — system proxy restored, daemon stopped",
			"  " + a.c(bold, "pano on") + " wakes it again",
		})
	case tui.ExitGone:
		a.printf("%s pano was turned off\n", a.c(dim, "○"))
	case tui.ExitDetach, tui.ExitInterrupt:
		a.printf("%s pano keeps running in the background — %s to watch · %s to stop\n",
			a.c(green, "●"), a.c(bold, "pano ui"), a.c(bold, "pano off"))
	}
	return nil
}
