package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/orron/pano/internal/api"
)

// renderDrawer renders the rules or held list.
func (m *Model) renderDrawer(w, h int) []string {
	t := m.theme()
	var out []string
	tabs := func(active string) string {
		var parts []string
		for _, n := range []string{"Rules", "Held", "Decrypt", "Mobile"} {
			if n == active {
				parts = append(parts, t.primary().Underline(true).UnderlineColor(t.Accent).Render(n))
			} else {
				parts = append(parts, t.muted().Render(n))
			}
		}
		return " " + strings.Join(parts, "   ")
	}
	if m.mode == modeMobile {
		state := t.muted().Render(glyphOff + " off")
		if m.status.Mobile.Enabled {
			state = t.fg(t.OK).Render(glyphMobile + " " + m.status.Mobile.Addr)
		}
		out = append(out, t.raised(line(tabs("Mobile")+"   "+state, w)))
		out = append(out, m.renderMobileTab(w, h-1)...)
		return pad(out, w, h)
	}
	if m.mode == modeDecrypt {
		_, st := t.modeStyle(m.status.Decrypt.Mode)
		out = append(out, t.raised(line(tabs("Decrypt")+"   "+st.Render("mode "+m.status.Decrypt.Mode), w)))
		out = append(out, m.renderDecryptTab(w, h-1)...)
		return pad(out, w, h)
	}
	if m.mode == modeHeld {
		out = append(out, t.raised(line(tabs("Held")+"   "+t.faint().Render(fmt.Sprintf("%d waiting", len(m.held))), w)))
		out = append(out, t.fg(t.LineFaint).Render(strings.Repeat(glyphRule, w)))
		if len(m.held) == 0 {
			out = append(out, " "+t.faint().Render("nothing held — add a breakpoint rule (preset hold) to pause requests"))
		}
		for i, hd := range m.held {
			row := " " + t.heldChip().Render(" "+glyphHeld+" ") + " " + t.secondary().Render(hd.Short) + " " + t.fg(t.Warn).Render(hd.Method) + " " + t.primary().Render(fit(hd.URL, max(10, w-44))) + "  " + t.muted().Render(hd.Phase+" · "+hd.Age)
			if i == m.drawerIx {
				row = t.selected(line(row, w))
			}
			out = append(out, row)
		}
		return pad(out, w, h)
	}
	out = append(out, t.raised(line(tabs("Rules")+"   "+t.faint().Render(fmt.Sprintf("%d rules", len(m.rules))), w)))
	out = append(out, t.fg(t.LineFaint).Render(strings.Repeat(glyphRule, w)))
	if len(m.rules) == 0 {
		out = append(out, " "+t.faint().Render("no rules — try: pano rules add --preset slow_network --param host=api.example.com"))
	}
	for i, r := range m.rules {
		out = append(out, m.renderRule(r, w, i == m.drawerIx))
	}
	return pad(out, w, h)
}

func (m *Model) renderRule(r api.Rule, w int, selected bool) string {
	t := m.theme()
	on := r.Enabled == nil || *r.Enabled
	dot := t.fg(t.OK).Render(glyphOn)
	nameStyle := t.primary()
	if !on {
		dot = t.muted().Render(glyphOff)
		nameStyle = t.muted()
	}
	var acts []string
	for _, a := range r.Actions {
		g, c := glyphTag, t.secondary()
		label := a.Type
		switch a.Type {
		case "delay":
			g, c, label = glyphDelay, t.fg(t.Warn), fmt.Sprintf("delay %dms", a.MS)
		case "throttle":
			g, c, label = glyphThrottle, t.fg(t.Warn), fmt.Sprintf("throttle %dKB/s", a.KBps)
		case "mock", "mock_every_n":
			g, c, label = glyphMock, t.fg(t.Mock), fmt.Sprintf("mock %d", a.Status)
		case "block":
			g, c, label = glyphBlocked, t.fg(t.Err), "block "+a.Mode
		case "breakpoint":
			g, c, label = glyphHeld, t.accent(), "hold"
		case "rewrite_body", "set_header", "remove_header", "set_query", "redirect":
			g, c = glyphRewrite, t.secondary()
		}
		acts = append(acts, c.Render(g)+" "+t.secondary().Render(label))
	}
	var match []string
	if r.Match.Host != "" {
		match = append(match, r.Match.Host)
	}
	if r.Match.Path != "" {
		match = append(match, r.Match.Path)
	}
	if len(r.Match.Method) > 0 {
		match = append(match, strings.Join(r.Match.Method, "|"))
	}
	if len(match) == 0 {
		match = []string{"*"}
	}
	name := r.Name
	if name == "" {
		name = r.ID
	}
	meta := fmt.Sprintf("hits %d", r.Hits)
	if r.TTLSeconds > 0 {
		meta += fmt.Sprintf(" · ttl %ds", r.TTLSeconds)
	}
	if r.Probability > 0 && r.Probability < 1 {
		meta += fmt.Sprintf(" · p=%.2f", r.Probability)
	}
	row := " " + dot + " " + t.muted().Render(r.ID) + "  " + nameStyle.Bold(true).Render(fit(name, 22)) + "  " + strings.Join(acts, " ") + "  " + t.muted().Render(fit(strings.Join(match, " "), 34)) + "  " + t.faint().Render(meta)
	if selected {
		return t.selected(line(row, w))
	}
	return line(row, w)
}

