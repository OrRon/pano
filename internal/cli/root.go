package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/config"
	"github.com/orron/pano/internal/update"
)

// Build info, set via -ldflags.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// Hooks are entrypoints implemented outside this package (to avoid import
// cycles): the daemon, the MCP server and the watchdog.
type Hooks struct {
	// Daemon runs the proxy daemon in the foreground until ctx is done.
	Daemon func(ctx context.Context, paths config.Paths, cfg config.Config, overrides DaemonOverrides) error
	// MCP runs the stdio MCP server.
	MCP func(ctx context.Context, c *client.Client, paths config.Paths) error
	// Watchdog waits for pid to exit and restores system proxy settings.
	Watchdog func(ctx context.Context, pid int, statePath string) error
}

// DaemonOverrides carry CLI flags into the daemon.
type DaemonOverrides struct {
	Port    int
	MCPPort int
	Bind    string
}

// App holds shared state for commands.
type App struct {
	hooks   Hooks
	paths   config.Paths
	sock    string
	jsonOut bool
	noColor bool
	quiet   bool
	verbose bool
	out     io.Writer
	errOut  io.Writer

	child *os.Process // daemon spawned by this process, if any (see waitStopped)

	upd *update.Checker // background release check, nil when it must not run

	frameWidth int // mascot row clamp override (tests); 0 = detect from stdout
	color      bool
}

// Version returns the version string.
func Version() string {
	v := version
	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
	}
	return v
}

// New builds the root command.
func New(h Hooks) *cobra.Command {
	app := &App{hooks: h, out: os.Stdout, errOut: os.Stderr}
	root := &cobra.Command{
		Use:   "pano",
		Short: "All-seeing HTTPS proxy for AI agents",
		Long: `pano decrypts and records this machine's HTTP(S) traffic and exposes it to
humans (this CLI) and AI agents (MCP) with token-efficient views, replay,
live rules and breakpoints.

Quick start:
  pano ca install      trust the local CA (one-time)
  pano on              route macOS traffic through pano
  pano tail            watch decrypted flows live
  pano off             restore previous proxy settings`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			p, err := config.DefaultPaths()
			if err != nil {
				return err
			}
			app.paths = p
			if app.sock == "" {
				app.sock = os.Getenv("PANO_SOCK")
			}
			if app.sock == "" {
				app.sock = p.Socket()
			}
			app.color = !app.noColor && os.Getenv("NO_COLOR") == "" && isTTY(os.Stdout) && !app.jsonOut
			app.startUpdateCheck(cmd)
			return nil
		},
		PersistentPostRun: func(*cobra.Command, []string) { app.printUpdateNotice() },
	}
	pf := root.PersistentFlags()
	pf.BoolVar(&app.jsonOut, "json", false, "output JSON")
	pf.StringVar(&app.sock, "sock", "", "control socket path (default ~/.pano/pano.sock, $PANO_SOCK)")
	pf.BoolVar(&app.noColor, "no-color", false, "disable colors (also honours NO_COLOR)")
	pf.BoolVarP(&app.quiet, "quiet", "q", false, "less output")
	pf.BoolVarP(&app.verbose, "verbose", "v", false, "more output")

	root.AddCommand(
		app.cmdVersion(), app.cmdStart(), app.cmdStop(), app.cmdStatus(), app.cmdOn(), app.cmdOff(),
		app.cmdCA(), app.cmdTail(), app.cmdFlows(), app.cmdShow(), app.cmdExplain(), app.cmdDiff(),
		app.cmdReplay(), app.cmdRules(), app.cmdBP(), app.cmdSession(), app.cmdDecrypt(),
		app.cmdExport(), app.cmdImport(), app.cmdRun(), app.cmdEnv(), app.cmdDoctor(), app.cmdConfig(),
		app.cmdMCP(), app.cmdUI(), app.cmdMobile(), app.cmdDaemon(), app.cmdWatchdog(),
	)
	root.CompletionOptions.HiddenDefaultCmd = false
	return root
}

// Execute runs the CLI.
func Execute(h Hooks) {
	root := New(h)
	if err := root.Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		if errors.Is(err, client.ErrNotRunning) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

func (a *App) client() *client.Client { return client.New(a.sock) }

func (a *App) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}

func (a *App) println(args ...any) {
	fmt.Fprintln(a.out, args...)
}

func (a *App) warn(format string, args ...any) {
	fmt.Fprintf(a.errOut, a.c(yellow, "warning: ")+format+"\n", args...)
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ANSI colors.
const (
	reset   = "\033[0m"
	bold    = "\033[1m"
	dim     = "\033[2m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
)

func (a *App) c(color, s string) string {
	if !a.color {
		return s
	}
	return color + s + reset
}

func (a *App) statusColor(status int, err string) string {
	switch {
	case err != "":
		return red
	case status >= 500:
		return red
	case status >= 400:
		return yellow
	case status >= 300:
		return cyan
	case status >= 200:
		return green
	}
	return dim
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return s[:n-1] + "…"
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
