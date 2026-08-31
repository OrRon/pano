package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/config"
	"github.com/orron/pano/internal/tui"
	"github.com/orron/pano/internal/update"
)

func (a *App) cmdVersion() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version (--check asks GitHub whether a newer release exists)",
		Long: `Prints the running version. With --check, asks GitHub's releases endpoint
for the latest tag right now — ignoring the once-a-day cache and the
opt-outs, since you asked — and prints the upgrade command for the way this
pano was installed. Nothing is downloaded or installed.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := map[string]any{"version": Version(), "commit": commit, "date": date}
			var info *update.Info
			if check {
				// Do not report the background check's result: the user asked.
				a.upd = nil
				var err error
				info, err = update.Check(cmd.Context(), a.updateOptions(true))
				if err != nil {
					return err
				}
				out["latest"], out["update_available"], out["hint"], out["url"] = info.Latest, info.Available, info.Hint, info.URL
			}
			if a.jsonOut {
				return a.printJSON(out)
			}
			a.printf("pano %s (%s, %s)\n", Version(), commit, date)
			switch {
			case info == nil:
			case update.IsDev(Version()):
				a.printf("  latest release %s · %s\n", a.c(bold, info.Latest), a.c(dim, "this is a development build"))
			case info.Available:
				a.printf("  %s %s is available · %s\n  %s\n", a.c(yellow, "↑"), a.c(bold, info.Latest), a.c(bold, info.Hint), a.c(dim, info.URL))
			default:
				a.printf("  %s up to date\n", a.c(green, "✓"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "ask GitHub for the latest release")
	return cmd
}

func (a *App) cmdStart() *cobra.Command {
	var ov DaemonOverrides
	var fg bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the proxy daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.paths.Ensure(); err != nil {
				return err
			}
			if a.client().Ping(cmd.Context()) {
				st, _ := a.client().Status(cmd.Context())
				if !a.quiet {
					a.printf("pano is already running (pid %d, proxy %s)\n", st.PID, st.ProxyAddr)
				}
				return nil
			}
			if fg {
				if a.hooks.Daemon == nil {
					return errors.New("daemon not available in this build")
				}
				cfg, warnings, err := config.LoadWithWarnings(a.paths)
				if err != nil {
					return err
				}
				for _, w := range warnings {
					a.warn("%s", w)
				}
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				return a.hooks.Daemon(ctx, a.paths, cfg, ov)
			}
			return a.spawnDaemon(cmd.Context(), ov)
		},
	}
	cmd.Flags().IntVar(&ov.Port, "port", 0, "proxy port (default from config: 9091)")
	cmd.Flags().IntVar(&ov.MCPPort, "mcp-port", 0, "HTTP MCP port (default from config: 9092)")
	cmd.Flags().StringVar(&ov.Bind, "bind", "", "bind address (default 127.0.0.1)")
	cmd.Flags().BoolVar(&fg, "foreground", false, "run in the foreground (logs to stderr)")
	return cmd
}

func (a *App) spawnDaemon(ctx context.Context, ov DaemonOverrides) error {
	if err := a.paths.Ensure(); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(a.paths.LogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	if fi, _ := logf.Stat(); fi != nil && fi.Size() > 20<<20 {
		_ = os.Rename(a.paths.LogFile(), a.paths.LogFile()+".1")
	}
	args := []string{"daemon"}
	if ov.Port > 0 {
		args = append(args, "--port", strconv.Itoa(ov.Port))
	}
	if ov.MCPPort > 0 {
		args = append(args, "--mcp-port", strconv.Itoa(ov.MCPPort))
	}
	if ov.Bind != "" {
		args = append(args, "--bind", ov.Bind)
	}
	c := exec.CommandContext(context.Background(), self, args...) //nolint:contextcheck // detached child must outlive the CLI
	c.Env = append(os.Environ(), "PANO_HOME="+a.paths.Dir)
	c.Stdout, c.Stderr = logf, logf
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	// Keep the handle: if this process later waits for the daemon to stop
	// (pano on → UI → quit) it must reap it, or the exited daemon lingers
	// as a zombie that still answers kill(pid, 0).
	a.child = c.Process
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if a.client().Ping(ctx) {
			st, _ := a.client().Status(ctx)
			if !a.quiet {
				a.printf("%s pano started (pid %d) — proxy %s\n", a.c(green, "✓"), st.PID, st.ProxyAddr)
				if !st.CA.Trusted && st.CA.Supported {
					a.printf("  CA not trusted yet: run %s\n", a.c(bold, "pano ca install"))
				}
			}
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not come up; see %s", a.paths.LogFile())
}

func (a *App) cmdStop() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon (restores system proxy settings)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.client().Ping(cmd.Context()) {
				if !a.quiet {
					a.println("pano is not running")
				}
				return nil
			}
			return a.stopDaemon(cmd.Context())
		},
	}
}

// stopDaemon asks a running daemon to shut down and waits for it to exit.
func (a *App) stopDaemon(ctx context.Context) error {
	c := a.client()
	if err := c.Shutdown(ctx); err != nil && !errors.Is(err, client.ErrNotRunning) {
		return err
	}
	a.waitStopped(ctx, 10*time.Second)
	if !a.quiet {
		a.printf("%s pano stopped\n", a.c(green, "✓"))
	}
	return nil
}

// waitStopped blocks until the daemon is gone, or the timeout passes.
//
// If this process spawned the daemon it reaps it with Wait: an exited child
// nobody has waited for is a zombie, and a zombie still answers
// kill(pid, 0), so the probe below would spin until the timeout. Otherwise
// the daemon belongs to another process and the probe is right; the daemon
// also removes its pid file as the last step of shutdown.
func (a *App) waitStopped(ctx context.Context, timeout time.Duration) {
	if a.child != nil {
		done := make(chan struct{})
		go func() { _, _ = a.child.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(timeout):
		}
		return
	}
	c := a.client()
	pid := 0
	if b, err := os.ReadFile(a.paths.PIDFile()); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid > 0 {
			if _, err := os.Stat(a.paths.PIDFile()); err != nil && !c.Ping(ctx) {
				return
			}
			if p, err := os.FindProcess(pid); err != nil || p.Signal(syscall.Signal(0)) != nil {
				return
			}
		} else if !c.Ping(ctx) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (a *App) cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon, capture, CA and system proxy state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.client().Status(cmd.Context())
			if err != nil {
				if errors.Is(err, client.ErrNotRunning) && !a.jsonOut {
					a.printf("%s pano is not running (start with %s)\n", a.c(red, "●"), a.c(bold, "pano on"))
					return &exitError{code: 3}
				}
				return err
			}
			if a.jsonOut {
				return a.printJSON(st)
			}
			a.renderStatus(st)
			return nil
		},
	}
}

func (a *App) renderStatus(st api.Status) {
	on := a.c(green, "●")
	off := a.c(dim, "○")
	a.printf("%s pano %s  pid %d  up %s\n", on, st.Version, st.PID, st.Uptime)
	a.printf("  proxy        %s\n", st.ProxyAddr)
	if st.MCPAddr != "" {
		a.printf("  mcp          stdio: pano mcp   http: http://%s/mcp\n", st.MCPAddr)
	}
	sp := off + " off"
	if st.SystemProxy.Enabled {
		sp = on + " ON  " + a.c(dim, st.SystemProxy.Detail)
	} else if st.SystemProxy.Detail != "" && !st.SystemProxy.Supported {
		sp = off + " " + st.SystemProxy.Detail
	}
	a.printf("  system proxy %s\n", sp)
	ca := off + " not trusted — run `pano ca install`"
	if st.CA.Trusted {
		ca = on + " trusted"
	} else if !st.CA.Supported {
		ca = off + " " + st.CA.Detail
	}
	a.printf("  ca           %s  %s\n", ca, a.c(dim, st.CA.Path))
	if st.CA.Warning != "" {
		a.printf("               %s %s\n", a.c(yellow, "warn"), st.CA.Warning)
	}
	cap := on + " capturing"
	if !st.Capturing {
		cap = off + " paused"
	}
	a.printf("  capture      %s  session %q  %d flows in memory (%d seen this run)\n", cap, st.Session, st.Flows, st.FlowsTotal)
	a.printf("  rules        %d (%d enabled)  held %d  active conns %d\n", st.Rules, st.RulesEnabled, st.Held, st.ActiveConns)
	if !st.Redaction {
		a.printf("  %s secret redaction is OFF\n", a.c(yellow, "!"))
	}
	a.printf("  lifecycle    %s\n", a.renderLifecycle(st.Lifecycle))
	a.printf("%s\n", a.renderDecrypt(st.Decrypt, "  ", time.Now()))
	a.printf("%s\n", a.renderMobile(st.Mobile, "  ", time.Now()))
}

func (a *App) cmdOn() *cobra.Command {
	var yes, background bool
	cmd := &cobra.Command{
		Use:   "on",
		Short: "Turn pano on: route the Mac's traffic through it and open the UI",
		Long: `Starts the daemon, sets the system-wide HTTP and HTTPS proxy of every
