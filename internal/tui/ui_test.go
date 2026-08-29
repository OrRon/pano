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

// TestDecryptDrawerKeys: D opens the Decrypt tab of the drawer, tab cycles
// rules → held → decrypt, and every list entry is on screen.
func TestDecryptDrawerKeys(t *testing.T) {
	m := sampleModel(120, 40)
	m.mode = modeList
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'D', Text: "D"})
	m = mm.(*Model)
	if m.mode != modeDecrypt {
		t.Fatalf("D should open the decrypt drawer, mode=%v", m.mode)
	}
	got := ansi.Strip(m.View().Content)
	for _, want := range append([]string{"1 all", "2 only", "3 off", "mmg.whatsapp.net", "×14", "REJECTED CERT"}, m.status.Decrypt.Never...) {
		if !strings.Contains(got, want) {
			t.Fatalf("drawer missing %q", want)
		}
	}
	if m.drawerLen() != len(m.status.Decrypt.Never)+len(m.status.Decrypt.Rejected) {
		t.Fatalf("drawerLen=%d", m.drawerLen())
	}
	m.mode = modeRules
	for _, want := range []mode{modeHeld, modeDecrypt, modeRules} {
		mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = mm.(*Model)
		if m.mode != want {
			t.Fatalf("tab cycle: got %v want %v", m.mode, want)
		}
	}
	// + opens the text input targeting the focused section (never here).
	m.mode, m.drawerIx = modeDecrypt, 0
	mm, _ = m.Update(tea.KeyPressMsg{Code: '+', Text: "+"})
	m = mm.(*Model)
	if m.mode != modeFilter || m.prevMode != modeDecrypt || m.decryptTarget != secNever {
		t.Fatalf("+ should open input for never: mode=%v target=%q", m.mode, m.decryptTarget)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if mm.(*Model).mode != modeDecrypt {
		t.Fatal("esc should return to the decrypt drawer")
	}
}

// TestHeaderNamesDecryptState: the header chip names the mode, the only
// hosts and the first rejected host — never just a count when there is room.
func TestHeaderNamesDecryptState(t *testing.T) {
	m := sampleModel(160, 40)
	m.mode = modeList
	hdr := ansi.Strip(strings.SplitN(m.View().Content, "\n", 2)[0])
	if !strings.Contains(hdr, "decrypt all") || !strings.Contains(hdr, "rejected mmg.whatsapp.net +1") {
		t.Fatalf("header: %q", hdr)
	}
	m.status.Decrypt.Mode, m.status.Decrypt.Only = "only", []string{"api.anthropic.com", "localhost"}
	hdr = ansi.Strip(strings.SplitN(m.View().Content, "\n", 2)[0])
	if !strings.Contains(hdr, "decrypt only api.anthropic.com localhost") {
		t.Fatalf("header should name the only hosts: %q", hdr)
	}
}

// TestActionsMenu: o opens the per-flow menu, its keys act on the selected
// host, and esc closes it back to where it came from.
func TestActionsMenu(t *testing.T) {
	m := sampleModel(120, 40)
	m.mode, m.cursor = modeList, 1 // api.openai.com
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m = mm.(*Model)
	if m.mode != modeActions {
		t.Fatalf("o should open actions, mode=%v", m.mode)
	}
	got := ansi.Strip(m.View().Content)
	for _, want := range []string{"ACTIONS", "decrypt only api.openai.com", "never decrypt api.openai.com", "filter host=api.openai.com", "replay", "decrypt all"} {
		if !strings.Contains(got, want) {
			t.Fatalf("menu missing %q", want)
		}
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = mm.(*Model)
	if m.mode != modeList || m.filterRaw != "host=api.openai.com" {
		t.Fatalf("/ should filter to the host: mode=%v filter=%q", m.mode, m.filterRaw)
	}
	m.mode, m.filterRaw = modeList, ""
	m.applyFilter()
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	mm, _ = mm.(*Model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if mm.(*Model).mode != modeList {
		t.Fatal("esc should close the menu")
	}
	// Tunnel rows get no replay/mark/explain entries.
	for i, ix := range m.visible {
		if m.table.rows[ix].Kind == flow.KindTunnel {
			m.cursor = i
			break
		}
	}
	r, _ := m.selected()
	if r.Kind != flow.KindTunnel {
		t.Fatal("no tunnel row in sample")
	}
	if acts := m.actionsFor(r); len(acts) != 3 {
		t.Fatalf("tunnel actions: %d", len(acts))
	}
}
