package tui

import "github.com/orron/pano/internal/glob"

func globMatch(pattern, s string) bool { return glob.Match(pattern, s) }
