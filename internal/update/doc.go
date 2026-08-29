// Package update tells the user when a newer pano release exists. It is
// notify-only by design (ADR 0010): at most once a day it asks GitHub's
// public releases endpoint for the latest tag, compares it with the running
// version and hands back a one-line hint — it never downloads or installs
// anything, so Homebrew (or go install) stays the only thing that changes
// the binary.
//
// The check is skipped for development builds, in CI, without a terminal,
// with --json, when PANO_NO_UPDATE_CHECK or DO_NOT_TRACK is set, when
// [updates] check = false in config.toml, or when the binary was built with
// Default set to "off". The request bypasses every proxy (including pano
// itself) and carries nothing but pano's version in its User-Agent.
package update
