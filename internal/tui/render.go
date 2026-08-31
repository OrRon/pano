package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/store"
)

// breakpoint classes.
type bp int

const (
	bpS bp = iota // single region
	bpM           // stacked
	bpL           // side by side
)

func (m *Model) bp() bp {
	switch {
	case m.width < 100 || m.height < 30:
		return bpS
	case m.width < 160:
		return bpM
	default:
		return bpL
	}
}

func (m *Model) theme() *Theme {
	if m.th == nil || m.th.dark != m.dark {
		m.th = newTheme(m.dark)
	}
	return m.th
}

// ---- helpers ----

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, glyphEllipsis)
}

// fitLeft keeps the tail of s (for hosts: the TLD survives).
func fitLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := ansi.StringWidth(s)
	if sw <= w {
		return s
	}
	return ansi.TruncateLeft(s, sw-w+1, glyphEllipsis)
}

func padR(s string, w int) string {
	sw := ansi.StringWidth(s)
	if sw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-sw)
}

func padL(s string, w int) string {
	sw := ansi.StringWidth(s)
	if sw >= w {
		return s
	}
	return strings.Repeat(" ", w-sw) + s
}

// line pads or cuts a rendered line to exactly w cells.
func line(s string, w int) string {
	sw := ansi.StringWidth(s)
	switch {
	case sw == w:
		return s
	case sw < w:
		return s + strings.Repeat(" ", w-sw)
	default:
		return ansi.Truncate(s, w, "")
	}
}