enabled network service to pano, and opens the terminal UI. Quitting the UI
(q) turns pano off again — the previous proxy settings are restored and the
daemon stops — like closing an app. Closing the terminal window, ctrl-c or a
kill do the same: the daemon notices the UI is gone and turns itself off.
From the UI, b keeps pano running in the background instead.

'pano on -b' (--background) skips the UI: the daemon keeps running until
'pano off'. That is also what happens when there is no terminal (a script,
an agent's shell, --json, piped output).

The first time, pano offers to install its CA into your login keychain so
browsers accept the certificates it mints (macOS shows one password prompt).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.on(cmd.Context(), yes, background)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not prompt (installs CA if needed)")
	cmd.Flags().BoolVarP(&background, "background", "b", false, "do not open the UI; pano runs until `pano off`")
	return cmd
}

// on is `pano on`: daemon up, CA trusted, system proxy on, then either the
// UI as owner (app mode, ADR 0009) or a banner and the prompt back
// (background mode).
func (a *App) on(ctx context.Context, yes, background bool) error {
	c := a.client()
	if !c.Ping(ctx) {
		if err := a.spawnDaemon(ctx, DaemonOverrides{}); err != nil {
			return err
		}
	}
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if st.CA.Supported && !st.CA.Trusted {
		if !yes && !isTTY(os.Stdin) {
			return errors.New("CA is not trusted; run `pano ca install` first (or pass --yes)")
		}
		if yes || a.confirm("pano's CA is not trusted yet. Install it into your login keychain now? (macOS will ask for your password)") {
			if err := a.caInstall(ctx, false); err != nil {
				a.warn("CA install failed: %v — continuing; HTTPS sites will show certificate errors", err)
			}
		}
	}
	sp, err := c.SetSysProxy(ctx, api.SysProxyRequest{Enabled: true, Confirm: "yes"})
	if err != nil {
		return err
	}
	if a.jsonOut {
		return a.printJSON(sp)
	}
	if !background && a.uiPossible() {
		return a.runUI(ctx, tui.Options{Own: true})
	}
	a.mascotWake([3]string{
		"",
		a.c(green, "✓") + " system proxy ON → " + st.ProxyAddr + "   running in the background",
		"  watch with " + a.c(bold, "pano ui") + " · " + a.c(bold, "pano tail") + "   turn off with " + a.c(bold, "pano off"),
	})
	if sp.Detail != "" {
		// After the animation: may be long (one entry per network
		// service) and is free to wrap here.
		a.printf("             %s\n", a.c(dim, sp.Detail))
	}
	a.suggestSimulators(ctx)
	return nil
}

