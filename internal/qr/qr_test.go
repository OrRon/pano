package qr

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRows(t *testing.T) {
	rows := Rows("http://192.168.1.23:9091")
	if len(rows) < 12 {
		t.Fatalf("too few rows: %d", len(rows))
	}
	w := ansi.StringWidth(rows[0])
	for _, l := range rows {
		if ansi.StringWidth(l) != w {
			t.Fatalf("ragged row %q", l)
		}
	}
	if w < 2*len(rows)-2 || w > 2*len(rows)+2 {
		t.Fatalf("not square: %d cols × %d rows", w, len(rows))
	}
	if strings.Trim(rows[0], "█") != "" {
		t.Fatalf("no quiet zone: %q", rows[0])
	}
	if Rows(strings.Repeat("x", 5000)) != nil {
		t.Fatal("oversize input should give nil")
	}
}