func sanitize(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\t", "  ")
	s = strings.ReplaceAll(s, "\r", "")
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 && r != '\n' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ---- header ----

func (m *Model) renderHeader(w int) string {
	t := m.theme()
	st := m.status
	chip := func(g string, gc lipgloss.Style, label string, lc lipgloss.Style) string {
		return gc.Render(g) + " " + lc.Render(label)
	}
	var parts []string
	parts = append(parts, m.renderMascotInline()+" "+t.primary().Bold(true).Render("pano"))
	switch {
	case m.err != nil:
		parts = append(parts, chip(glyphBad, t.fg(t.Err), "daemon unreachable · retrying "+spinnerFrames[m.frame%len(spinnerFrames)], t.fg(t.Err)))
	case st.Version == "":
		parts = append(parts, chip(spinnerFrames[m.frame%len(spinnerFrames)], t.accent(), "connecting", t.muted()))
	default:
		switch {
		case st.Capturing && !m.paused:
			parts = append(parts, chip(glyphOn, t.fg(t.OK), "capturing", t.secondary()))
		case m.paused:
			parts = append(parts, chip(glyphOff, t.fg(t.Warn), "paused (ui)", t.fg(t.Warn)))
		default:
			parts = append(parts, chip(glyphOff, t.fg(t.Warn), "paused", t.fg(t.Warn)))
		}
		if st.SystemProxy.Enabled {
			lbl := "proxy on"
			if w >= 160 && st.SystemProxy.Detail != "" {
				lbl += " (" + st.SystemProxy.Detail + ")"
			}
			parts = append(parts, chip(glyphProxy, t.fg(t.OK), lbl, t.secondary()))
		} else {
			parts = append(parts, chip(glyphProxy, t.muted(), "proxy off", t.muted()))
		}
		if st.CA.Trusted {
			lbl := "ca"
			if w >= 120 {
				lbl = "ca trusted"
			}
			parts = append(parts, chip(glyphOK, t.fg(t.OK), lbl, t.secondary()))
		} else if st.CA.Supported {
			parts = append(parts, chip(glyphBad, t.fg(t.Err), "ca untrusted", t.fg(t.Err)))
		}
		parts = append(parts, m.renderDecryptChips(w)...)
		if st.Mobile.Enabled {
			// Wide: address and the latest device; otherwise just a count —
			// the header is already full at 120 columns.
			lbl := "mobile"
			if w >= 160 {
				lbl = "mobile " + st.Mobile.Addr
			}
			if n := len(st.Mobile.Devices); n > 0 {
				if w >= 160 && st.Mobile.Devices[0].Name != "" {
					lbl += " · " + st.Mobile.Devices[0].Name
					if n > 1 {
						lbl += fmt.Sprintf(" +%d", n-1)
					}
				} else {
					lbl += fmt.Sprintf(" ·%d", n)
				}
			}
			parts = append(parts, chip(glyphMobile, t.fg(t.OK), lbl, t.secondary()))
		}
		if st.Rules > 0 {
			lbl := fmt.Sprintf("%d rules", st.Rules)
			if w >= 160 && st.RulesEnabled != st.Rules {
				lbl = fmt.Sprintf("%d rules · %d on", st.Rules, st.RulesEnabled)
			}
			parts = append(parts, chip(glyphLLM, t.fg(t.LLM), lbl, t.secondary()))
		}
		if n := len(m.held); n > 0 {
			parts = append(parts, t.heldChip().Render(fmt.Sprintf(" %s %d held ", glyphHeld, n)))
		}
		if m.update != nil && w >= 100 {
			lbl := m.update.Latest + " available"
			if w >= 160 {
				lbl += " · " + m.update.Hint
			}
			parts = append(parts, chip(glyphUp, t.accent(), lbl, t.accent()))
		}
	}
	gapStr := "   "
	if w < 160 {
		gapStr = "  "
	}
	left := strings.Join(parts, gapStr)

	// Right side, shed least useful first when the row is tight:
	// in flight → session → flows.
	var right []string
	if st.Session != "" {
		right = append(right, "session "+st.Session)
	}
	right = append(right, fmt.Sprintf("%d flows", st.FlowsTotal))
	if st.ActiveConns > 0 && w >= 120 {
		right = append(right, fmt.Sprintf("%d in flight", st.ActiveConns))
	}
	var rs string
	gap := 0
	for len(right) > 0 {
		rs = t.muted().Render(strings.Join(right, " · "))
		gap = w - ansi.StringWidth(left) - ansi.StringWidth(rs) - 2
		if gap >= 1 {
			break
		}
		switch {
		case len(right) == 3:
			right = right[:2]
		case len(right) == 2 && st.Session != "":
			right = right[1:]
		default:
			right = nil
		}
	}
	if len(right) == 0 {
		rs = ""
		gap = max(0, w-ansi.StringWidth(left)-1)
	}
	row := " " + left + strings.Repeat(" ", gap) + rs
	return t.raised(line(row, w))
}

// ---- footer ----

type hint struct{ key, label string }

func (m *Model) renderFooter(w int) string {
	t := m.theme()
	var hints []hint
	switch m.mode {
	case modeDetail:
		switch m.tab {
		case tabExplain:
			hints = []hint{{"1/2/3", "summary/request/response"}, {"⇥", "next tab"}, {"‹", "back"}, {"J/K", "next/prev flow"}, {"?", "more"}}
		case tabRequest, tabResponse:
			hints = []hint{{"v", "view"}, {"H", "headers"}, {"/", "path"}, {"S", "reveal"}, {"⇥", "tabs"}, {"‹", "back"}, {"x", "explain"}, {"?", "more"}}
		default:
			hints = []hint{{"⇥", "tabs"}, {"‹", "back"}, {"o", "options"}, {"2/3", "request/response"}, {"x", "explain"}, {"R", "replay"}, {"m", "mark"}, {"d", "diff"}, {"?", "more"}}
			if r, ok := m.selected(); ok && r.Kind == flow.KindTunnel {
				hints = []hint{{"n", "never decrypt " + r.Host}, {"‹", "back"}, {"D", "decrypt"}, {"?", "more"}}
			}
		}
	case modeRules:
		hints = []hint{{"⏎", "toggle"}, {"x", "remove"}, {"⇥", "held"}, {"esc", "close"}}
	case modeHeld:
		hints = []hint{{"⏎", "resume"}, {"x", "drop"}, {"⇥", "decrypt"}, {"esc", "close"}}
	case modeActions:
		hints = []hint{{"⏎", "run"}, {"j/k", "move"}, {"o", "only"}, {"n", "never"}, {"/", "host filter"}, {"esc", "close"}}
	case modeQuit:
		hints = []hint{{"q", "turn pano off"}, {"b", "keep in background"}, {"⏎", "highlighted"}, {"esc", "stay"}}
	case modeSimulator:
		hints = []hint{{"i", "install & restart"}, {"esc", "later"}, {"x", "don't ask again"}}
	case modeDecrypt:
		hints = []hint{{"1/2/3", "all/only/off"}, {"⏎", "→ only"}, {"n", "→ never"}, {"x", "remove"}, {"+", "add host"}, {"⇥", "mobile"}, {"esc", "close"}}
	case modeMobile:
		if m.status.Mobile.Enabled {
			hints = []hint{{"⏎", "close to the network"}, {"⇥", "rules"}, {"esc", "close"}}
		} else {
			hints = []hint{{"⏎", "open to phones on your Wi-Fi"}, {"⇥", "rules"}, {"esc", "close"}}
		}
	case modeFilter:
		hints = []hint{{"⏎", "apply"}, {"esc", "cancel"}}
	case modeHelp:
		hints = []hint{{"esc", "close"}}
	default:
		hints = []hint{{"j/k", "move"}, {"⏎", "open"}, {"o", "options"}, {"/", "filter"}, {"x", "explain"}, {"m", "mark"}, {"d", "diff"}, {"R", "replay"}, {"X", "clear"}, {"r", "rules"}, {"h", "held"}, {"D", "decrypt"}, {"M", "mobile"}, {"space", "pause"}, {"?", "help"}, {"q", "quit"}}
		if r, ok := m.selected(); ok && r.Kind == flow.KindTunnel {
			hints = append([]hint{{"n", "never decrypt " + r.Host}}, hints...)
		}
	}
	var b strings.Builder
	for i, h := range hints {
		s := t.accent().Render(h.key) + " " + t.muted().Render(h.label)
		if ansi.StringWidth(b.String())+ansi.StringWidth(s)+3 > w-24 && i > 3 {
			break
		}
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(s)
	}
	left := " " + b.String()
	var right string
	switch {
	case m.toast != "" && time.Since(m.toastAt) < 2500*time.Millisecond:
		st := t.accent()
		if strings.HasPrefix(m.toast, "✗") {
			st = t.fg(t.Err)
		}
		right = st.Render(m.toast)
	case m.mode == modeList || m.mode == modeDetail:
		if m.filterRaw != "" {
			right = t.muted().Render(fmt.Sprintf("%d of %d", len(m.visible), len(m.table.rows)))
		} else if len(m.visible) > 0 {
			right = t.muted().Render(fmt.Sprintf("%d/%d", m.cursor+1, len(m.visible)))
		}
		if !m.follow && m.unseen > 0 && m.mode == modeList {
			right = t.accent().Render(fmt.Sprintf("%s %d new · g to jump", glyphUp, m.unseen)) + "  " + right
		}
	}
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right) - 1
	if gap < 1 {
		right = ""
		gap = 0
	}
	return t.raised(line(left+strings.Repeat(" ", gap)+right, w))
}

