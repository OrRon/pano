package tui

// iOS Simulator suggestion — the TUI face of `pano ca install --simulator`.
// The Simulator shares the Mac's network stack, so its traffic already flows
// through pano; but each simulator keeps its own trust store, so HTTPS stays
// opaque until pano's certificate is installed there. On startup the UI
// checks once for booted simulators that lack the current certificate and
// offers the install as a small mascot modal: `i` installs and restarts the
// simulator, `esc` waits until next time, `x` never asks again for that
// device. pano never installs without the keypress.

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/orron/pano/internal/simulator"
)

type (
	simsMsg    struct{ devs []simulator.Device }
	simDoneMsg struct {
		devs []simulator.Device
		err  error
	}
)

// detectSims asks once, off the UI thread, whether a suggestion is due.
func detectSims(sim *simulator.Manager) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return simsMsg{devs: sim.Suggest(ctx)}
	}
}

// doSimInstall trusts the CA in each simulator and restarts it.
func doSimInstall(sim *simulator.Manager, devs []simulator.Device) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, d := range devs {
			if err := sim.Install(ctx, d); err != nil {
				return simDoneMsg{devs: devs, err: err}
			}
			if err := sim.Reboot(ctx, d); err != nil {
				return simDoneMsg{devs: devs, err: err}
			}
		}
		return simDoneMsg{devs: devs}
	}
}

func (m *Model) handleSimulatorKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "i", "enter":
		devs := m.sims
		m.mode, m.sims = m.prevMode, nil
		m.showToast("installing the certificate…")
		return m, doSimInstall(m.sim, devs)
	case "x":
		_ = m.sim.Dismiss(m.sims)
		m.mode, m.sims = m.prevMode, nil
		m.showToast("ok — pano ca install --simulator works anytime")
	case "esc", "q", "l":
		m.mode, m.sims = m.prevMode, nil
	}
	return m, nil
}

// simulatorRows is the panel height the modal needs.
func (m *Model) simulatorRows() int { return len(m.sims) + 9 }

// renderSimulator renders the modal: the mascot beside a short explanation,
// the detected devices in full, and every action with its key on screen.
func (m *Model) renderSimulator(w, h int) []string {
	t := m.theme()
	var out []string
	title := " " + t.secondary().Bold(true).Render("iOS SIMULATOR") + "   " + t.faint().Render("esc later")
	out = append(out, t.raised(line(title, w)))
	out = append(out, t.fg(t.LineFaint).Render(strings.Repeat(glyphRule, w)))
	box := m.renderMascot()
	msg := []string{
		t.primary().Render("A simulator is running — pano can decrypt its HTTPS."),
		t.muted().Render("It already uses the Mac's proxy; it only needs pano's"),
		t.muted().Render("certificate in its own trust store (not the keychain)."),
	}
	for i := range box {
		out = append(out, "  "+box[i]+"   "+fit(msg[i], max(0, w-16)))
	}
	out = append(out, "")
	for _, d := range m.sims {
		out = append(out, "    "+t.secondary().Render("▯ "+d.Label()))
	}
	out = append(out, "")
	out = append(out, "  "+t.accent().Render("i")+" "+t.primary().Render("install & restart the simulator")+
		"    "+t.accent().Render("esc")+" "+t.muted().Render("later")+
		"    "+t.accent().Render("x")+" "+t.muted().Render("don't ask again"))
	return pad(out, w, h)
}
