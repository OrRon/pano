package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/mobile"
)

func (a *App) cmdMobile() *cobra.Command {
	var (
		ip     string
		port   int
		noWait bool
		noQR   bool
	)
	cmd := &cobra.Command{
		Use:   "mobile [on]",
		Short: "Put a phone or tablet behind pano: opens the proxy to the Wi-Fi and shows a QR code",
		Long: `Opens the proxy to devices on the same network and prints a QR code that
opens pano's setup page on the phone. The page names the proxy settings to
enter, hands out the certificate in the form the device installs (a profile
on iOS, a .crt on Android) and ticks each step off live as the phone gets
there. The Mac's system proxy is untouched.

Only the proxy is exposed — never the control API or MCP — and only to
private addresses on the local network. Starts the daemon if it is not
running; 'pano mobile off' closes the listener and, unless 'pano on' is
active, stops the daemon again. 'pano off' closes it too.

Phones can also open http://pano.internal once their traffic passes through
pano, on any network.`,
		Example: `  pano mobile                    # open + QR + watch devices arrive
  pano mobile --no-wait          # open, print, return
  pano mobile --ip 10.0.0.5      # pick the interface yourself
  pano mobile status
  pano mobile off`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 || (len(args) == 1 && args[0] == "on") {
				return nil
			}
			return fmt.Errorf("unknown argument %q (use: pano mobile [on] | off | status)", strings.Join(args, " "))
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c := a.client()
			if !c.Ping(ctx) {
				if err := a.spawnDaemon(ctx, DaemonOverrides{}); err != nil {
					return err
				}
			}
			m, err := c.SetMobile(ctx, api.MobileRequest{Enabled: true, IP: ip, Port: port})
			if err != nil {
				if errors.Is(err, mobile.ErrNoLAN) || strings.Contains(err.Error(), "no LAN address") {
					return fmt.Errorf("%w\n  interfaces seen: %s", err, describeInterfaces())
				}
				return err
			}
			if a.jsonOut {
				return a.printJSON(m)
			}
			a.renderMobileBanner(m, !noQR)
			if noWait || !isTTY(os.Stdout) {
				a.printMobileDevices(m, false)
				return nil
			}
			return a.watchMobile(ctx, c)
		},
	}
	cmd.Flags().StringVar(&ip, "ip", "", "LAN address to listen on (default: the Wi-Fi interface)")
	cmd.Flags().IntVar(&port, "port", 0, "port for the LAN listener (default: the proxy port)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "print and return instead of watching devices arrive")
	cmd.Flags().BoolVar(&noQR, "no-qr", false, "skip the QR code")

	var keep bool
	off := &cobra.Command{
		Use:   "off",
		Short: "Close the proxy to the network; stops the daemon unless `pano on` is active",
		Long: `Closes the LAN listener. If the Mac's system proxy is not routed through
pano (no 'pano on'), nothing else needs the daemon, so it is stopped as well —
'pano mobile' then 'pano mobile off' leaves nothing running. Pass
--keep-daemon to only close the listener.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c := a.client()
			m, err := c.SetMobile(ctx, api.MobileRequest{Enabled: false})
			if err != nil {
				if errors.Is(err, client.ErrNotRunning) {
					a.printf("%s pano is not running; nothing is open\n", a.c(dim, "○"))
					return nil
				}
				return err
			}
			st, err := c.Status(ctx)
			if err != nil {
				return err
			}
			stop := !keep && !st.SystemProxy.Enabled
			if a.jsonOut {
				if err := a.printJSON(m); err != nil {
					return err
				}
			} else {
				hint := "  daemon kept running (" + a.c(bold, "pano on") + " is active)"
				if keep {
					hint = "  daemon kept running"
				} else if stop {
					hint = "  stopping the daemon · " + a.c(bold, "pano mobile") + " or " + a.c(bold, "pano on") + " wakes it again"
				}
				a.mascotPrint(eyesShut, [3]string{"", a.c(green, "✓") + " proxy closed to the network — only this Mac can reach it", hint})
				if len(m.Devices) > 0 && m.LastAddr != "" {
					a.printf("  %s a device may still point at %s — turn its Wi-Fi proxy off, or run %s again\n", a.c(yellow, "!"), m.LastAddr, a.c(bold, "pano mobile"))
				}
			}
			if !stop {
				return nil
			}
			return a.stopDaemon(ctx)
		},
	}
	off.Flags().BoolVar(&keep, "keep-daemon", false, "close the listener but leave the daemon running")
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the LAN listener and every device seen",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := a.client().Mobile(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(m)
			}
			a.printf("%s\n", a.renderMobile(m, "  ", time.Now()))
			return nil
		},
	}
	cmd.AddCommand(off, status)
	return cmd
}

func describeInterfaces() string {
	ifs := mobile.Interfaces()
	if len(ifs) == 0 {
		return "none with a private IPv4 address"
	}
	parts := make([]string, len(ifs))
	for i, l := range ifs {
		parts[i] = l.Interface + " " + l.IP
	}
	return strings.Join(parts, ", ")
}

// renderMobileBanner is the `pano mobile` screen: mascot, where pano
// listens, the QR code with the instructions beside it.
func (a *App) renderMobileBanner(m api.Mobile, qr bool) {
	where := m.Interface
	if m.Network != "" {
		where += " · " + m.Network
	}
	a.mascotWake([3]string{
		"",
		a.c(green, "✓") + " proxy open to your network at " + a.c(bold, m.Addr) + "  " + a.c(dim, where),
		"  " + a.c(dim, "the Mac's own proxy setting is unchanged"),
	})
	if m.Warning != "" {
		a.printf("  %s %s\n", a.c(yellow, "warn"), m.Warning)
	}
	a.printf("\n")

	text := []string{
		a.c(bold, "On the phone") + a.c(dim, " (same Wi-Fi)"),
		"",
		"Scan → the setup page opens.",
		"It gives you the proxy settings, the",
		"certificate, and ticks each step off",
		"as the phone gets there.",
		"",
		a.c(dim, "No camera?   ") + a.c(cyan, m.URL),
		a.c(dim, "Proxied already? ") + a.c(cyan, m.MagicURL),
	}
	if !qr {
		for _, l := range text {
			a.printf("  %s\n", l)
		}
		a.printf("\n")
		return
	}
	code := qrLines(m.URL, a.color)
	width := termWidth()
	qrW := 0
	if len(code) > 0 {
		qrW = ansi.StringWidth(code[0])
	}
	sideBySide := width == 0 || width >= qrW+2+42
	if !sideBySide {
		for _, l := range code {
			a.printf("  %s\n", l)
		}
		for _, l := range text {
			a.printf("  %s\n", l)
		}
		a.printf("\n")
		return
	}
	top := max(0, (len(code)-len(text))/2)
	for i, l := range code {
		line := "  " + l
		if j := i - top; j >= 0 && j < len(text) {
			line += "   " + text[j]
		}
		a.printf("%s\n", line)
	}
	a.printf("\n")
}

// watchMobile polls the daemon and redraws the device list in place until
// ctrl-c. Leaving keeps the listener open; `pano mobile off` closes it.
func (a *App) watchMobile(ctx context.Context, c *client.Client) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.printf("\033[?25l")
	defer a.printf("\033[?25h")
	rows := 0
	frame := 0
	for {
		m, err := c.Mobile(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		var lines []string
		if len(m.Devices) == 0 {
			lines = append(lines, "  "+a.c(cyan, spinnerCLI[frame%len(spinnerCLI)])+" waiting for a device…")
		} else {
			lines = append(lines, a.deviceLines(m, true, time.Now())...)
		}
		lines = append(lines, "  "+a.c(dim, "ctrl-c leaves this open · close with ")+a.c(bold, "pano mobile off"))
		if rows > 0 {
			a.printf("\033[%dA", rows)
		}
		w := termWidth()
		for _, l := range lines {
			if w > 0 {
				l = ansi.Truncate(l, w, "")
			}
			a.printf("\033[2K%s\n", l)
		}
		rows = len(lines)
		frame++
		select {
		case <-ctx.Done():
			a.printf("\n")
			return nil
		case <-time.After(700 * time.Millisecond):
		}
	}
	return nil
}

var spinnerCLI = []string{"◐", "◓", "◑", "◒"}

// deviceLines renders one line per device, every device, most recent first.
// With the listener off the entries are history: no live marks, just when
// each device was last seen.
func (a *App) deviceLines(m api.Mobile, live bool, now time.Time) []string {
	var out []string
	for _, d := range m.Devices {
		name := a.c(bold, d.Label())
		if d.Name != "" {
			name += "  " + a.c(dim, d.IP)
		}
		if !m.Enabled {
			out = append(out, fmt.Sprintf("  %s %s   %s", a.c(dim, "○"), name, a.c(dim, fmt.Sprintf("seen %s while mobile was on · %d requests", ago(now, d.LastSeen), d.Requests))))
			continue
		}
		mark := a.c(yellow, "◐")
		state := "proxy " + a.c(green, "✓") + "  https " + a.c(dim, "…")
		switch {
		case d.TLS:
			mark = a.c(green, "●")
			state = "proxy " + a.c(green, "✓") + "  https " + a.c(green, "✓")
		case d.Rejected > 0:
			state = "proxy " + a.c(green, "✓") + "  https " + a.c(red, fmt.Sprintf("✕ ×%d", d.Rejected)) + a.c(dim, " — certificate not trusted yet?")
		case !d.Proxy:
			mark = a.c(dim, "○")
			state = "proxy " + a.c(dim, "…")
		}
		extra := fmt.Sprintf("%d requests", d.Requests)
		if !live {
			extra += " · last " + ago(now, d.LastSeen)
		}
		out = append(out, fmt.Sprintf("  %s %s   %s   %s", mark, name, state, a.c(dim, extra)))
	}
	return out
}

func (a *App) printMobileDevices(m api.Mobile, live bool) {
	if len(m.Devices) == 0 {
		a.printf("  %s no device has connected yet\n", a.c(dim, "○"))
		return
	}
	for _, l := range a.deviceLines(m, live, time.Now()) {
		a.printf("%s\n", l)
	}
}

// renderMobile is the block `pano status` and `pano mobile status` share.
func (a *App) renderMobile(m api.Mobile, indent string, now time.Time) string {
	var b strings.Builder
	if !m.Enabled {
		fmt.Fprintf(&b, "%smobile    %s off — %s opens the proxy to phones on your Wi-Fi", indent, a.c(dim, "○"), a.c(bold, "pano mobile"))
	} else {
		where := m.Interface
		if m.Network != "" {
			where += " · " + m.Network
		}
		fmt.Fprintf(&b, "%smobile    %s ON  %s  %s  %s", indent, a.c(green, "●"), a.c(bold, m.Addr), a.c(dim, where), a.c(cyan, m.MagicURL))
		if m.Warning != "" {
			fmt.Fprintf(&b, "\n%s          %s %s", indent, a.c(yellow, "warn"), m.Warning)
		}
	}
	if len(m.Devices) > 0 {
		fmt.Fprintf(&b, "\n%s  devices", indent)
		for _, l := range a.deviceLines(m, false, now) {
			fmt.Fprintf(&b, "\n%s%s", indent, strings.TrimPrefix(l, "  "))
		}
		if !m.Enabled && m.LastAddr != "" {
			fmt.Fprintf(&b, "\n%s  %s a device may still point at %s — turn its Wi-Fi proxy off, or run %s again", indent, a.c(yellow, "!"), m.LastAddr, a.c(bold, "pano mobile"))
		}
	}
	return b.String()
}
