package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/api"
)

// cmdDecrypt manages the HTTPS decryption policy: mode (all/only/off) and the
// only/never host lists. Every list is printed in full on every call.
func (a *App) cmdDecrypt() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decrypt",
		Short: "Which HTTPS hosts are decrypted: mode all|only|off, only/never lists",
		Long: `Show or change which HTTPS tunnels pano decrypts.

  all   decrypt every host except those on the never list (default)
  only  decrypt just the hosts on the only list
  off   decrypt nothing: tunnels are recorded as host + bytes + timing

The never list wins in every mode (use it for apps that pin certificates).
A bare host covers its subdomains ("whatsapp.net" matches "mmg.whatsapp.net");
globs (*.example.com) work too. Changes apply immediately and are saved to
config.toml. Hosts whose client refused pano's certificate in the last hour
are listed under "rejected" as never-list suggestions; they are never added
automatically.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := a.client().Decrypt(cmd.Context())
			if err != nil {
				return err
			}
			return a.printDecrypt(d)
		},
	}
	for _, m := range []struct{ mode, short string }{
		{"all", "Decrypt every host except the never list"},
		{"off", "Decrypt nothing (tunnel everything)"},
	} {
		mode := m.mode
		cmd.AddCommand(&cobra.Command{
			Use: mode, Short: m.short, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				d, err := a.client().ChangeDecrypt(cmd.Context(), api.DecryptChange{Mode: mode, Source: "cli"})
				if err != nil {
					return err
				}
				if a.jsonOut {
					return a.printJSON(d)
				}
				a.printf("%s mode %s\n", a.c(green, "✓"), a.modeText(d.Mode))
				return a.printDecrypt(d)
			},
		})
	}
	cmd.AddCommand(a.cmdDecryptList("only", "Switch to mode only; `only add|rm` edits the list of hosts decrypted in that mode"),
		a.cmdDecryptList("never", "Hosts never decrypted, in every mode (pinned apps)"))
	return cmd
}

// cmdDecryptList builds `pano decrypt only|never` with add/rm subcommands.
// The bare `only` sets the mode (it is one of the three modes); the bare
// `never` prints the policy.
func (a *App) cmdDecryptList(list, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use: list, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var (
				d   api.Decrypt
				err error
			)
			if list == "only" {
				d, err = a.client().ChangeDecrypt(cmd.Context(), api.DecryptChange{Mode: "only", Source: "cli"})
			} else {
				d, err = a.client().Decrypt(cmd.Context())
			}
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(d)
			}
			if list == "only" {
				a.printf("%s mode %s\n", a.c(green, "✓"), a.modeText(d.Mode))
			}
			return a.printDecrypt(d)
		},
	}
	var rejected bool
	add := &cobra.Command{
		Use: "add <host|glob>...", Short: "Add hosts to the " + list + " list",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rejected {
				args = append(args, api.RejectedAlias)
			}
			if len(args) == 0 {
				return fmt.Errorf("give at least one host or glob%s", map[bool]string{true: " (or --rejected)", false: ""}[list == "never"])
			}
			ch := api.DecryptChange{Source: "cli"}
			if list == "only" {
				ch.AddOnly = args
			} else {
				ch.AddNever = args
			}
			d, err := a.client().ChangeDecrypt(cmd.Context(), ch)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(d)
			}
			a.printf("%s %s + %s\n", a.c(green, "✓"), list, strings.Join(args, " "))
			return a.printDecrypt(d)
		},
	}
	if list == "never" {
		add.Flags().BoolVar(&rejected, "rejected", false, "add every host that rejected pano's certificate in the last hour")
	} else {
		add.Args = cobra.MinimumNArgs(1)
	}
	rm := &cobra.Command{
		Use: "rm <host|glob>...", Short: "Remove hosts from the " + list + " list", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch := api.DecryptChange{Source: "cli"}
			if list == "only" {
				ch.RemoveOnly = args
			} else {
				ch.RemoveNever = args
			}
			d, err := a.client().ChangeDecrypt(cmd.Context(), ch)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(d)
			}
			a.printf("%s %s - %s\n", a.c(green, "✓"), list, strings.Join(args, " "))
			return a.printDecrypt(d)
		},
	}
	cmd.AddCommand(add, rm)
	return cmd
}

// modeText paints a decrypt mode by meaning.
func (a *App) modeText(mode string) string {
	switch mode {
	case "all":
		return a.c(green, "all") + a.c(dim, "  (every host except never)")
	case "only":
		return a.c(magenta, "only") + a.c(dim, "  (just the only list)")
	case "off":
		return a.c(yellow, "off") + a.c(dim, "  (nothing is decrypted)")
	}
	return mode
}

// printDecrypt prints the full policy: mode, both lists, rejected hosts.
func (a *App) printDecrypt(d api.Decrypt) error {
	if a.jsonOut {
		return a.printJSON(d)
	}
	a.printf("%s\n", a.renderDecrypt(d, "", time.Now()))
	return nil
}

// renderDecrypt renders the policy block used by `pano decrypt` and
// `pano status`. Every entry of every list is printed; long lists wrap.
func (a *App) renderDecrypt(d api.Decrypt, indent string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%sdecrypt   %s\n", indent, a.modeText(d.Mode))
	fmt.Fprintf(&b, "%s  only    %s\n", indent, a.hostList(d.Only, indent+"          ", magenta))
	fmt.Fprintf(&b, "%s  never   %s", indent, a.hostList(d.Never, indent+"          ", dim))
	if len(d.Rejected) > 0 {
		var hosts []string
		for _, r := range d.Rejected {
			hosts = append(hosts, a.c(red, r.Host)+a.c(dim, fmt.Sprintf(" ×%d %s", r.Count, ago(now, r.Last))))
		}
		fmt.Fprintf(&b, "\n%s  %s %s\n", indent, a.c(red, "rejected"), wrapList(hosts, indent+"           ", 100))
		fmt.Fprintf(&b, "%s          %s", indent, a.c(dim, "certificate refused in the last hour (pinning?) → pano decrypt never add --rejected"))
	}
	return b.String()
}

func (a *App) hostList(hosts []string, cont, color string) string {
	if len(hosts) == 0 {
		return a.c(dim, "—")
	}
	painted := make([]string, len(hosts))
	for i, h := range hosts {
		painted[i] = a.c(color, h)
	}
	return wrapList(painted, cont, 100)
}

// wrapList joins items with two spaces, wrapping at width using cont as the
// continuation indent. Items are never dropped or truncated.
func wrapList(items []string, cont string, width int) string {
	var b strings.Builder
	col := 0
	for i, it := range items {
		w := len(stripANSI(it))
		if i > 0 {
			if col+2+w > width {
				b.WriteString("\n" + cont)
				col = 0
			} else {
				b.WriteString("  ")
				col += 2
			}
		}
		b.WriteString(it)
		col += w
	}
	return b.String()
}

func ago(now, t time.Time) string {
	d := now.Sub(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}