// ---- filter bar ----

func (m *Model) renderFilterBar(w int) string {
	t := m.theme()
	if m.mode == modeFilter {
		prompt := t.accent().Render(" / ")
		if m.prevMode == modeDetail {
			prompt = t.accent().Render(" $ ")
		}
		return line(prompt+m.input.View(), w)
	}
	if m.filterRaw == "" {
		return ""
	}
	var chips []string
	for _, tok := range strings.Fields(m.filterRaw) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			k, v, ok = strings.Cut(tok, ":")
		}
		if ok {
			chips = append(chips, t.faint().Render(k+":")+t.primary().Render(v))
		} else {
			chips = append(chips, t.faint().Render("q:")+t.primary().Render(tok))
		}
	}
	left := " " + t.accent().Render("/") + " " + strings.Join(chips, "  ")
	right := t.muted().Render(fmt.Sprintf("%d of %d", len(m.visible), len(m.table.rows))) + t.faint().Render("  esc clears")
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right) - 1
	if gap < 1 {
		right = ""
		gap = 0
	}
	return line(left+strings.Repeat(" ", gap)+right, w)
}

// ---- list ----

type columns struct {
	wide                                          bool
	id, time, meth, host, path, st, dur, up, down int
	typ, flags                                    int
}

func (m *Model) columns(w int) columns {
	c := columns{time: 8, meth: 4, st: 3, dur: 6}
	// gutter(1) + separators
	if w < 104 {
		c.flags = 4
		if w >= 70 {
			c.id = 5
		}
		c.path = w - 1 - c.time - 1 - c.meth - 1 - 1 - c.st - 1 - c.dur - 1 - c.flags
		if c.id > 0 {
			c.path -= c.id + 1
		}
		return c
	}
	c.wide = true
	c.meth = 6
	c.id, c.host, c.st, c.up, c.down, c.typ = 5, 22, 5, 6, 7, 5
	c.flags = 18
	if w >= 140 {
		c.host, c.typ = 26, 6
	}
	if w >= 200 {
		c.host, c.flags = 30, 22
	}
	fixed := 1 + c.id + 1 + c.time + 1 + c.meth + 1 + c.host + 1 + 1 + c.st + 1 + c.dur + 1 + c.up + 1 + c.down + 1 + c.typ + 1 + c.flags
	c.path = w - fixed
	if c.path < 16 {
		c.flags = max(4, c.flags-(16-c.path))
		fixed = 1 + c.id + 1 + c.time + 1 + c.meth + 1 + c.host + 1 + 1 + c.st + 1 + c.dur + 1 + c.up + 1 + c.down + 1 + c.typ + 1 + c.flags
		c.path = w - fixed
	}
	return c
}

