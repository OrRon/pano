package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/qr"
)

// Mobile drawer: the fourth tab next to Rules, Held and Decrypt. One key
// (⏎) opens or closes the proxy to the Wi-Fi; while it is open the drawer
// shows the QR code a phone scans and every device that has connected, with
// its state; while it is closed, the devices seen last time and a reminder
// that a phone may still point at the old address.

// mobileRows is how many lines the Mobile tab needs to show everything.
func (m *Model) mobileRows() int {
	mb := m.status.Mobile
	n := 4 // tab strip, state line, rule, devices header
	if mb.Enabled {
		n += len(qr.Rows(mb.URL)) + 1
	}
	n += max(1, len(mb.Devices))
	if !mb.Enabled && mb.LastAddr != "" && len(mb.Devices) > 0 {
		n++
	}
	return n
}

// renderMobileTab renders the Mobile drawer body (below the tab strip).
func (m *Model) renderMobileTab(w, h int) []string {
	t := m.theme()
	mb := m.status.Mobile
	var out []string

	// State line.
	switch {
	case mb.Enabled:
		where := mb.Interface
		if mb.Network != "" {
			where += " · " + mb.Network
		}
		out = append(out, " "+t.fg(t.OK).Render(glyphOn+" open to your network at")+" "+t.primary().Bold(true).Render(mb.Addr)+"  "+t.muted().Render(where)+"   "+t.faint().Render("⏎ closes it"))
		if mb.Warning != "" {
			out = append(out, " "+t.fg(t.Warn).Render("! "+mb.Warning))
		}
	default:
		out = append(out, " "+t.muted().Render(glyphOff+" closed — only this Mac can reach the proxy")+"   "+t.accent().Render("⏎ opens it to phones on your Wi-Fi"))
	}
	out = append(out, t.fg(t.LineFaint).Render(strings.Repeat(glyphRule, w)))

	// QR code with the instructions beside it (below it on narrow drawers).
	if mb.Enabled {
		code := qr.Rows(mb.URL)
		qrStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#000000"))
		text := []string{
			t.primary().Bold(true).Render("On the phone") + t.muted().Render(" (same Wi-Fi)"),
			"",
			t.secondary().Render("Scan → the setup page opens: proxy"),
			t.secondary().Render("settings, certificate, live progress."),
			"",
			t.faint().Render("No camera?   ") + t.accent().Render(mb.URL),
			t.faint().Render("Proxied already? ") + t.accent().Render(mb.MagicURL),
		}
		qrW := 0
		if len(code) > 0 {
			qrW = ansi.StringWidth(code[0])
		}
		if w >= qrW+2+40 {
			top := max(0, (len(code)-len(text))/2)
			for i, l := range code {
				row := " " + qrStyle.Render(l)
				if j := i - top; j >= 0 && j < len(text) {
					row += "   " + text[j]
				}
				out = append(out, row)
			}
		} else {
			for _, l := range code {
				out = append(out, " "+qrStyle.Render(l))
			}
			for _, l := range text {
				out = append(out, " "+l)
			}
		}
		out = append(out, "")
	}

	// Devices, every one, most recent first.
	title := "devices"
	note := "proxy ✓ first request through pano · https ✓ first accepted handshake"
	if !mb.Enabled {
		note = "seen while it was open"
	}
	head := " " + t.secondary().Bold(true).Render(strings.ToUpper(title)) + "  " + t.faint().Render(note)
	count := t.secondary().Render(fmt.Sprintf("%d", len(mb.Devices)))
	out = append(out, padR(head, max(0, w-len(fmt.Sprint(len(mb.Devices)))-2))+count)
	if len(mb.Devices) == 0 {
		msg := "none yet — scan the code with a phone"
		if !mb.Enabled {
			msg = "none — open the listener and scan the code with a phone"
		}
		out = append(out, "   "+t.faint().Render(msg))
	}
	for i, d := range mb.Devices {
		row := m.renderDevice(d, mb.Enabled)
		if i == m.drawerIx {
			row = t.selected(line(row, w))
		}
		out = append(out, row)
	}
	if !mb.Enabled && mb.LastAddr != "" && len(mb.Devices) > 0 {
		out = append(out, " "+t.fg(t.Warn).Render("! a device may still point at "+mb.LastAddr+" — turn its Wi-Fi proxy off, or ⏎ to reopen"))
	}
	if len(out) > h {
		out = out[:h]
	}
	return pad(out, w, h)
}

// renderDevice is one device row: state glyph, name, address, checks, counts.
func (m *Model) renderDevice(d api.Device, live bool) string {
	t := m.theme()
	name := t.primary().Bold(true).Render(d.Label())
	if d.Name != "" {
		name += "  " + t.muted().Render(d.IP)
	}
	if !live {
		return "   " + t.muted().Render(glyphOff) + " " + name + "   " + t.faint().Render(fmt.Sprintf("seen %s · %d requests", agoText(m.now, d.LastSeen), d.Requests))
	}
	ok := t.fg(t.OK).Render(glyphOK)
	mark := t.fg(t.Warn).Render(glyphHalf)
	state := "proxy " + ok + "  https " + t.faint().Render(glyphEllipsis)
	switch {
	case d.TLS:
		mark = t.fg(t.OK).Render(glyphOn)
		state = "proxy " + ok + "  https " + ok
	case d.Rejected > 0:
		state = "proxy " + ok + "  https " + t.fg(t.Err).Render(fmt.Sprintf("%s ×%d", glyphBad, d.Rejected)) + t.muted().Render(" — certificate not trusted yet?")
	case !d.Proxy:
		mark = t.muted().Render(glyphOff)
		state = "proxy " + t.faint().Render(glyphEllipsis)
	}
	return "   " + mark + " " + name + "   " + state + "   " + t.faint().Render(fmt.Sprintf("%d requests · %s", d.Requests, agoText(m.now, d.LastSeen)))
}

// handleMobileKey handles keys specific to the Mobile tab.
func (m *Model) handleMobileKey(key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter", "space":
		on := !m.status.Mobile.Enabled
		return m, doMobile(m.c, on), true
	}
	return m, nil, false
}

// doMobile opens or closes the LAN listener and reports it as a toast.
func doMobile(c *client.Client, enabled bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mb, err := c.SetMobile(ctx, api.MobileRequest{Enabled: enabled})
		if err != nil {
			return actionMsg{err: err}
		}
		if mb.Enabled {
			return actionMsg{text: "mobile on at " + mb.Addr + " — scan the code with a phone"}
		}
		return actionMsg{text: "mobile off — only this Mac can reach the proxy"}
	}
}
