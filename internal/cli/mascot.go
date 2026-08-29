package cli

import (
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

// The mascot is pano's logo — a rounded rectangle — given a pair of eyes:
// Panoptes, the watcher. `pano on` wakes it up (a short glance around, a
// blink, eyes wide open) and `pano off` puts it to sleep. The same character
// lives in the `pano ui` header, where its eyes track the daemon's state.
//
// The animation only runs on a colour TTY; piped, --json, --quiet and
// NO_COLOR output get a single static frame with no delay, so scripts never
// wait on it.

const (
	eyesOpen  = " • • "
	eyesWide  = " ◉ ◉ "
	eyesShut  = " ─ ─ "
	eyesLeft  = "• •  "
	eyesRight = "  • •"
)

// wakeFrames is the `pano on` sequence at mascotFrameDelay per frame.
var wakeFrames = []string{eyesOpen, eyesLeft, eyesLeft, eyesOpen, eyesRight, eyesRight, eyesOpen, eyesShut, eyesWide}

const mascotFrameDelay = 90 * time.Millisecond

// termWidth is the width of stdout in cells, or 0 when it is not a terminal.
func termWidth() int {
	if !isTTY(os.Stdout) {
		return 0
	}
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 0
	}
	return int(ws.Col)
}

// mascotFrame renders the 3-row box with the given eyes and a line of text to
// the right of each row. Every row is clamped to the terminal width: the
// animation rewinds exactly three rows between frames, so a wrapped row
// would leave a trail of boxes down the screen.
func (a *App) mascotFrame(eyes string, text [3]string) string {
	top, mid, bot := a.brand()
	rows := [3]string{
		top + "╭─────╮" + reset,
		mid + "│" + reset + a.c(bold, eyes) + mid + "│" + reset,
		bot + "╰─────╯" + reset,
	}
	if !a.color {
		rows = [3]string{"╭─────╮", "│" + eyes + "│", "╰─────╯"}
	}
	width := a.frameWidth
	if width == 0 {
		width = termWidth()
	}
	var b strings.Builder
	for i, r := range rows {
		row := "  " + r
		if text[i] != "" {
			row += "  " + text[i]
		}
		if width > 0 {
			row = ansi.Truncate(row, width-1, "…")
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

// brand returns the SGR prefixes for the box's three rows: the logo gradient
// (violet → blue) on 24-bit terminals, plain magenta/blue elsewhere.
func (a *App) brand() (top, mid, bot string) {
	if ct := os.Getenv("COLORTERM"); ct == "truecolor" || ct == "24bit" {
		return "\033[38;2;196;76;230m", "\033[38;2;152;108;236m", "\033[38;2;108;141;242m"
	}
	return magenta, magenta, blue
}

// mascotPrint writes one static frame.
func (a *App) mascotPrint(eyes string, text [3]string) {
	a.printf("%s", a.mascotFrame(eyes, text))
}

// mascotWake plays the wake-up animation in place when stdout is a colour
// TTY, otherwise prints the final frame.
func (a *App) mascotWake(text [3]string) {
	if !a.color || a.quiet {
		a.mascotPrint(eyesWide, text)
		return
	}
	a.printf("\033[?25l") // hide cursor
	for i, eyes := range wakeFrames {
		if i > 0 {
			a.printf("\033[3A") // back up over the previous frame
			time.Sleep(mascotFrameDelay)
		}
		a.mascotPrint(eyes, text)
	}
	a.printf("\033[?25h")
}
