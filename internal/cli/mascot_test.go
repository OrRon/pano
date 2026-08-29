package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestMascotStaticWithoutColor(t *testing.T) {
	var out bytes.Buffer
	a := &App{out: &out, color: false}
	a.mascotWake([3]string{"", "on", "hint"})
	got := out.String()
	if strings.Count(got, "\n") != 3 {
		t.Fatalf("expected exactly one 3-row frame, got %q", got)
	}
	if strings.Contains(got, "\033") {
		t.Fatalf("no escape codes expected without colour: %q", got)
	}
	for _, want := range []string{"╭─────╮", "│ ◉ ◉ │  on", "╰─────╯  hint"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestMascotFrameClampsToTerminalWidth(t *testing.T) {
	a := &App{color: false, frameWidth: 40}
	long := strings.Repeat("6 services (Chromatic - Player 01, Ethernet) ", 4)
	f := a.mascotFrame(eyesOpen, [3]string{"", "✓ system proxy ON → 127.0.0.1:9091  " + long, ""})
	lines := strings.Split(strings.TrimSuffix(f, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("frame must be exactly 3 rows, got %d", len(lines))
	}
	for _, l := range lines {
		if n := len([]rune(l)); n > 39 {
			t.Fatalf("row wider than terminal (%d cells): %q", n, l)
		}
	}
	if !strings.HasSuffix(lines[1], "…") {
		t.Fatalf("long row should be truncated with an ellipsis: %q", lines[1])
	}
}

func TestMascotFrameWidthIsStable(t *testing.T) {
	a := &App{color: false}
	for _, eyes := range wakeFrames {
		if len([]rune(eyes)) != 5 {
			t.Fatalf("eyes %q must be 5 cells", eyes)
		}
	}
	f := a.mascotFrame(eyesShut, [3]string{})
	if !strings.Contains(f, "│ ─ ─ │") {
		t.Fatalf("shut eyes: %q", f)
	}
}
