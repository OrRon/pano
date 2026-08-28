package tui

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/store"
)

// detail tabs.
type tab int

const (
	tabSummary tab = iota
	tabRequest
	tabResponse
	tabExplain
	tabDiff
)

var tabNames = []string{"Summary", "Request", "Response", "Explain", "Diff"}

// renderDetailHead returns the breadcrumb line and the tab strip.
func (m *Model) renderDetailHead(w int, focused bool) []string {
	t := m.theme()
	r, ok := m.table.get(m.detailID)
	var head string
	if !ok {
		head = t.muted().Render(" no flow selected")
	} else {
		idStyle := t.secondary()
		if focused {
			idStyle = t.accent()
		}
		meth := r.Method
		switch r.Kind {
		case flow.KindTunnel:
			meth = "TUN"
		case flow.KindWebSocket:
			meth = "WS"
		case flow.KindHTTP:
		}
		url := hostOnly(r.Host) + r.Path
		var st string
		switch {
		case r.State == flow.StateHeld:
			st = t.heldChip().Render(glyphHeld + " HELD")
		case r.State == flow.StateActive:
			st = t.accent().Render(spinnerFrames[m.frame%len(spinnerFrames)] + " in flight")
		case r.Error != "":
			st = t.fg(t.Err).Render(glyphBad + " " + fit(r.Error, 40))
		default:
			st = t.fg(t.status(r.Status, "")).Render(fmt.Sprintf("%d %s", r.Status, http.StatusText(r.Status)))
		}
		right := st + "  " + t.muted().Render(r.Duration) + "  " + t.faint().Render(glyphUp) + t.muted().Render(store.FormatBytes(r.Up)) + " " + t.faint().Render(glyphDown) + t.muted().Render(store.FormatBytes(r.Down)) + "  " + m.flagGlyphs(r, false)
		left := " " + t.faint().Render("‹") + " " + idStyle.Bold(true).Render(r.Short) + "  " + t.fg(t.Warn).Render(meth) + " "
		room := w - ansi.StringWidth(left) - ansi.StringWidth(right) - 2
		if room < 12 {
			right = st
			room = w - ansi.StringWidth(left) - ansi.StringWidth(right) - 2
		}
		head = left + t.primary().Render(fit(url, max(0, room))) + strings.Repeat(" ", max(1, room-ansi.StringWidth(fit(url, max(0, room)))+1)) + right
	}
	head = t.raised().Render(line(head, w))

	// Tab strip.
	var tabs []string
	for i, name := range tabNames {
		if tab(i) == tabDiff && m.diff.Text == "" {
			continue
		}
		if tab(i) == tabExplain && ok && !store.IsLLMHost(hostOnly(r.Host)) && m.explain.Text == "" {
			continue
		}
		num := itoa(i + 1)
		if tab(i) == m.tab {
			tabs = append(tabs, t.accent().Render(num)+" "+t.primary().Bold(true).Underline(true).UnderlineColor(t.Accent).Render(name))
		} else {
			tabs = append(tabs, t.faint().Render(num)+" "+t.muted().Render(name))
		}
	}
	strip := " " + strings.Join(tabs, "  ")
	var right string
	if ok {
		switch m.tab {
		case tabExplain:
			right = t.fg(t.LLM).Render(glyphLLM+" "+m.explain.Provider) + t.faint().Render(" · ") + t.muted().Render(m.explain.Model)
		case tabDiff:
			right = t.muted().Render("diff " + m.marked.Short() + " ↔ " + r.Short)
		case tabSummary:
			right = t.muted().Render(r.Type) + t.faint().Render(" · ") + t.muted().Render("summary")
		default:
			right = t.muted().Render(r.Type) + t.faint().Render(" · ") + t.muted().Render(m.detailQ.View) + t.faint().Render(" · v to cycle")
		}
		if m.detailQ.RevealSecrets {
			right += t.faint().Render(" · ") + t.fg(t.Warn).Render("secrets revealed")
		}
		if m.loading {
			right += " " + t.accent().Render(spinnerFrames[m.frame%len(spinnerFrames)])
		}
	}
	gap := w - ansi.StringWidth(strip) - ansi.StringWidth(right) - 1
	if gap < 1 {
		right, gap = "", 0
	}
	strip = line(strip+strings.Repeat(" ", gap)+right, w)
	rule := t.fg(t.LineFaint).Render(strings.Repeat(glyphRule, w))
	return []string{head, strip, rule}
}

