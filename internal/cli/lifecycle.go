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
)

func (a *App) cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		RunE: func(_ *cobra.Command, _ []string) error {
			if a.jsonOut {
				return a.printJSON(map[string]string{"version": Version(), "commit": commit, "date": date})
			}
			a.printf("pano %s (%s, %s)\n", Version(), commit, date)
			return nil
		},
	}
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
	_ = c.Process.Release()
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
	pid := 0
	if b, err := os.ReadFile(a.paths.PIDFile()); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if err := c.Shutdown(ctx); err != nil && !errors.Is(err, client.ErrNotRunning) {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pid > 0 {
			if p, err := os.FindProcess(pid); err != nil || p.Signal(syscall.Signal(0)) != nil {
				break
			}
		} else if !c.Ping(ctx) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !a.quiet {
		a.printf("%s pano stopped\n", a.c(green, "✓"))
	}
	return nil
}

func (a *App) cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon, capture, CA and system proxy state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.client().Status(cmd.Context())
			if err != nil {
				if errors.Is(err, client.ErrNotRunning) && !a.jsonOut {
					a.printf("%s pano is not running (start with %s)\n", a.c(red, "●"), a.c(bold, "pano start"))
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
	a.printf("  capture      %s  session %q  %d flows in memory, %d total\n", cap, st.Session, st.Flows, st.FlowsTotal)
	a.printf("  rules        %d (%d enabled)  held %d  active conns %d\n", st.Rules, st.RulesEnabled, st.Held, st.ActiveConns)
	if st.Dropped > 0 {
		a.printf("  %s %d events dropped (store fell behind)\n", a.c(yellow, "!"), st.Dropped)
	}
	if !st.Redaction {
		a.printf("  %s secret redaction is OFF\n", a.c(yellow, "!"))
	}
	a.printf("%s\n", a.renderDecrypt(st.Decrypt, "  ", time.Now()))
	a.printf("%s\n", a.renderMobile(st.Mobile, "  ", time.Now()))
}

func (a *App) cmdOn() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "on",
		Short: "Route macOS system HTTP/HTTPS traffic through pano",
		Long: `Sets the system-wide HTTP and HTTPS proxy of every enabled network service
to pano. The previous settings are snapshotted and restored by 'pano off',
'pano stop', or automatically if the daemon dies.

The first time, pano offers to install its CA into your login keychain so
browsers accept the certificates it mints (macOS shows one password prompt).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
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
			a.mascotWake([3]string{
				"",
				a.c(green, "✓") + " system proxy ON → " + st.ProxyAddr,
				"  watch with " + a.c(bold, "pano ui") + " · " + a.c(bold, "pano tail") + "   turn off with " + a.c(bold, "pano off"),
			})
			if sp.Detail != "" {
				// After the animation: may be long (one entry per network
				// service) and is free to wrap here.
				a.printf("             %s\n", a.c(dim, sp.Detail))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not prompt (installs CA if needed)")
	return cmd
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
