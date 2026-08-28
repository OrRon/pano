# ADR 0004: macOS-first, with OS seams from day one

Status: accepted (2026-08)

## Context

The author's machine and the first users are on macOS, where the two
operations that make pano "just work" — trusting a root certificate and
pointing the whole system at a proxy — have well-known automation paths:
`security add-trusted-cert` (which always shows a GUI admin prompt since Big
Sur; leaf certificates must carry a SAN, the `serverAuth` EKU and live at
most 825 days) and `networksetup -setwebproxy/-setsecurewebproxy` (which may
require administrator privileges depending on the account; `osascript … with
administrator privileges` is the fallback).

Linux desktops have several competing proxy settings (GNOME `gsettings`,
KDE, environment variables) and trust stores (`update-ca-certificates`,
`trust anchor`, per-app bundles); Windows uses the registry and `certutil`.
Supporting all of them well before the core is proven would slow everything
else down, but painting ourselves into a macOS-only corner would be worse.

## Decision

- Everything that is not OS-specific — proxy engine, CA minting, store,
  rules, views, explain, control API, CLI, MCP — is pure Go, `CGO_ENABLED=0`,
  and cross-compiles. CI builds and tests on both `macos-14` and
  `ubuntu-latest`; releases ship darwin and linux for arm64 and amd64.
  (Windows is not a target yet: `internal/cli` uses `Setsid` and
  `syscall.Exec`.)
- Exactly three OS-specific pieces exist, each behind an interface with build
  tags from the first commit:
  - `internal/sysproxy`: `darwin.go` drives `networksetup` with a snapshot
    file; `other.go` reports `Supported() == false` and an error that points
    at `pano run --` / `HTTP(S)_PROXY`.
  - `internal/ca`: `keychain_darwin.go` installs into the login (or, with
    `--system`, System) keychain; `keychain_other.go` prints manual steps for
    Debian/Ubuntu, Fedora/Arch and per-process use.
  - `internal/watchdog`: `wait_darwin.go` uses kqueue `EVFILT_PROC/NOTE_EXIT`;
    `wait_other.go` polls with signal 0 every 500 ms.
- `pano run -- <cmd>` and `pano env` — the per-process capture path — are
  fully functional on every OS and are the recommended path for
  agent-spawned subprocesses even on macOS.
- The README states the status plainly: pre-1.0, macOS primary, Linux builds
  and works via `pano run --`.

## Consequences

- A Linux user gets a working proxy, capture, rules and MCP today; they
  configure trust and proxying themselves.
- Adding GNOME `gsettings` or the Windows registry later is a new file behind
  an existing interface, not a refactor.
- Tests that exercise `networksetup` and `security` run only on the macOS
  runner; the darwin implementations are unit-tested against a fake command
  runner so the parsing and snapshot logic is covered everywhere.
- Documentation carries a permanent "Linux/Windows status" section
  ([faq.md](../faq.md)).