// renderLifecycle is the one-line status of who stops the daemon.
func (a *App) renderLifecycle(l api.Lifecycle) string {
	uis := ""
	switch l.UIs {
	case 0:
	case 1:
		uis = a.c(dim, "  1 ui attached")
	default:
		uis = a.c(dim, fmt.Sprintf("  %d uis attached", l.UIs))
	}
	if l.Mode == "app" {
		return a.c(green, "●") + " app — closing its window turns pano off" + uis
	}
	return a.c(dim, "○") + " background — " + a.c(bold, "pano off") + " stops it" + uis
}

func (a *App) cmdOff() *cobra.Command {
	var keep bool
	cmd := &cobra.Command{
		Use:   "off",
		Short: "Restore the previous macOS proxy settings and stop the daemon",
		Long: `Restores the proxy settings snapshotted by 'pano on', then stops the daemon
so nothing keeps running (and MCP tools report "pano is off") until the next
'pano on'. Pass --keep-daemon to only restore the proxy settings, e.g. when
you still want 'pano run --' or the MCP server to work.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c := a.client()
			if !c.Ping(ctx) {
				// Daemon down: try the stale-state path via a foreground restore.
				return a.restoreStale(ctx)
			}
			sp, err := c.SetSysProxy(ctx, api.SysProxyRequest{Enabled: false, Confirm: "yes"})
			if err != nil {
				return err
			}
			if a.jsonOut {
				if err := a.printJSON(sp); err != nil {
					return err
				}
			} else {
				hint := "  daemon kept running"
				if !keep {
					hint = "  stopping the daemon · " + a.c(bold, "pano on") + " wakes it again"
				}
				a.mascotPrint(eyesShut, [3]string{"", a.c(green, "✓") + " system proxy off (previous settings restored)", hint})
			}
			if keep {
				return nil
			}
			return a.stopDaemon(ctx)
		},
	}
	cmd.Flags().BoolVar(&keep, "keep-daemon", false, "restore the proxy settings but leave the daemon running")
	return cmd
}

func (a *App) restoreStale(ctx context.Context) error {
	if _, err := os.Stat(a.paths.SysProxyState()); err != nil {
		if !a.quiet {
			a.println("pano is not running and no system proxy state to restore")
		}
		return nil
	}
	if a.hooks.Watchdog == nil {
		return errors.New("stale system proxy state found but restore is unavailable in this build")
	}
	// Watchdog with pid 0 restores immediately.
	if err := a.hooks.Watchdog(ctx, 0, a.paths.SysProxyState()); err != nil {
		return err
	}
	a.printf("%s restored system proxy settings left by a dead daemon\n", a.c(green, "✓"))
	return nil
}

func (a *App) confirm(q string) bool {
	fmt.Fprintf(a.errOut, "%s [y/N] ", q)
	var s string
	_, _ = fmt.Fscanln(os.Stdin, &s)
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}

func (a *App) cmdDaemon() *cobra.Command {
	var ov DaemonOverrides
	cmd := &cobra.Command{
		Use:    "daemon",
		Short:  "Run the daemon in the foreground (internal; use `pano start`)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.hooks.Daemon == nil {
				return errors.New("daemon not available in this build")
			}
			if err := a.paths.Ensure(); err != nil {
				return err
			}
			cfg, warnings, err := config.LoadWithWarnings(a.paths)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				a.warn("%s", w)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			return a.hooks.Daemon(ctx, a.paths, cfg, ov)
		},
	}
	cmd.Flags().IntVar(&ov.Port, "port", 0, "")
	cmd.Flags().IntVar(&ov.MCPPort, "mcp-port", 0, "")
	cmd.Flags().StringVar(&ov.Bind, "bind", "", "")
	return cmd
}

func (a *App) cmdWatchdog() *cobra.Command {
	var pid int
	var state string
	cmd := &cobra.Command{
		Use:    "_watchdog",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.hooks.Watchdog == nil {
				return errors.New("watchdog not available")
			}
			return a.hooks.Watchdog(cmd.Context(), pid, state)
		},
	}
	cmd.Flags().IntVar(&pid, "pid", 0, "daemon pid")
	cmd.Flags().StringVar(&state, "state", "", "sysproxy state file")
	return cmd
}
