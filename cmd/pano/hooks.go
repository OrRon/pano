package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/orron/pano/internal/cli"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/config"
	"github.com/orron/pano/internal/daemon"
	"github.com/orron/pano/internal/mcpserver"
	"github.com/orron/pano/internal/watchdog"
)

// hooks wires the daemon, MCP server and watchdog entrypoints. They live in
// separate packages to keep the CLI free of heavy imports and import cycles.
func hooks() cli.Hooks {
	return cli.Hooks{
		Daemon: func(ctx context.Context, paths config.Paths, cfg config.Config, ov cli.DaemonOverrides) error {
			return daemon.Run(ctx, daemon.Options{
				Paths: paths, Config: cfg, Version: cli.Version(),
				Port: ov.Port, MCPPort: ov.MCPPort, Bind: ov.Bind,
			})
		},
		MCP: func(ctx context.Context, c *client.Client, _ config.Paths) error {
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
			return mcpserver.New(c, cli.Version(), logger).ServeStdio(ctx)
		},
		Watchdog: func(ctx context.Context, pid int, statePath string) error {
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			return watchdog.Run(ctx, pid, statePath, logger)
		},
	}
}