// renderHelp renders the key reference.
func (m *Model) renderHelp(w, h int) []string {
	t := m.theme()
	type group struct {
		name string
		keys [][2]string
	}
	groups := []group{
		{"navigate", [][2]string{{"j/k ↑/↓", "move"}, {"g/G", "top/bottom"}, {"^d/^u", "half page"}, {"⏎ l", "open flow"}, {"esc h", "back / clear filter"}, {"f", "follow newest"}, {"space", "pause list"}}},
		{"inspect", [][2]string{{"1-5", "summary/request/response/explain/diff"}, {"⇥", "next tab"}, {"v", "cycle view: summary→schema→pretty→raw"}, {"/", "filter (list) · JSON path (detail)"}, {"x", "explain LLM call"}, {"S", "reveal secrets (audited)"}, {"H", "toggle headers"}}},
		{"act", [][2]string{{"m", "mark for diff"}, {"d", "diff marked ↔ selected"}, {"R", "replay"}, {"X", "clear all captured flows (press X twice)"}, {"c", "toggle capture"}, {"r", "rules drawer"}, {"h", "held requests"}, {"D", "decrypt drawer"}, {"M", "mobile drawer: open the proxy to phones, QR code, devices"}, {"o", "options for the selected flow: decrypt only/never its host, filter, replay…"}, {"n", "never decrypt this tunnel's host"}, {"q", "quit: turn pano off, or keep it running in the background"}}},
		{"drawers", [][2]string{{"⏎", "toggle rule · resume held · host → only"}, {"x", "remove rule · drop held · remove host"}, {"n", "host → never"}, {"+", "type a host to add"}, {"1/2/3", "decrypt all / only / off"}, {"⏎ (mobile)", "open / close the proxy to the Wi-Fi"}, {"⇥", "rules → held → decrypt → mobile"}}},
	}
	var out []string
	out = append(out, t.raised(line(" "+t.secondary().Bold(true).Render("KEYS")+"   "+t.faint().Render("esc closes"), w)))
	out = append(out, "")
	col := max(30, (w-4)/2)
	var left, right []string
	for gi, g := range groups {
		var lines []string
		lines = append(lines, " "+t.faint().Render(strings.ToUpper(g.name)))
		for _, k := range g.keys {
			lines = append(lines, "   "+padR(t.accent().Render(k[0]), 14)+t.secondary().Render(k[1]))
		}
		lines = append(lines, "")
		if gi%2 == 0 {
			left = append(left, lines...)
		} else {
			right = append(right, lines...)
		}
	}
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, padR(fit(l, col), col)+"  "+fit(r, w-col-2))
	}
	out = append(out, "", " "+t.faint().Render("filter syntax: host=*.openai.com path=/v1/* method=POST status=!2xx since=15m type=json errors=1 state=held  · bare words search url/headers"))
	return pad(out, w, h)
}

// overlay composes a panel on top of base using Lip Gloss layers.
func (m *Model) overlay(base string, panel []string, x, y, w int) string {
	t := m.theme()
	for i := range panel {
		panel[i] = line(panel[i], w)
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.LineFaint).
		Background(t.BgOverlay).
		Render(strings.Join(panel, "\n"))
	canvas := lipgloss.NewCanvas(m.width, m.height)
	comp := lipgloss.NewCompositor(
		lipgloss.NewLayer(base).Z(0),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	)
	canvas.Compose(comp)
	return canvas.Render()
}

var _ = ansi.Strip
