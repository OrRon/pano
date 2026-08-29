package tui

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// The mascot is the logo's rounded rectangle given a pair of eyes — Panoptes,
// the watcher. It is a status indicator before it is decoration: the eyes are
// open while pano captures, wide for a beat when a flow has just arrived, shut
// while paused and crossed when the daemon is unreachable. It blinks once in
// a while so a quiet screen still reads as alive. Everything is derived from
// the model's state and the shared 250 ms tick, so snapshots are stable.

// mood is what the mascot's eyes say.
type mood int

const (
	moodWatch mood = iota // capturing, nothing new
	moodAlert             // a flow arrived within the last few frames
	moodSleep             // capture paused (daemon or UI)
	moodDead              // daemon unreachable
)

// mascotInner is the width of the box interior in cells.
const mascotInner = 5

func (m *Model) mood() mood {
	switch {
	case m.err != nil:
		return moodDead
	case m.status.Version != "" && (!m.status.Capturing || m.paused):
		return moodSleep
	}
	for _, at := range m.table.fresh {
		if age := m.now.Sub(at); age >= 0 && age < 600*time.Millisecond {
			return moodAlert
		}
	}
	return moodWatch
}

// eyes returns the box interior for a mood at a tick frame: exactly
// mascotInner cells, eyes included.
func eyes(md mood, frame int) string {
	switch md {
	case moodDead:
		return " " + glyphBad + " " + glyphBad + " "
	case moodSleep:
		return " ─ ─ "
	case moodAlert:
		return " ◉ ◉ "
	case moodWatch:
	}
	// A 10 s cycle: glance left, glance right, one blink; otherwise centred.
	switch f := frame % 40; {
	case f >= 12 && f < 15:
		return "• •  "
	case f >= 26 && f < 29:
		return "  • •"
	case f == 35:
		return " ─ ─ "
	}
	return " • • "
}

// eyeStyle colours the eyes by mood.
func (m *Model) eyeStyle(md mood) lipgloss.Style {
	t := m.theme()
	switch md {
	case moodDead:
		return t.fg(t.Err)
	case moodSleep:
		return t.muted()
	case moodAlert:
		return t.accent().Bold(true)
	case moodWatch:
	}
	return t.primary().Bold(true)
}

// renderMascotInline is the one-row header form: thick box edges around the
// eyes, tinted with the logo gradient left to right.
func (m *Model) renderMascotInline() string {
	t := m.theme()
	md := m.mood()
	return t.fg(t.brand[0]).Render("▐") + m.eyeStyle(md).Render(eyes(md, m.frame)) + t.fg(t.brand[2]).Render("▌")
}

// renderMascot is the three-row form used by the empty state: a rounded box
// with the logo gradient running top to bottom.
func (m *Model) renderMascot() []string {
	t := m.theme()
	md := m.mood()
	rule := strings.Repeat("─", mascotInner)
	return []string{
		t.fg(t.brand[0]).Render("╭" + rule + "╮"),
		t.fg(t.brand[1]).Render("│") + m.eyeStyle(md).Render(eyes(md, m.frame)) + t.fg(t.brand[1]).Render("│"),
		t.fg(t.brand[2]).Render("╰" + rule + "╯"),
	}
}
