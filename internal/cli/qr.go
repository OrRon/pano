package cli

import "github.com/orron/pano/internal/qr"

// qrLines renders text as a QR code for the terminal. With colour the rows
// are painted white on black explicitly so the code scans on light and dark
// terminals alike; without it the glyphs sit on the terminal's own colours.
func qrLines(text string, color bool) []string {
	rows := qr.Rows(text)
	if !color {
		return rows
	}
	const paint = "\033[38;2;255;255;255m\033[48;2;0;0;0m"
	for i, r := range rows {
		rows[i] = paint + r + reset
	}
	return rows
}
