// Package qr renders a QR code as rows of half-block characters — two
// modules per terminal row — for the terminal front ends (`pano mobile` and
// the TUI's Mobile drawer). Colour is the caller's business: Rows returns
// plain glyphs where "light" modules are █ / ▀ / ▄ and dark ones are spaces,
// so painting a row white-on-black gives a code phone cameras read on any
// terminal theme (they accept inverted codes too, which is what an unpainted
// row on a dark terminal is).
package qr

import (
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// Rows encodes text at medium error correction and returns one string per
// pair of module rows, including the library's 4-module quiet zone. nil when
// the text does not fit a QR code.
func Rows(text string) []string {
	q, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return nil
	}
	bm := q.Bitmap() // true = dark module
	if len(bm)%2 == 1 {
		bm = append(bm, make([]bool, len(bm[0]))) // pad to an even row count (light)
	}
	out := make([]string, 0, len(bm)/2)
	for y := 0; y < len(bm); y += 2 {
		var b strings.Builder
		for x := range bm[y] {
			top, bot := !bm[y][x], !bm[y+1][x] // true = light
			switch {
			case top && bot:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bot:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		out = append(out, b.String())
	}
	return out
}