func (m *Model) renderColumnHeader(w int, c columns) string {
	t := m.theme()
	f := t.faint()
	var b strings.Builder
	b.WriteString(" ")
	if c.id > 0 {
		b.WriteString(padR(f.Render("ID"), c.id) + " ")
	}
	b.WriteString(padR(f.Render("TIME"), c.time) + " ")
	b.WriteString(padR(f.Render("METH"), c.meth) + " ")
	if c.wide {
		b.WriteString(padL(f.Render("HOST"), c.host) + " ")
		b.WriteString(padR(f.Render("PATH"), c.path) + " ")
		b.WriteString(padL(f.Render("ST"), c.st) + " ")
		b.WriteString(padL(f.Render("DUR"), c.dur) + " ")
		b.WriteString(padL(f.Render(glyphUp+"UP"), c.up) + " ")
		b.WriteString(padL(f.Render(glyphDown+"DOWN"), c.down) + " ")
		b.WriteString(padR(f.Render("TYPE"), c.typ) + " ")
		b.WriteString(f.Render("FLAGS"))
	} else {
		b.WriteString(padR(f.Render("HOST/PATH"), c.path) + " ")
		b.WriteString(padL(f.Render("ST"), c.st) + " ")
		b.WriteString(padL(f.Render("DUR"), c.dur) + " ")
		b.WriteString(f.Render("⚑"))
	}
	return line(b.String(), w)
}

