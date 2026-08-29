package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/client"
)

// Decrypt drawer: the third tab next to Rules and Held. It shows the decrypt
// mode as a numbered segmented control and every entry of the only, never and
// rejected lists — one per row, scrolling rather than eliding.

// Section names used by decryptItem and for the text-input target.
const (
	secOnly     = "only"
	secNever    = "never"
	secRejected = "rejected"
)

// decryptItem is one selectable row of the Decrypt tab.
type decryptItem struct {
	section string
	host    string
	rej     api.RejectedHost // set for secRejected
}

// decryptItems flattens only ∪ never ∪ rejected in display order so the
// drawer cursor (drawerIx) can walk them like rules.
func (m *Model) decryptItems() []decryptItem {
	d := m.status.Decrypt
	items := make([]decryptItem, 0, len(d.Only)+len(d.Never)+len(d.Rejected))
	for _, h := range d.Only {
		items = append(items, decryptItem{section: secOnly, host: h})
	}
	for _, h := range d.Never {
		items = append(items, decryptItem{section: secNever, host: h})
	}
	for _, r := range d.Rejected {
		items = append(items, decryptItem{section: secRejected, host: r.Host, rej: r})
	}
	return items
}

// decryptSelected returns the item under the cursor.
func (m *Model) decryptSelected() (decryptItem, bool) {
	items := m.decryptItems()
	if m.drawerIx < 0 || m.drawerIx >= len(items) {
		return decryptItem{}, false
	}
	return items[m.drawerIx], true
}

// decryptRows is how many lines the Decrypt tab needs to show everything.
func (m *Model) decryptRows() int {
	d := m.status.Decrypt
	n := 3 + 3 // tabs, mode line, rule + three section headers
	n += max(1, len(d.Only)) + max(1, len(d.Never))
	if len(d.Rejected) > 0 {
		n += len(d.Rejected)
	}
	return n
}

// modeStyle paints a decrypt mode by meaning: all = OK, only = accent,
// off = warning.
func (t *Theme) modeStyle(mode string) (glyph string, st lipgloss.Style) {
	switch mode {
	case "only":
		return glyphHalf, t.accent()
	case "off":
		return glyphOff, t.fg(t.Warn)
	default:
		return glyphOn, t.fg(t.OK)
	}
}

// renderDecryptTab renders the Decrypt drawer body (below the tab strip).
func (m *Model) renderDecryptTab(w, h int) []string {
	t := m.theme()
	d := m.status.Decrypt
	var out []string

	// Mode as a numbered segmented control, like the detail tabs.
	seg := " " + t.faint().Render("mode")
	for i, mode := range []string{"all", "only", "off"} {
		g, st := t.modeStyle(mode)
		label := fmt.Sprintf("%d %s", i+1, mode)
		if mode == d.Mode {
			seg += "   " + st.Render(g+" "+label)
		} else {
			seg += "   " + t.muted().Render(glyphOff+" "+label)
		}
	}
	hint := map[string]string{
		"all":  "every host except never",
		"only": "just the only list",
		"off":  "nothing is decrypted",
	}[d.Mode]
	seg += "   " + t.faint().Render(hint)
	out = append(out, seg)
	out = append(out, t.fg(t.LineFaint).Render(strings.Repeat(glyphRule, w)))

	items := m.decryptItems()
	ix := 0
	selRow := -1
	section := func(title, note string, n int, st lipgloss.Style) {
		head := " " + st.Bold(true).Render(strings.ToUpper(title)) + "  " + t.faint().Render(note)
		count := st.Render(fmt.Sprintf("%d", n))
		out = append(out, padR(head, max(0, w-len(fmt.Sprint(n))-2))+count)
	}
	row := func(it decryptItem) {
		var s string
		switch it.section {
		case secOnly:
			s = "   " + t.accent().Render(it.host)
		case secNever:
			s = "   " + t.secondary().Render(it.host)
		default:
			s = "   " + t.fg(t.Err).Render(it.host) + "  " + t.muted().Render(fmt.Sprintf("×%d", it.rej.Count)) + "  " + t.faint().Render(agoText(m.now, it.rej.Last))
		}
		if ix == m.drawerIx {
			selRow = len(out)
			s = t.selected(line(s, w))
		}
		out = append(out, s)
		ix++
	}
	empty := func(text string) { out = append(out, "   "+t.faint().Render(text)) }

	section("only", "decrypted when mode is only", len(d.Only), t.accent())
	if len(d.Only) == 0 {
		empty("empty — press + to add a host, or ⏎ on a never/rejected host")
	}
	for _, it := range items {
		if it.section == secOnly {
			row(it)
		}
	}
	section("never", "never decrypted, in every mode (pinned apps)", len(d.Never), t.secondary())
	if len(d.Never) == 0 {
		empty("empty — press + to add a host")
	}
	for _, it := range items {
		if it.section == secNever {
			row(it)
		}
	}
	if len(d.Rejected) > 0 {
		section("rejected cert", "refused pano's certificate (last hour) — pinning?  n → never", len(d.Rejected), t.fg(t.Err))
		for _, it := range items {
			if it.section == secRejected {
				row(it)
			}
		}
	} else {
		section("rejected cert", "none in the last hour", 0, t.muted())
	}

	// Scroll rather than elide: keep the selected row in view.
	if len(out) > h && selRow >= 0 {
		start := 0
		if selRow >= h {
			start = selRow - h + 1
		}
		end := min(len(out), start+h)
		out = out[start:end]
		if start > 0 {
			out[0] = " " + t.faint().Render(fmt.Sprintf("%s %d more above", glyphUp, start))
		}
		if end < len(out)+start {
			out[len(out)-1] = " " + t.faint().Render(fmt.Sprintf("%s %d more below", glyphDown, len(items)+6-end))
		}
	}
	return pad(out, w, h)
}

