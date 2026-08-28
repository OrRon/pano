package cli

import (
	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/tui"
)

func (a *App) cmdUI() *cobra.Command {
	return &cobra.Command{
		Use:     "ui",
		Aliases: []string{"tui", "watch"},
		Short:   "Interactive terminal UI: live flows, details, explain, diff, rules",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c := a.client()
			if !c.Ping(ctx) {
				if err := a.spawnDaemon(ctx, DaemonOverrides{}); err != nil {
					return err
				}
			}
			return tui.Run(ctx, c, Version())
		},
	}
}