func (m *Model) flagGlyphs(r api.FlowRow, wordy bool) string {
	t := m.theme()
	var out []string
	add := func(g string, c lipgloss.Style, word string) {
		if wordy {
			out = append(out, c.Render(g)+" "+t.muted().Render(word))
		} else {
			out = append(out, c.Render(g))
		}
	}
	seen := map[string]bool{}
	if isRemoteClient(r.Client) {
		add(glyphMobile, t.secondary(), "remote")
	}
	for _, f := range r.Flags {
		if seen[f] {
			continue
		}
		seen[f] = true
		switch f {
		case "llm":
			add(glyphLLM, t.fg(t.LLM), "llm")
		case "stream":
			if r.State == flow.StateActive {
				add(glyphLive, t.accent(), "live")
			} else {
				add(glyphStream, t.accent(), "stream")
			}
		case "err":
			// status colour already says it; only mark transport errors
			if r.Error != "" && !wordy {
				add(glyphBad, t.fg(t.Err), "err")
			}
		case "held":
			// shown in the status cell
		case "mock":
			add(glyphMock, t.fg(t.Mock), "mock")
		case "replay":
			add(glyphReplay, t.secondary(), "replay")
		case "block":
			add(glyphBlocked, t.fg(t.Err), "block")
		case "delay", "throttle":
			g := glyphDelay
			if f == "throttle" {
				g = glyphThrottle
			}
			add(g, t.fg(t.Warn), f)
		case "rewrite_body", "set_header", "remove_header", "set_query", "redirect":
			add(glyphRewrite, t.secondary(), f)
		case "never", "unlisted", "off":
			add(glyphProxy, t.muted(), f)
		case "active", "trunc", "tag":
		default:
			add(glyphTag, t.secondary(), f)
		}
	}
	return strings.Join(out, " ")
}

// isRemoteClient reports whether a flow came from another device (phone,
// tablet) rather than this machine.
func isRemoteClient(addr string) bool {
	return addr != "" && store.MatchClient("remote", addr)
}

func isStatic(r api.FlowRow) bool {
	switch r.Type {
	case "js", "css", "img", "font", "media":
		return true
	}
	return false
}

