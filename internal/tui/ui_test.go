package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/orron/pano/internal/flow"
)

// The cursor row and the bars must be painted edge to edge: every SGR reset
// emitted by a nested style has to be followed by the background again.
func TestPaintSurvivesNestedResets(t *testing.T) {
	th := newTheme(true)
	inner := th.muted().Render("abc") + " " + th.fg(th.OK).Render("200") + " tail"
	got := th.selected(inner)
	bg := ansi.Style{}.BackgroundColor(th.BgSelected).String()
	if !strings.HasPrefix(got, bg) {
		t.Fatalf("missing leading background: %q", got)
	}
	// Strip the final reset, then every remaining reset must re-assert bg.
	body := strings.TrimSuffix(got, ansi.ResetStyle)
	if n := strings.Count(body, ansi.ResetStyle); n != strings.Count(body, ansi.ResetStyle+bg) {
		t.Fatalf("background lost after a nested reset: %q", got)
	}
	if ansi.Strip(got) != "abc 200 tail" {
		t.Fatalf("text changed: %q", ansi.Strip(got))
	}
}

func TestSelectedRowIsPaintedToTheEdge(t *testing.T) {
	m := sampleModel(120, 40)
	m.mode = modeList
	m.table.fresh = map[flow.ID]time.Time{}
	lines := strings.Split(m.View().Content, "\n")
	// Row 0 of the list is the cursor row (after header, column header, held bar).
	row := lines[3]
	bg := ansi.Style{}.BackgroundColor(m.theme().BgSelected).String()
	if !strings.Contains(row, "19a6") || !strings.HasPrefix(row, bg) {
		t.Fatalf("cursor row not painted: %q", row)
	}
	if strings.Contains(strings.TrimSuffix(row, ansi.ResetStyle), ansi.ResetStyle+" ") {
		t.Fatalf("background gap in cursor row: %q", row)
	}
}

func TestEscClosesDetail(t *testing.T) {
	m := sampleModel(200, 60)
	m.mode = modeDetail
	if !strings.Contains(ansi.Strip(m.View().Content), "1 Summary") {
		t.Fatal("detail pane expected before esc")
	}
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: tea.KeyLeft}, {Code: 'q', Text: "q"}} {
		m.mode, m.detailID = modeDetail, 1317
		mm, _ := m.Update(key)
		got := mm.(*Model)
		if got.mode != modeList || got.detailID != 0 {
			t.Fatalf("%s: mode=%v detailID=%v", key, got.mode, got.detailID)
		}
		if strings.Contains(ansi.Strip(got.View().Content), "1 Summary") {
			t.Fatalf("%s: detail pane still rendered", key)
		}
	}
}

func TestMascotMoods(t *testing.T) {
	m := sampleModel(120, 40)
	m.table.fresh = map[flow.ID]time.Time{}
	if eyes(m.mood(), 0) != " • • " {
		t.Fatalf("frame 0 while capturing should be centred open eyes, got %q", eyes(m.mood(), 0))
	}
	if eyes(moodWatch, 35) != " ─ ─ " || eyes(moodWatch, 13) != "• •  " || eyes(moodWatch, 27) != "  • •" {
		t.Fatal("watch cycle: blink/glance frames")
	}
	m.paused = true
	if m.mood() != moodSleep {
		t.Fatal("paused → sleep")
	}
	m.paused = false
	m.err = errors.New("dial: no such file")
	if m.mood() != moodDead {
		t.Fatal("unreachable → dead")
	}
	for md := moodWatch; md <= moodDead; md++ {
		for f := 0; f < 40; f++ {
			if w := ansi.StringWidth(eyes(md, f)); w != mascotInner {
				t.Fatalf("mood %d frame %d: width %d", md, f, w)
			}
		}
	}
}