// renderPathBar shows the JSON path selector / view info under the body.
func (m *Model) renderPathBar(w int) string {
	t := m.theme()
	left := " " + t.accent().Render("$") + " "
	if m.detailQ.Path != "" {
		left += t.primary().Render(m.detailQ.Path)
	} else {
		left += t.faint().Render("path · press / to select a JSON value")
	}
	pct := ""
	if m.vp.Height() > 0 {
		pct = fmt.Sprintf("%d%%", int(m.vp.ScrollPercent()*100))
	}
	right := t.muted().Render(pct)
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right) - 1
	if gap < 1 {
		right, gap = "", 0
	}
	return t.raised().Render(line(left+strings.Repeat(" ", gap)+right, w))
}

// renderDetail renders the whole detail region at the given size.
func (m *Model) renderDetail(w, h int, focused bool) []string {
	var out []string
	head := m.renderDetailHead(w, focused)
	out = append(out, head...)
	bodyH := h - len(head) - 1
	if bodyH < 1 {
		bodyH = 1
	}
	if m.vp.Width() != w || m.vp.Height() != bodyH {
		m.vp.SetWidth(w)
		m.vp.SetHeight(bodyH)
	}
	body := m.vp.View()
	bl := strings.Split(body, "\n")
	for len(bl) < bodyH {
		bl = append(bl, "")
	}
	for i := range bl {
		bl[i] = line(bl[i], w)
	}
	out = append(out, bl[:bodyH]...)
	out = append(out, m.renderPathBar(w))
	return out
}

// renderContext renders the third column at wide sizes: explain digest for
// LLM flows, else timing + rules for the selected flow.
func (m *Model) renderContext(w, h int) []string {
	t := m.theme()
	var out []string
	title := func(s string) string {
		return t.raised().Render(line(" "+t.secondary().Bold(true).Render(strings.ToUpper(s)), w))
	}
	r, ok := m.table.get(m.detailID)
	if !ok {
		out = append(out, title("context"))
		return pad(out, w, h)
	}
	if m.explain.Text != "" && store.IsLLMHost(hostOnly(r.Host)) {
		out = append(out, title("explain  "+glyphLLM+" "+m.explain.Provider))
		var wrapped []string
		for _, l := range strings.Split(sanitize(m.explain.Text), "\n") {
			parts := strings.Split(ansi.Wrap(l, w-2, ""), "\n")
			for i := 1; i < len(parts); i++ {
				parts[i] = "  " + strings.TrimLeft(parts[i], " ")
			}
			wrapped = append(wrapped, parts...)
		}
		for _, l := range strings.Split(m.styleExplainText(strings.Join(wrapped, "\n")), "\n") {
			out = append(out, " "+l)
		}
		out = append(out, "")
	}
	out = append(out, title("timing"))
	f := m.detail.Flow
	if f != nil && !f.T.Start.IsZero() {
		total := f.T.Total()
		bar := func(label string, d, ref float64) string {
			frac := 0.0
			if ref > 0 {
				frac = d / ref
			}
			n := int(frac * float64(max(4, w-18)))
			return " " + t.muted().Render(padR(label, 5)) + t.accent().Render(strings.Repeat("█", max(0, n))) + " " + t.secondary().Render(store.FormatDuration(time.Duration(d*float64(time.Millisecond))))
		}
		tot := float64(total.Milliseconds())
		if !f.T.Connected.IsZero() && f.T.Connected.After(f.T.Start) {
			out = append(out, bar("conn", float64(f.T.Connected.Sub(f.T.Start).Milliseconds()), tot))
		}
		if !f.T.TLSDone.IsZero() && f.T.TLSDone.After(f.T.Start) {
			out = append(out, bar("tls", float64(f.T.TLSDone.Sub(f.T.Start).Milliseconds()), tot))
		}
		if !f.T.FirstByte.IsZero() {
			out = append(out, bar("ttfb", float64(f.T.TTFB().Milliseconds()), tot))
		}
		out = append(out, bar("total", tot, tot))
		if f.T.Reused {
			out = append(out, " "+t.faint().Render("connection reused"))
		}
	} else {
		out = append(out, " "+t.faint().Render(r.Duration+" total"))
	}
	out = append(out, "")
	out = append(out, title("rules"))
	if f != nil && len(f.Rules) > 0 {
		for _, h := range f.Rules {
			out = append(out, " "+t.secondary().Render(h.RuleID)+" "+t.fg(t.Warn).Render(glyphDelay+" "+h.Action)+" "+t.faint().Render(h.Note))
		}
	} else {
		out = append(out, " "+t.faint().Render("none matched"))
	}
	return pad(out, w, h)
}

func pad(lines []string, w, h int) []string {
	for i := range lines {
		lines[i] = line(lines[i], w)
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return lines[:h]
}

var _ = api.ViewSummary
