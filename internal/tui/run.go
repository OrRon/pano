package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/orron/pano/internal/client"
)

// Run starts the interactive UI and blocks until the user quits.
func Run(ctx context.Context, c *client.Client, version string) error {
	if !c.Ping(ctx) {
		return client.ErrNotRunning
	}
	m := New(c, version)
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(os.Stderr))
	_, err := p.Run()
	if m.sub != nil {
		m.sub.cancel()
	}
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("ui: %w", err)
	}
	return nil
}

// layoutViewport sizes the detail viewport for the current terminal.
func (m *Model) layoutViewport() {
	w, h := m.detailSize()
	m.vp.SetWidth(w)
	m.vp.SetHeight(h)
	m.input.SetWidth(max(10, m.width-4))
}

// setPaneContent pushes the current pane's text into the viewport.
func (m *Model) setPaneContent() {
	var text string
	switch m.pane {
	case paneExplain:
		text = m.explain.Text
	case paneDiff:
		text = m.diff.Text
	default:
		if m.detailErr != nil {
			text = "error: " + m.detailErr.Error()
		} else {
			text = m.detail.Text
		}
	}
	if m.pane != paneBody || m.detailQ.View != "raw" {
		w := m.vp.Width()
		if w > 20 {
			var wrapped []string
			for _, l := range strings.Split(sanitize(text), "\n") {
				parts := strings.Split(ansi.Wrap(l, w-1, ""), "\n")
				for i := 1; i < len(parts); i++ {
					parts[i] = "  " + strings.TrimLeft(parts[i], " ")
				}
				wrapped = append(wrapped, parts...)
			}
			text = strings.Join(wrapped, "\n")
		}
	}
	text = m.styleBody(text)
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(strings.TrimRight(text, "\n"))
	if !atBottom {
		m.vp.GotoTop()
	}
}

// detailSize returns the viewport size for the current layout.
func (m *Model) detailSize() (int, int) {
	w, h := m.width, m.height-2
	switch m.bp() {
	case bpM:
		h -= h * 2 / 5
	case bpL:
		ctxW := 0
		if m.width >= 200 {
			ctxW = 38
		}
		w = m.width - (m.width-ctxW)*55/100 - 1 - ctxW
	case bpS:
	}
	h -= 4 // head(3) + path bar
	if w < 20 {
		w = 20
	}
	if h < 3 {
		h = 3
	}
	return w, h
}