// renderDecryptChips renders the header chips: the mode (with the only hosts
// named when there is room) and, when present, the rejected hosts.
func (m *Model) renderDecryptChips(w int) []string {
	t := m.theme()
	d := m.status.Decrypt
	if d.Mode == "" {
		return nil
	}
	g, st := t.modeStyle(d.Mode)
	label := "decrypt " + d.Mode
	if d.Mode == "only" {
		switch {
		case len(d.Only) == 0:
			label += " (nobody)"
		case w >= 120 && len(strings.Join(d.Only, " ")) <= w/4:
			label += " " + strings.Join(d.Only, " ")
		default:
			label += fmt.Sprintf(" ·%d", len(d.Only))
		}
	}
	chips := []string{st.Render(g) + " " + st.Render(label)}
	if n := len(d.Rejected); n > 0 {
		lbl := fmt.Sprintf("%d rejected", n)
		if w >= 120 {
			lbl = "rejected " + d.Rejected[0].Host
			if n > 1 {
				lbl += fmt.Sprintf(" +%d", n-1)
			}
		}
		chips = append(chips, t.fg(t.Err).Render(glyphBad)+" "+t.fg(t.Err).Render(lbl))
	}
	return chips
}

// handleDecryptKey handles keys specific to the Decrypt tab. It returns
// handled=false for keys shared with the other drawers.
func (m *Model) handleDecryptKey(key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "1", "2", "3":
		mode := map[string]string{"1": "all", "2": "only", "3": "off"}[key]
		return m, doDecrypt(m.c, api.DecryptChange{Mode: mode}, "decrypt "+mode), true
	case "enter", "space":
		it, ok := m.decryptSelected()
		if !ok {
			return m, nil, true
		}
		ch := api.DecryptChange{AddOnly: []string{it.host}}
		if it.section == secNever {
			ch.RemoveNever = []string{it.host}
		}
		return m, doDecrypt(m.c, ch, "only + "+it.host), true
	case "n":
		it, ok := m.decryptSelected()
		if !ok {
			return m, nil, true
		}
		ch := api.DecryptChange{AddNever: []string{it.host}}
		if it.section == secOnly {
			ch.RemoveOnly = []string{it.host}
		}
		return m, doDecrypt(m.c, ch, "never + "+it.host), true
	case "x", "delete", "backspace":
		it, ok := m.decryptSelected()
		if !ok {
			return m, nil, true
		}
		switch it.section {
		case secOnly:
			return m, doDecrypt(m.c, api.DecryptChange{RemoveOnly: []string{it.host}}, "only - "+it.host), true
		case secNever:
			return m, doDecrypt(m.c, api.DecryptChange{RemoveNever: []string{it.host}}, "never - "+it.host), true
		default:
			m.showToast("✗ " + it.host + " is a suggestion, not on a list — n adds it to never")
			return m, nil, true
		}
	case "+", "a":
		target := secOnly
		if it, ok := m.decryptSelected(); ok && it.section == secNever {
			target = secNever
		}
		m.decryptTarget = target
		m.prevMode = modeDecrypt
		m.mode = modeFilter
		m.input.SetValue("")
		m.input.Placeholder = "host or glob to add to " + target + "  (whatsapp.net covers subdomains · *.example.com)"
		return m, m.input.Focus(), true
	}
	return m, nil, false
}

// doDecrypt applies a change from the TUI and reports it as a toast.
func doDecrypt(c *client.Client, ch api.DecryptChange, text string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ch.Source = "tui"
		if _, err := c.ChangeDecrypt(ctx, ch); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{text: text}
	}
}

func agoText(now, t time.Time) string {
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
