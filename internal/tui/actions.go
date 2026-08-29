package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

// Actions menu: `o` on a selected flow (list or detail) opens a small
// overlay listing everything you can do with that entry — decrypt only /
// never for its host, filter to the host, replay, mark, explain. Every item
// shows its key; the same keys keep working in the list without the menu.

type action struct {
	key, label, note string
	cmd              func(m *Model, r api.FlowRow) (tea.Model, tea.Cmd)
}

// actionsFor lists the actions available for a flow row.
func (m *Model) actionsFor(r api.FlowRow) []action {
	d := m.status.Decrypt
	onOnly := hostListed(d.Only, r.Host)
	onNever := hostListed(d.Never, r.Host)

	onlyLabel, onlyNote := "decrypt only "+r.Host, "adds to the only list and switches mode to only"
	switch {
	case onOnly && d.Mode == "only":
		onlyLabel, onlyNote = "already decrypt-only "+r.Host, "on the only list, mode is only"
	case onOnly:
		onlyNote = "already on the only list; switches mode to only"
	case d.Mode == "only":
		onlyNote = "adds to the only list"
	}
	neverLabel, neverNote := "never decrypt "+r.Host, "adds to the never list (wins in every mode)"
	if onNever {
		neverLabel, neverNote = "stop never-decrypting "+r.Host, "removes from the never list"
	}
	acts := []action{
		{"o", onlyLabel, onlyNote, func(m *Model, r api.FlowRow) (tea.Model, tea.Cmd) {
			ch := api.DecryptChange{Mode: "only"}
			if !onOnly {
				ch.AddOnly = []string{r.Host}
			}
			if onNever {
				ch.RemoveNever = []string{r.Host}
			}
			return m, doDecrypt(m.c, ch, "only "+r.Host)
		}},
		{"n", neverLabel, neverNote, func(m *Model, r api.FlowRow) (tea.Model, tea.Cmd) {
			if onNever {
				return m, doDecrypt(m.c, api.DecryptChange{RemoveNever: []string{r.Host}}, "never - "+r.Host)
			}
			ch := api.DecryptChange{AddNever: []string{r.Host}}
			if onOnly {
				ch.RemoveOnly = []string{r.Host}
			}
			return m, doDecrypt(m.c, ch, "never + "+r.Host)
		}},
		{"/", "filter host=" + r.Host, "show only this host's flows", func(m *Model, r api.FlowRow) (tea.Model, tea.Cmd) {
			m.setFilter("host=" + r.Host)
			m.cursor, m.offset = 0, 0
			return m, m.reloadFlows()
		}},
	}
	if r.Kind != flow.KindTunnel {
		acts = append(acts,
			action{"R", "replay", "re-send through the proxy", func(m *Model, r api.FlowRow) (tea.Model, tea.Cmd) {
				m.showToast("replaying " + r.Short + "…")
				return m, doReplay(m.c, r.ID)
			}},
			action{"m", "mark for diff", "then d on another flow", func(m *Model, r api.FlowRow) (tea.Model, tea.Cmd) {
				m.marked = r.ID
				m.showToast("marked " + r.Short + " — select another and press d to diff")
				return m, nil
			}},
			action{"x", "explain", "LLM digest of this flow", func(m *Model, r api.FlowRow) (tea.Model, tea.Cmd) {
				return m.openDetail(paneExplain)
			}},
		)
	}
	return acts
}

func hostListed(list []string, host string) bool {
	for _, h := range list {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// openActions opens the menu for the selected flow.
func (m *Model) openActions(from mode) (tea.Model, tea.Cmd) {
	if _, ok := m.selected(); !ok {
		return m, nil
	}
	m.prevMode = from
	m.mode = modeActions
	m.actionIx = 0
	return m, nil
}

// handleActionsKey drives the menu: j/k move, ⏎ runs the highlighted item,
// any item key runs it directly, esc/o close.
func (m *Model) handleActionsKey(key string) (tea.Model, tea.Cmd) {
	r, ok := m.selected()
	if !ok {
		m.mode = m.prevMode
		return m, nil
	}
	acts := m.actionsFor(r)
	switch key {
	case "esc", "o", "q":
		m.mode = m.prevMode
		return m, nil
	case "j", "down":
		if m.actionIx < len(acts)-1 {
			m.actionIx++
		}
		return m, nil
	case "k", "up":
		if m.actionIx > 0 {
			m.actionIx--
		}
		return m, nil
	case "enter", "space":
		m.mode = m.prevMode
		return acts[m.actionIx].cmd(m, r)
	case "?":
		m.mode = modeHelp
		return m, nil
	}
	for _, a := range acts {
		if a.key == key {
			m.mode = m.prevMode
			return a.cmd(m, r)
		}
	}
	return m, nil
}

// actionsRows is the panel height the menu needs.
func (m *Model) actionsRows() int {
	r, ok := m.selected()
	if !ok {
		return 4
	}
	return len(m.actionsFor(r)) + 4
}

// renderActions renders the menu panel.
func (m *Model) renderActions(w, h int) []string {
	t := m.theme()
	r, ok := m.selected()
	if !ok {
		return pad(nil, w, h)
	}
	acts := m.actionsFor(r)
	var out []string
	title := " " + t.secondary().Bold(true).Render("ACTIONS") + "  " + t.primary().Render(fit(r.Method+" "+r.Host+r.Path, max(10, w-24))) + "   " + t.faint().Render("esc closes")
	out = append(out, t.raised(line(title, w)))
	out = append(out, t.fg(t.LineFaint).Render(strings.Repeat(glyphRule, w)))
	labelW := 0
	for _, a := range acts {
		labelW = max(labelW, ansi.StringWidth(a.label))
	}
	labelW = min(labelW+2, max(20, w-40))
	for i, a := range acts {
		keyStyle, labelStyle := t.accent(), t.primary()
		switch a.key {
		case "o":
			labelStyle = t.accent()
		case "n":
			labelStyle = t.fg(t.Warn)
		}
		row := "  " + padR(keyStyle.Render(a.key), 4) + padR(labelStyle.Render(fit(a.label, labelW-1)), labelW) + t.faint().Render(fit(a.note, max(0, w-labelW-8)))
		if i == m.actionIx {
			row = t.selected(line(row, w))
		}
		out = append(out, row)
	}
	out = append(out, "")
	d := m.status.Decrypt
	g, st := t.modeStyle(d.Mode)
	state := fmt.Sprintf("%s decrypt %s", g, d.Mode)
	switch {
	case hostListed(d.Never, r.Host):
		state += " · " + r.Host + " is on never"
	case hostListed(d.Only, r.Host):
		state += " · " + r.Host + " is on only"
	}
	out = append(out, "  "+st.Render(state))
	return pad(out, w, h)
}
