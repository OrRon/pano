package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/ca"
	"github.com/orron/pano/internal/config"
)

func (a *App) cmdSession() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Group flows into named sessions"}
	ls := &cobra.Command{
		Use: "ls", Short: "List sessions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ss, err := a.client().Sessions(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(ss)
			}
			for _, s := range ss {
				mark := " "
				if s.Current {
					mark = a.c(green, "●")
				}
				a.printf("%s %s %s  %d flows  started %s\n", mark, pad(s.ID, 8), pad(s.Name, 24), s.Flows, s.StartedAt.Local().Format("Jan 2 15:04"))
			}
			return nil
		},
	}
	newc := &cobra.Command{
		Use: "new <name>", Short: "Start a new session (becomes current)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.client().StartSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			a.printf("%s session %s (%s) started\n", a.c(green, "✓"), s.Name, s.ID)
			return nil
		},
	}
	clear := &cobra.Command{
		Use: "clear", Short: "Forget all flows in memory (keeps persisted history)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := a.client().Capture(cmd.Context(), api.CaptureRequest{Action: "clear"}); err != nil {
				return err
			}
			a.printf("%s cleared\n", a.c(green, "✓"))
			return nil
		},
	}
	rm := &cobra.Command{
		Use: "rm <id>", Short: "Delete a session and its flows", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.client().DeleteSession(cmd.Context(), args[0]); err != nil {
				return err
			}
			a.printf("%s deleted %s\n", a.c(green, "✓"), args[0])
			return nil
		},
	}
	cmd.AddCommand(ls, newc, clear, rm)
	return cmd
}

func (a *App) cmdExport() *cobra.Command {
	var f api.FlowFilter
	var out string
	var reveal bool
	cmd := &cobra.Command{Use: "export", Short: "Export flows"}
	har := &cobra.Command{
		Use: "har", Short: "Write flows to a HAR 1.2 file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return errors.New("--out FILE required")
			}
			r, err := a.client().HAR(cmd.Context(), api.HARRequest{Action: "export", Path: absPath(out), Filter: f, RevealSecrets: reveal})
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(r)
			}
			a.printf("%s wrote %d flows (%d bytes) to %s\n", a.c(green, "✓"), r.Count, r.Bytes, r.Path)
			return nil
		},
	}
	addFilterFlags(har, &f)
	har.Flags().StringVarP(&out, "out", "o", "", "output file")
	har.Flags().BoolVar(&reveal, "reveal", false, "include secrets unredacted (audited)")
	cmd.AddCommand(har)
	return cmd
}

func (a *App) cmdImport() *cobra.Command {
	var in string
	cmd := &cobra.Command{Use: "import", Short: "Import flows"}
	har := &cobra.Command{
		Use: "har", Short: "Import a HAR file into the current session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if in == "" {
				return errors.New("--in FILE required")
			}
			r, err := a.client().HAR(cmd.Context(), api.HARRequest{Action: "import", Path: absPath(in)})
			if err != nil {
				return err
			}
			a.printf("%s imported %d flows from %s\n", a.c(green, "✓"), r.Count, r.Path)
			return nil
		},
	}
	har.Flags().StringVarP(&in, "in", "i", "", "input file")
	cmd.AddCommand(har)
	return cmd
}

func absPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	wd, _ := os.Getwd()
	return wd + "/" + p
}

// proxyEnv returns the environment a child needs to use pano.
func (a *App) proxyEnv(ctx context.Context) ([]string, error) {
	st, err := a.client().Status(ctx)
	if err != nil {
		return nil, err
	}
	proxy := "http://" + st.ProxyAddr
	caPath := a.paths.CACert()
	noProxy := "localhost,127.0.0.1,::1,*.local"
	return []string{
		"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy, "ALL_PROXY=" + proxy,
		"http_proxy=" + proxy, "https_proxy=" + proxy, "all_proxy=" + proxy,
		"NO_PROXY=" + noProxy, "no_proxy=" + noProxy,
		"SSL_CERT_FILE=" + caPath, "NODE_EXTRA_CA_CERTS=" + caPath, "REQUESTS_CA_BUNDLE=" + caPath,
		"CURL_CA_BUNDLE=" + caPath, "GIT_SSL_CAINFO=" + caPath, "AWS_CA_BUNDLE=" + caPath,
		"DENO_CERT=" + caPath, "CARGO_HTTP_CAINFO=" + caPath,
	}, nil
}

