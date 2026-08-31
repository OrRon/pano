package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/ca"
)

func (a *App) cmdCA() *cobra.Command {
	cmd := &cobra.Command{Use: "ca", Short: "Manage the local certificate authority"}
	var system, sim bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Trust the pano CA (login keychain; macOS asks for your password once)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if sim {
				return a.caSimulatorInstall(cmd.Context())
			}
			if err := a.caInstall(cmd.Context(), system); err != nil {
				return err
			}
			a.printf("%s pano CA trusted\n", a.c(green, "✓"))
			a.printf("  Firefox users: set about:config security.enterprise_roots.enabled = true\n")
			return nil
		},
	}
	install.Flags().BoolVar(&system, "system", false, "install into the System keychain (all users; needs sudo)")
	install.Flags().BoolVar(&sim, "simulator", false, "trust the CA inside every booted iOS Simulator instead of the Mac keychain")
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the pano CA from the keychain",
		RunE: func(cmd *cobra.Command, _ []string) error {
			auth, err := a.loadCA()
			if err != nil {
				return err
			}
			if err := ca.NewTrustStore().Uninstall(cmd.Context(), a.paths.CACert(), auth.Subject()); err != nil {
				return err
			}
			a.printf("%s pano CA removed from keychain\n", a.c(green, "✓"))
			return nil
		},
	}
	path := &cobra.Command{
		Use:   "path",
		Short: "Print the CA certificate path",
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, err := a.loadCA(); err != nil {
				return err
			}
			a.println(a.paths.CACert())
			return nil
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether the CA is trusted",
		RunE: func(cmd *cobra.Command, _ []string) error {
			auth, err := a.loadCA()
			if err != nil {
				return err
			}
			st := ca.NewTrustStore().Status(cmd.Context(), a.paths.CACert(), auth.Subject())
			if a.jsonOut {
				return a.printJSON(map[string]any{
					"subject": auth.Subject(), "path": a.paths.CACert(), "not_after": auth.NotAfter(),
					"trust": st, "warning": auth.ExpiryWarning(),
				})
			}
			mark := a.c(red, "○")
			if st.Installed {
				mark = a.c(green, "●")
			}
			a.printf("%s %s\n  %s\n  %s\n  expires %s (%s)\n", mark, auth.Subject(), a.paths.CACert(), st.Detail,
				auth.NotAfter().Format("2006-01-02"), a.c(dim, "valid "+formatDays(time.Until(auth.NotAfter()))))
			if w := auth.ExpiryWarning(); w != "" {
				a.printf("  %s %s\n", a.c(yellow, "warn"), w)
			}
			if !st.Installed && st.Supported {
				a.printf("  run %s\n", a.c(bold, "pano ca install"))
			}
			return nil
		},
	}
	reset := &cobra.Command{
		Use:   "reset",
		Short: "Generate a new CA (removes the old one from the keychain; re-run install)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.confirm("This deletes the current CA, removes it from the keychain and clears cached certificates. Continue?") {
				return nil
			}
			// Untrust the outgoing root first: otherwise a root whose key is
			// about to be destroyed would stay trusted in the keychain forever.
			if old, err := a.loadCA(); err == nil {
				ts := ca.NewTrustStore()
				if st := ts.Status(cmd.Context(), a.paths.CACert(), old.Subject()); st.Supported {
					if err := ts.Uninstall(cmd.Context(), a.paths.CACert(), old.Subject()); err != nil {
						a.warn("could not remove the old CA from the keychain: %v — remove %q by hand", err, old.Subject())
					}
				}
			}
			for _, p := range []string{a.paths.CACert(), a.paths.CAKey(), a.paths.LeafKey()} {
				_ = os.Remove(p)
			}
			_ = os.RemoveAll(a.paths.CertCache())
			if _, err := a.loadCA(); err != nil {
				return err
			}
			a.printf("%s new CA generated at %s — run `pano ca install`\n", a.c(green, "✓"), a.paths.CACert())
			return nil
		},
	}
	cmd.AddCommand(install, uninstall, path, status, reset)
	return cmd
}

// formatDays renders a remaining lifetime as "N more days" / "expired".
func formatDays(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	return fmt.Sprintf("%d more days", int(d.Hours()/24))
}

func (a *App) loadCA() (*ca.Authority, error) {
	if err := a.paths.Ensure(); err != nil {
		return nil, err
	}
	return ca.Load(ca.Options{
		CertFile: a.paths.CACert(), KeyFile: a.paths.CAKey(), LeafKeyFile: a.paths.LeafKey(), CacheDir: a.paths.CertCache(),
	})
}

func (a *App) caInstall(ctx context.Context, system bool) error {
	auth, err := a.loadCA()
	if err != nil {
		return err
	}
	if from := auth.RotatedFrom(); from != "" {
		a.warn("the previous root CA (%s) had expired; a new one was generated", from)
	}
	ts := ca.NewTrustStore()
	if st := ts.Status(ctx, a.paths.CACert(), auth.Subject()); st.Installed {
		return nil
	}
	if err := ts.Install(ctx, a.paths.CACert(), auth.Subject(), system); err != nil {
		if errors.Is(err, ca.ErrUnsupported) {
			return fmt.Errorf("%w\n%s", err, ts.ManualInstructions(a.paths.CACert()))
		}
		return fmt.Errorf("%w\nManual steps:\n%s", err, ts.ManualInstructions(a.paths.CACert()))
	}
	return nil
}