func (m *Model) renderRow(r api.FlowRow, c columns, w int, selected, focused bool) string {
	t := m.theme()
	static := isStatic(r)
	dim := static && !selected
	base := t.primary()
	sec := t.secondary()
	mut := t.muted()
	if dim {
		base, sec, mut = t.muted(), t.muted(), t.faint()
	}
	// gutter
	gutter := " "
	switch {
	case selected && focused:
		gutter = t.accent().Render(glyphCursor)
	case selected:
		gutter = t.secondary().Render(glyphCursor)
	case r.ID == m.marked:
		gutter = t.secondary().Render(glyphMarked)
	}
	// method
	meth := r.Method
	switch r.Kind {
	case flow.KindTunnel:
		meth = "TUN"
	case flow.KindWebSocket:
		meth = "WS"
	case flow.KindHTTP:
	}
	if !c.wide {
		switch meth {
		case "DELETE":
			meth = "DEL"
		case "OPTIONS":
			meth = "OPTS"
		case "PATCH":
			meth = "PTCH"
		}
	}
	methStyle := sec
	switch meth {
	case "POST", "PUT", "PATCH", "DELETE":
		if !dim {
			methStyle = t.fg(t.Warn)
		}
	case "WS":
		methStyle = t.accent()
	}
	// status cell
	var stCell string
	switch {
	case r.State == flow.StateHeld:
		stCell = t.heldChip().Render(glyphHeld + " HELD")
	case r.State == flow.StateActive:
		stCell = t.accent().Render(spinnerFrames[m.frame%len(spinnerFrames)])
	case r.Error != "" && r.Status == 0:
		stCell = t.fg(t.Err).Render(glyphBad)
	case r.Error != "":
		stCell = t.fg(t.Err).Render(fmt.Sprint(r.Status))
	case r.Kind == flow.KindTunnel:
		stCell = mut.Render("–")
	case r.Status == 0:
		stCell = mut.Render("–")
	default:
		st := t.fg(t.status(r.Status, ""))
		if dim {
			st = mut
		}
		if hasFlag(r.Flags, "mock") && !dim {
			st = t.fg(t.Mock)
		}
		stCell = st.Render(fmt.Sprint(r.Status))
	}
	// duration + bytes with gradients
	durStyle := mut
	if !dim && r.DurMS > 0 {
		durStyle = t.fg(t.gradAt(durPos(r.DurMS)))
	}
	dur := r.Duration
	if r.State == flow.StateActive && !m.now.IsZero() {
		dur = store.FormatDuration(m.now.Sub(r.Time))
	}
	if r.State == flow.StateHeld {
		dur = "—"
	}
	downStyle := mut
	if !dim && r.Down > 0 {
		downStyle = t.fg(t.gradAt(bytesPos(r.Down)))
	}
	timeStr := r.Time.Local().Format("15:04:05")

	var b strings.Builder
	b.WriteString(gutter)
	if c.wide {
		b.WriteString(padR(mut.Render(r.Short), c.id) + " ")
		b.WriteString(mut.Render(timeStr) + " ")
		b.WriteString(padR(methStyle.Render(meth), c.meth) + " ")
		host := hostOnly(r.Host)
		b.WriteString(padL(sec.Render(fitLeft(host, c.host)), c.host) + " ")
		if r.Error != "" && r.Status == 0 {
			msg := fit(r.Path+"  "+glyphBad+" "+r.Error, c.path+1+c.st+1+c.dur+1+c.up+1+c.down+1+c.typ+1+c.flags)
			b.WriteString(t.fg(t.Err).Render(msg))
			return m.rowBG(line(b.String(), w), r, selected, focused)
		}
		b.WriteString(padR(base.Render(fit(r.Path, c.path)), c.path) + " ")
		b.WriteString(padL(stCell, c.st) + " ")
		b.WriteString(padL(durStyle.Render(dur), c.dur) + " ")
		b.WriteString(padL(mut.Render(bytesOrDash(r.Up)), c.up) + " ")
		down := bytesOrDash(r.Down)
		if r.State == flow.StateActive && r.Down > 0 {
			down = glyphLive + " " + store.FormatBytes(r.Down)
		}
		b.WriteString(padL(downStyle.Render(down), c.down) + " ")
		typ := r.Type
		if typ == "tunnel" {
			typ = "tun"
		}
		b.WriteString(padR(m.typeStyle(typ, dim).Render(typ), c.typ) + " ")
		b.WriteString(fit(m.flagGlyphs(r, c.flags >= 14), c.flags))
	} else {
		if c.id > 0 {
			b.WriteString(padR(mut.Render(r.Short), c.id) + " ")
		}
		b.WriteString(mut.Render(timeStr) + " ")
		b.WriteString(padR(methStyle.Render(meth), c.meth) + " ")
		if r.Error != "" && r.Status == 0 {
			hp := fit(hostOnly(r.Host)+r.Path, c.path)
			b.WriteString(padR(sec.Render(hp), c.path) + " ")
			b.WriteString(t.fg(t.Err).Render(fit(glyphBad+" "+r.Error, c.st+1+c.dur+1+c.flags)))
			return m.rowBG(line(b.String(), w), r, selected, focused)
		}
		hp := hostOnly(r.Host)
		hpw := c.path
		hostW := ansi.StringWidth(hp)
		pathPart := r.Path
		if hostW+ansi.StringWidth(pathPart) > hpw {
			if hostW > hpw/2 {
				hp = fitLeft(hp, hpw/2)
				hostW = ansi.StringWidth(hp)
			}
			pathPart = fit(pathPart, hpw-hostW)
		}
		b.WriteString(padR(sec.Render(hp)+base.Render(pathPart), c.path) + " ")
		b.WriteString(padL(stCell, c.st) + " ")
		b.WriteString(padL(durStyle.Render(dur), c.dur) + " ")
		b.WriteString(fit(m.flagGlyphs(r, false), c.flags))
	}
	return m.rowBG(line(b.String(), w), r, selected, focused)
}

