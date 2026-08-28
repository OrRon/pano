package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/orron/pano/internal/api"
)

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "pano"
	if m.mode == modeFilter {
		v.Cursor = m.cursorForInput()
	}
	return v
}

func (m *Model) cursorForInput() *tea.Cursor {
	// Filter bar sits directly above the footer.
	y := m.height - 2
	x := 3 + m.input.Position()
	c := tea.NewCursor(x, y)
	c.Shape = tea.CursorBar
	c.Blink = true
	return c
}

func (m *Model) render() string {
	if !m.ready || m.width < 20 || m.height < 6 {
		return "pano ui — terminal too small"
	}
	m.theme()
	w, h := m.width, m.height
	header := m.renderHeader(w)
	footer := m.renderFooter(w)
	filterBar := m.renderFilterBar(w)
	bodyH := h - 2
	if filterBar != "" {
		bodyH--
	}
	var body []string
	switch m.bp() {
	case bpS:
		body = m.renderSingle(w, bodyH)
	case bpM:
		body = m.renderStacked(w, bodyH)
	default:
		body = m.renderSideBySide(w, bodyH)
	}
	body = pad(body, w, bodyH)
	parts := []string{header}
	parts = append(parts, body...)
	if filterBar != "" {
		parts = append(parts, filterBar)
	}
	parts = append(parts, footer)
	base := strings.Join(parts, "\n")

	switch m.mode {
	case modeRules, modeHeld:
		if m.bp() == bpS {
			// full-screen replacement (no compositing on small terminals)
			panel := m.renderDrawer(w, bodyH)
			parts = append([]string{header}, panel...)
			parts = append(parts, footer)
			return strings.Join(parts, "\n")
		}
		pw := min(w-6, 96)
		ph := min(bodyH-2, max(8, len(m.rules)+len(m.held)+6))
		return m.overlay(base, m.renderDrawer(pw, ph), w-pw-4, 2, pw)
	case modeHelp:
		if m.bp() == bpS {
			parts = append([]string{header}, m.renderHelp(w, bodyH)...)
			parts = append(parts, footer)
			return strings.Join(parts, "\n")
		}
		pw := min(w-8, 110)
		ph := min(bodyH-2, 26)
		return m.overlay(base, m.renderHelp(pw, ph), (w-pw)/2-1, (h-ph)/2-1, pw)
	case modeList, modeDetail, modeFilter:
	}
	return base
}

// renderSingle: list OR detail.
func (m *Model) renderSingle(w, h int) []string {
	if m.mode == modeDetail {
		return m.renderDetail(w, h, true)
	}
	return m.renderList(w, h, true)
}

// renderStacked: list on top, detail below.
func (m *Model) renderStacked(w, h int) []string {
	listH := h * 2 / 5
	if listH < 6 {
		listH = 6
	}
	if m.detailID == 0 {
		listH = h
	}
	focusDetail := m.mode == modeDetail
	out := m.renderList(w, listH, !focusDetail)
	out = pad(out, w, listH)
	if m.detailID != 0 {
		out = append(out, m.renderDetail(w, h-listH, focusDetail)...)
	}
	return out
}

// renderSideBySide: list left, detail right, optional context column.
func (m *Model) renderSideBySide(w, h int) []string {
	t := m.theme()
	sep := t.fg(t.LineFaint).Render(glyphSep)
	ctxW := 0
	if w >= 200 {
		ctxW = 38
	}
	listW := (w - ctxW) * 55 / 100
	if m.detailID == 0 {
		listW = w
		ctxW = 0
	}
	detailW := w - listW - 1 - ctxW
	if ctxW > 0 {
		detailW--
	}
	focusDetail := m.mode == modeDetail
	left := pad(m.renderList(listW, h, !focusDetail), listW, h)
	if m.detailID == 0 {
		return left
	}
	right := pad(m.renderDetail(detailW, h, focusDetail), detailW, h)
	var ctx []string
	if ctxW > 0 {
		ctx = m.renderContext(ctxW, h)
	}
	out := make([]string, h)
	for i := 0; i < h; i++ {
		s := left[i] + sep + right[i]
		if ctxW > 0 {
			s += sep + ctx[i]
		}
		out[i] = s
	}
	return out
}

var _ = api.ViewSummary
