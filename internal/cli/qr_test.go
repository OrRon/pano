package cli

import (
	"strings"
	"testing"
)

func TestQRLines(t *testing.T) {
	plain := qrLines("http://192.168.1.23:9091", false)
	if len(plain) < 12 || strings.ContainsRune(plain[0], '\033') {
		t.Fatalf("plain rows: %d %q", len(plain), plain[0])
	}
	color := qrLines("x", true)
	if !strings.Contains(color[0], "\033[38;2;255;255;255m") || !strings.HasSuffix(color[0], reset) {
		t.Fatal("colour rows must set explicit fg/bg and reset")
	}
	if qrLines(strings.Repeat("x", 5000), false) != nil {
		t.Fatal("oversize input should give nil")
	}
}