func (m *Model) typeStyle(typ string, dim bool) lipgloss.Style {
	t := m.theme()
	if dim {
		return t.faint()
	}
	switch typ {
	case "json":
		return t.primary()
	case "sse", "ws":
		return t.accent()
	case "html", "text", "xml", "form":
		return t.secondary()
	default:
		return t.muted()
	}
}

func bytesOrDash(n int64) string {
	if n == 0 {
		return "–"
	}
	return store.FormatBytes(n)
}

// rowBG applies selection / fresh-arrival backgrounds.
func (m *Model) rowBG(s string, r api.FlowRow, selected, focused bool) string {
	t := m.theme()
	switch {
	case selected && focused:
		return t.selected(s)
	case selected:
		return t.raised(s)
	}
	if at, ok := m.table.fresh[r.ID]; ok {
		age := m.now.Sub(at)
		if age >= 0 && age < 350*time.Millisecond {
			return t.paint(t.AccentDim, s)
		}
		if age > time.Second {
			delete(m.table.fresh, r.ID)
		}
	}
	return s
}

func (m *Model) renderList(w, h int, focused bool) []string {
	t := m.theme()
	c := m.columns(w)
	var out []string
	showHdr := m.height > 24
	if showHdr {
		out = append(out, m.renderColumnHeader(w, c))
		h--
	}
	if len(m.held) > 0 && m.mode != modeHeld {
		hd := m.held[0]
		s := " " + t.heldChip().Render(fmt.Sprintf(" %s %d held ", glyphHeld, len(m.held))) + "  " +
			t.secondary().Render(hd.Short) + " " + t.fg(t.Warn).Render(hd.Method) + " " + t.primary().Render(fit(hd.URL, max(10, w-60))) +
			"   " + t.accent().Render("h") + t.muted().Render(" open")
		out = append(out, t.raised(line(s, w)))
		h--
	}
	if m.paused && m.mode == modeList {
		out = append(out, t.raised(line(" "+t.fg(t.Warn).Render(glyphOff+" list paused")+t.muted().Render(" · new flows are still captured · ")+t.accent().Render("space")+t.muted().Render(" resume"), w)))
		h--
	}
	if len(m.visible) == 0 {
		out = append(out, m.renderEmpty(w, h)...)
		return out
	}
	if m.offset > len(m.visible)-1 {
		m.offset = max(0, len(m.visible)-1)
	}
	for i := m.offset; i < len(m.visible) && len(out) < h+boolToInt(showHdr)+boolToInt(len(m.held) > 0 && m.mode != modeHeld)+boolToInt(m.paused && m.mode == modeList); i++ {
		r := m.table.rows[m.visible[i]]
		out = append(out, m.renderRow(r, c, w, i == m.cursor, focused))
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (m *Model) renderEmpty(w, h int) []string {
	t := m.theme()
	st := m.status
	var lines []string
	if m.filterRaw != "" {
		lines = []string{t.muted().Render("◉  no flows match"), t.faint().Render("esc clears the filter")}
	} else {
		lines = append(lines, t.muted().Render("◉  nothing captured yet"))
		if !st.SystemProxy.Enabled {
			lines = append(lines, t.faint().Render(glyphProxy+" proxy off  →  run  ")+t.secondary().Render("pano on")+t.faint().Render("   or   ")+t.secondary().Render("pano run -- <cmd>"))
		}
		ca := t.fg(t.OK).Render(glyphOK + " ca trusted")
		if !st.CA.Trusted {
			ca = t.fg(t.Err).Render(glyphBad+" ca untrusted  →  ") + t.secondary().Render("pano ca install")
		}
		lines = append(lines, ca+t.faint().Render("     listening "+st.ProxyAddr))
	}
	if h >= 8 {
		lines = append(append(m.renderMascot(), ""), lines...)
	}
	top := max(0, (h-len(lines))/2-1)
	var out []string
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	for _, l := range lines {
		pad := max(0, (w-ansi.StringWidth(l))/2)
		out = append(out, strings.Repeat(" ", pad)+l)
	}
	return out
}
