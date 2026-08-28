// Package glob implements the tiny wildcard language pano uses for host and
// path matching: '*' matches any run of characters (including '.' and '/'),
// '?' matches one character, and matching is case-insensitive.
package glob

import "strings"

// Match reports whether s matches pattern.
func Match(pattern, s string) bool {
	return match(strings.ToLower(pattern), strings.ToLower(s))
}

func match(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			// Collapse consecutive stars.
			for len(p) > 0 && p[0] == '*' {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if match(p, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

// MatchAny reports whether s matches any pattern.
func MatchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if Match(p, s) {
			return true
		}
	}
	return false
}

// IsPattern reports whether p contains wildcards.
func IsPattern(p string) bool { return strings.ContainsAny(p, "*?") }