func (a *App) cmdRun() *cobra.Command {
	return &cobra.Command{
		Use:   "run -- <command> [args...]",
		Short: "Run a command with proxy + CA environment set (no system changes)",
		Long: `Runs a command with HTTP_PROXY/HTTPS_PROXY pointing at pano and the CA
bundle variables (SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, …)
pointing at pano's root certificate. Nothing system-wide changes, and no
keychain trust is needed. Starts the daemon if it is not running.`,
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) == 0 {
				return errors.New("command required")
			}
			ctx := cmd.Context()
			if !a.client().Ping(ctx) {
				if err := a.spawnDaemon(ctx, DaemonOverrides{}); err != nil {
					return err
				}
			}
			env, err := a.proxyEnv(ctx)
			if err != nil {
				return err
			}
			bin, err := exec.LookPath(args[0])
			if err != nil {
				return err
			}
			return syscall.Exec(bin, args, append(os.Environ(), env...)) //nolint:gosec // intentional exec of user command
		},
	}
}

func (a *App) cmdEnv() *cobra.Command {
	return &cobra.Command{
		Use:   "env",
		Short: "Print shell exports to route a shell through pano (eval \"$(pano env)\")",
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := a.proxyEnv(cmd.Context())
			if err != nil {
				return err
			}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				a.printf("export %s=%q\n", k, v)
			}
			return nil
		},
	}
}

func (a *App) cmdDoctor() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check daemon, port, CA trust, and system proxy sanity",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ok := func(b bool) string {
				if b {
					return a.c(green, "ok  ")
				}
				return a.c(red, "FAIL")
			}
			problems := 0
			c := a.client()
			running := c.Ping(ctx)
			a.printf("%s daemon running (%s)\n", ok(running), a.sock)
			cfg, warnings, err := config.LoadWithWarnings(a.paths)
			a.printf("%s config %s\n", ok(err == nil), a.paths.ConfigFile())
			if err != nil {
				a.printf("     %v\n", err)
				problems++
			}
			for _, w := range warnings {
				a.printf("%s %s\n", a.c(yellow, "warn"), w)
			}
			auth, err := a.loadCA()
			a.printf("%s CA files %s\n", ok(err == nil), a.paths.CACert())
			if err != nil {
				a.printf("     %v\n", err)
				problems++
			} else {
				st := ca.NewTrustStore().Status(ctx, a.paths.CACert(), auth.Subject())
				a.printf("%s CA trusted: %s\n", ok(st.Installed || !st.Supported), st.Detail)
				if !st.Installed && st.Supported {
					a.printf("     run %s\n", a.c(bold, "pano ca install"))
					problems++
				}
				if w := auth.ExpiryWarning(); w != "" {
					a.printf("%s %s\n", a.c(yellow, "warn"), w)
				} else {
					a.printf("%s CA expires %s\n", ok(true), auth.NotAfter().Format("2006-01-02"))
				}
			}
			if running {
				st, err := c.Status(ctx)
				if err == nil {
					a.printf("%s proxy listening on %s\n", ok(true), st.ProxyAddr)
					if st.SystemProxy.Enabled {
						a.printf("%s system proxy → pano (%s)\n", ok(true), st.SystemProxy.Detail)
					} else {
						a.printf("%s system proxy off (use `pano on` or `pano run -- cmd`)\n", a.c(dim, "info"))
					}
					if st.Dropped > 0 {
						a.printf("%s %d events dropped by the store\n", a.c(yellow, "warn"), st.Dropped)
					}
					a.printf("%s %s\n", ok(true), a.renderDecrypt(st.Decrypt, "     ", time.Now())[5:])
					if n := len(st.Decrypt.Rejected); n > 0 {
						a.printf("%s %d host(s) refused pano's certificate in the last hour (pinning?) — see above\n", a.c(yellow, "warn"), n)
					}
				}
			} else {
				addr := net.JoinHostPort(cfg.Proxy.Bind, strconv.Itoa(cfg.Proxy.Port))
				ln, err := net.Listen("tcp", addr)
				if err != nil {
					a.printf("%s port %s is in use by another process\n", ok(false), addr)
					problems++
				} else {
					_ = ln.Close()
					a.printf("%s port %s free\n", ok(true), addr)
				}
				if _, err := os.Stat(a.paths.SysProxyState()); err == nil {
					a.printf("%s stale system proxy state found (daemon died with proxy on) — run %s\n", ok(false), a.c(bold, "pano off"))
					problems++
				}
			}
			if problems > 0 {
				return &exitError{code: 2}
			}
			return nil
		},
	}
}

