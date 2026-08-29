package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/orron/pano/internal/client"
)

// Quit overlay (ADR 0009). `q` in the list asks what leaving means: turn
// pano off (restore the Mac's proxy settings, stop the daemon — like
// quitting an app) or close this window and keep pano capturing in the
// background. The highlighted default follows how the UI was opened: a
// `pano on` UI owns the daemon and defaults to off; a `pano ui` window that
// attached to a running daemon defaults to background. Either way both
// choices are on screen with their keys.

// Exit is why the UI returned.
type Exit int

const (
	// ExitInterrupt is ctrl-c or a cancelled context: the daemon applies
	// the ownership rule on its own (off if this UI owned it).
	ExitInterrupt Exit = iota
	// ExitOff means the user chose "turn pano off"; the daemon is stopping.
	ExitOff
	// ExitDetach means the user chose "keep running in the background".
	ExitDetach
	// ExitGone means the daemon went away underneath the UI (`pano off`
	// in another terminal, a crash).
	ExitGone
)

type quitItem struct {
	key, label, note string
}

func (m *Model) quitItems() []quitItem {
	return []quitItem{
		{"q", "quit and turn pano off", "restores the Mac's proxy settings and stops pano"},
		{"b", "keep pano running in the background", "pano ui reopens this window · pano off stops it"},
	}
}

// openQuit shows the overlay with the default that matches ownership.
func (m *Model) openQuit() (tea.Model, tea.Cmd) {
	m.prevMode = m.mode
	m.mode = modeQuit
	m.quitIx = 0
	if !m.own {
		m.quitIx = 1
	}
	return m, nil
}

func (m *Model) handleQuitKey(key string) (tea.Model, tea.Cmd) {
	items := m.quitItems()
	switch key {
	case "esc":
		m.mode = m.prevMode
		return m, nil
	case "j", "down":
		if m.quitIx < len(items)-1 {
			m.quitIx++
		}
		return m, nil
	case "k", "up":
		if m.quitIx > 0 {
			m.quitIx--
		}
		return m, nil
	case "enter", "space":
		key = items[m.quitIx].key
	}
	switch key {
	case "q":
		m.showToast("turning pano off…")
		return m, doOff(m.c)
	case "b":
		return m, doDisown(m.c)
	}
	return m, nil
}

// quitRows is the panel height the overlay needs.
func (m *Model) quitRows() int { return len(m.quitItems()) + 5 }

// renderQuit renders the overlay panel.
func (m *Model) renderQuit(w, h int) []string {
	t := m.theme()
	var out []string
	title := " " + t.secondary().Bold(true).Render("QUIT") + "   " + t.faint().Render("esc stays")
	out = append(out, t.raised(line(title, w)))
	out = append(out, t.fg(t.LineFaint).Render(strings.Repeat(glyphRule, w)))
	items := m.quitItems()
	labelW := 0
	for _, it := range items {
		labelW = max(labelW, len(it.label))
	}
	labelW = min(labelW+2, max(20, w-40))
	for i, it := range items {
		labelStyle := t.primary()
		if it.key == "q" {
			labelStyle = t.fg(t.Warn)
		}
		row := "  " + padR(t.accent().Render(it.key), 4) + padR(labelStyle.Render(fit(it.label, labelW-1)), labelW) + t.faint().Render(fit(it.note, max(0, w-labelW-8)))
		if i == m.quitIx {
			row = t.selected(line(row, w))
		}
		out = append(out, row)
	}
	out = append(out, "")
	state := glyphOn + " pano is on"
	if m.status.SystemProxy.Enabled {
		state += " · system proxy → " + m.status.ProxyAddr
	}
	if m.status.Mobile.Enabled {
		state += " · mobile " + m.status.Mobile.Addr
	}
	if m.own {
		state += " · this window owns it"
	} else {
		state += " · started in the background"
	}
	out = append(out, "  "+t.muted().Render(state))
	return pad(out, w, h)
}

// ---- daemon calls ----

type (
	offDoneMsg    struct{ err error }
	disownDoneMsg struct{ err error }
	attachLostMsg struct{}
)

func doOff(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return offDoneMsg{err: c.Off(ctx)}
	}
}

func doDisown(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.Disown(ctx)
		return disownDoneMsg{err: err}
	}
}

// attachSub holds the daemon-side attachment for the life of the UI.
type attachSub struct {
	gone   <-chan struct{}
	cancel context.CancelFunc
}

func openAttach(c *client.Client, own bool) (*attachSub, error) {
	ctx, cancel := context.WithCancel(context.Background())
	gone, err := c.Attach(ctx, own)
	if err != nil {
		cancel()
		return nil, err
	}
	return &attachSub{gone: gone, cancel: cancel}, nil
}

func (s *attachSub) wait() tea.Cmd {
	return func() tea.Msg {
		<-s.gone
		return attachLostMsg{}
	}
}
