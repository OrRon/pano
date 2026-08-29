package tui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Theme is the single style registry for the UI ("Panoptes" palette). Every
// component reads colours from here; nothing hardcodes hex values elsewhere.
type Theme struct {
	dark bool

	BgRaised, BgSelected, BgOverlay, LineFaint color.Color
	FgPrimary, FgSecondary, FgMuted, FgFaint   color.Color
	Accent, AccentDim                          color.Color
	OK, Redirect, Warn, Err, LLM, Mock         color.Color
	SynStr, SynNum, SynBool                    color.Color // body syntax: strings / numbers / bool+null
	HeldFg, HeldBg                             color.Color
	BrandA, BrandB                             color.Color // logo gradient (top → bottom); mascot only

	grad  []color.Color // duration/bytes gradient
	brand []color.Color // 3-step BrandA → BrandB
}

func newTheme(dark bool) *Theme {
	ld := lipgloss.LightDark(dark)
	c := func(d, l string) color.Color { return ld(lipgloss.Color(l), lipgloss.Color(d)) }
	t := &Theme{
		dark:        dark,
		BgRaised:    c("#1E1E26", "#F3F3F7"),
		BgSelected:  c("#2B2B38", "#E6E7EF"),
		BgOverlay:   c("#22222C", "#FAFAFC"),
		LineFaint:   c("#33333F", "#DADAE3"),
		FgPrimary:   c("#E7E5EF", "#1D1D26"),
		FgSecondary: c("#A9A6B6", "#4E4D5A"),
		FgMuted:     c("#6F6D7B", "#8A8896"),
		FgFaint:     c("#4A4955", "#B9B8C3"),
		Accent:      c("#38C8E8", "#0B8DB0"),
		AccentDim:   c("#1F6B7A", "#BFE7F0"),
		OK:          c("#3FCF8E", "#178A58"),
		Redirect:    c("#7FA1C9", "#4A6A99"),
		Warn:        c("#F2B544", "#A86B00"),
		Err:         c("#F0605A", "#C93C36"),
		LLM:         c("#B692FF", "#6E42D6"),
		Mock:        c("#F27DB4", "#C23D86"),
		SynStr:      c("#9ECE9A", "#2F7A45"),
		SynNum:      c("#E6A96B", "#9C5A10"),
		SynBool:     c("#8FB8E8", "#3B6FB0"),
		HeldFg:      c("#15151A", "#FFFFFF"),
		HeldBg:      c("#38C8E8", "#0B8DB0"),
		BrandA:      c("#C44CE6", "#A82ED0"),
		BrandB:      c("#6C8DF2", "#3F63D8"),
	}
	t.grad = lipgloss.Blend1D(24, t.FgMuted, t.FgPrimary, t.Warn, t.Err)
	t.brand = lipgloss.Blend1D(3, t.BrandA, t.BrandB)
	return t
}

// Style helpers.
func (t *Theme) fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
func (t *Theme) primary() lipgloss.Style         { return t.fg(t.FgPrimary) }
func (t *Theme) secondary() lipgloss.Style       { return t.fg(t.FgSecondary) }
func (t *Theme) muted() lipgloss.Style           { return t.fg(t.FgMuted) }
func (t *Theme) faint() lipgloss.Style           { return t.fg(t.FgFaint) }
func (t *Theme) accent() lipgloss.Style          { return t.fg(t.Accent) }

// paint fills a whole rendered line with bg. Nested lipgloss styles end
// every segment with an SGR reset, which would otherwise switch the
// background off after the first styled cell — the bar/row would only be
// highlighted up to its first coloured word. The background is re-asserted
// after each reset so the fill runs edge to edge.
func (t *Theme) paint(bg color.Color, s string) string {
	seq := ansi.Style{}.BackgroundColor(bg).String()
	s = strings.ReplaceAll(s, ansi.ResetStyle, ansi.ResetStyle+seq)
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+seq)
	return seq + s + ansi.ResetStyle
}

// raised paints a header/footer/title bar.
func (t *Theme) raised(s string) string { return t.paint(t.BgRaised, s) }

// selected paints the cursor row of the focused pane.
func (t *Theme) selected(s string) string { return t.paint(t.BgSelected, s) }

func (t *Theme) heldChip() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.HeldFg).Background(t.HeldBg).Bold(true)
}

// status returns the colour for an HTTP status class.
func (t *Theme) status(code int, errText string) color.Color {
	switch {
	case errText != "":
		return t.Err
	case code >= 500:
		return t.Err
	case code >= 400:
		return t.Warn
	case code >= 300:
		return t.Redirect
	case code >= 200:
		return t.OK
	}
	return t.FgMuted
}

// gradAt maps a 0..1 position onto the duration/bytes gradient.
func (t *Theme) gradAt(x float64) color.Color {
	if x < 0 {
		x = 0
	}
	if x > 1 {
		x = 1
	}
	i := int(math.Round(x * float64(len(t.grad)-1)))
	return t.grad[i]
}

// durPos maps a duration in ms to the gradient (50 ms → 0, 30 s → 1).
func durPos(ms float64) float64 {
	if ms <= 50 {
		return 0
	}
	return math.Log10(ms/50) / math.Log10(30000/50)
}

// bytesPos maps bytes to the gradient (1 KB → 0, 10 MB → 1).
func bytesPos(n int64) float64 {
	if n <= 1000 {
		return 0
	}
	return math.Log10(float64(n)/1000) / 4
}

// Glyphs (unicode tier; all single-cell).
const (
	glyphCursor   = "▌"
	glyphMarked   = "▍"
	glyphOn       = "●"
	glyphOff      = "○"
	glyphHalf     = "◐" // decrypt mode "only": some hosts
	glyphProxy    = "⇄"
	glyphOK       = "✓"
	glyphBad      = "✕"
	glyphLLM      = "◆"
	glyphStream   = "≈"
	glyphLive     = "▸"
	glyphHeld     = "‖"
	glyphMock     = "◇"
	glyphReplay   = "↻"
	glyphBlocked  = "⊘"
	glyphDelay    = "◔"
	glyphThrottle = "≋"
	glyphRewrite  = "✎"
	glyphTag      = "⚑"
	glyphUp       = "▲"
	glyphDown     = "▼"
	glyphEllipsis = "…"
	glyphSep      = "│"
	glyphRule     = "─"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