func (a *App) cmdConfig() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Show or edit ~/.pano/config.toml"}
	get := &cobra.Command{
		Use: "get", Short: "Print the effective configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.client().Ping(cmd.Context()) {
				raw, err := a.client().Config(cmd.Context())
				if err == nil {
					if a.jsonOut {
						a.println(string(raw))
						return nil
					}
					var v any
					_ = json.Unmarshal(raw, &v)
					return a.printJSON(v)
				}
			}
			cfg, warnings, err := config.LoadWithWarnings(a.paths)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				a.warn("%s", w)
			}
			return a.printJSON(cfg)
		},
	}
	path := &cobra.Command{
		Use: "path", Short: "Print the config file path",
		RunE: func(_ *cobra.Command, _ []string) error { a.println(a.paths.ConfigFile()); return nil },
	}
	initc := &cobra.Command{
		Use: "init", Short: "Write a config file with defaults",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.paths.Ensure(); err != nil {
				return err
			}
			if _, err := os.Stat(a.paths.ConfigFile()); err == nil {
				return fmt.Errorf("%s already exists", a.paths.ConfigFile())
			}
			if err := config.Save(a.paths, config.Default()); err != nil {
				return err
			}
			a.printf("%s wrote %s\n", a.c(green, "✓"), a.paths.ConfigFile())
			return nil
		},
	}
	edit := &cobra.Command{
		Use: "edit", Short: "Open the config file in $EDITOR",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := os.Stat(a.paths.ConfigFile()); err != nil {
				if err := a.paths.Ensure(); err != nil {
					return err
				}
				if err := config.Save(a.paths, config.Default()); err != nil {
					return err
				}
			}
			ed := os.Getenv("EDITOR")
			if ed == "" {
				ed = "vi"
			}
			c := exec.CommandContext(cmd.Context(), ed, a.paths.ConfigFile())
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
	}
	cmd.AddCommand(get, path, initc, edit)
	return cmd
}

func (a *App) cmdMCP() *cobra.Command {
	var httpOnly bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server on stdio (for Claude Code and other agents)",
		Long: `Serves the Model Context Protocol over stdio. Register with:

  claude mcp add --scope user --transport stdio pano -- pano mcp

The server never starts the daemon: tools answer "pano is off" until you run
'pano on' (or 'pano start') in a terminal, and work again the moment it is
up. Nothing is written to stdout except protocol messages; logs go to stderr.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if httpOnly {
				st, err := a.client().Status(ctx)
				if err != nil {
					return err
				}
				a.printf("http://%s/mcp\n", st.MCPAddr)
				return nil
			}
			if a.hooks.MCP == nil {
				return errors.New("mcp server not available in this build")
			}
			a.out = os.Stderr // never print to stdout in MCP mode
			return a.hooks.MCP(ctx, a.client(), a.paths)
		},
	}
	cmd.Flags().BoolVar(&httpOnly, "http", false, "print the Streamable HTTP MCP URL instead of serving stdio")
	var scope string
	install := &cobra.Command{
		Use:   "install",
		Short: "Register pano with Claude Code (runs `claude mcp add`)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			self, _ := os.Executable()
			if p, err := exec.LookPath("pano"); err == nil {
				self = p
			}
			args := []string{"mcp", "add", "--scope", scope, "--transport", "stdio", "pano", "--", self, "mcp"}
			if _, lookErr := exec.LookPath("claude"); lookErr != nil {
				a.printf("claude CLI not found. Add manually:\n\n  claude %s\n\nor in .mcp.json:\n%s\n", strings.Join(args, " "), mcpJSON(self))
				return nil //nolint:nilerr // absence of the claude CLI is not an error; we printed instructions
			}
			c := exec.CommandContext(cmd.Context(), "claude", args...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return err
			}
			a.printf("%s registered. In Claude Code, try: \"use pano to show my last 10 requests\"\n", a.c(green, "✓"))
			return nil
		},
	}
	install.Flags().StringVar(&scope, "scope", "user", "user|project|local")
	cmd.AddCommand(install)
	return cmd
}

func mcpJSON(self string) string {
	b, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"pano": map[string]any{"type": "stdio", "command": self, "args": []string{"mcp"}},
		},
	}, "", "  ")
	return string(b)
}
