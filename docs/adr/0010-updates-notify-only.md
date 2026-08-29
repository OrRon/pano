# ADR 0010: Updates are notify-only — the package manager upgrades pano

**Status:** accepted (2026-08-30)

## Context

pano is in beta and will change often, so users need to learn about new
releases without watching the repository. Three mechanisms are common in
open-source CLIs, and the ecosystem has clear opinions about each:

- **Notify-only check** — ask the project's public releases endpoint at
  most once a day and print one line. `gh` is the reference: 24 h cache,
  plain GET to `api.github.com/repos/…/releases/latest`, skipped without a
  TTY, in CI and when `GH_NO_UPDATE_NOTIFIER` is set. Deno
  (`DENO_NO_UPDATE_CHECK`) and lazygit (`update.method: prompt | background |
  never`) do the same. Accepted practice when it is documented and easy to
  turn off.
- **Self-update on command** (`deno upgrade`, `rustup self update`) —
  tolerated, but Homebrew's acceptable-formulae policy says *"software that
  updates itself conflicts with Homebrew's version and upgrade management.
  Self-update behaviour must be disabled when this can be done"*, and the
  well-behaved tools refuse to overwrite a brew-managed binary.
- **Automatic self-install** — not accepted anywhere in the OSS toolchain;
  distributions patch it out.

Two pano-specific constraints sharpen this. Debian's privacy guidelines list
"version checks" as a form of phoning home and patch such behaviour out of
packages; a tool whose whole purpose is to decrypt its owner's traffic must
be beyond reproach on calling home. And pano *is* the Mac's system proxy
while it is on, so any request it makes for itself would otherwise loop
through pano and be recorded as a flow.

## Decision

1. **Homebrew (cask) and `go install` are the only things that change the
   binary.** There is no `pano upgrade`; the cask does not declare
   `auto_updates`, so `brew outdated` and `brew upgrade` stay authoritative.
2. **pano notifies, once a day, one line, and only to a person.** The check
   runs in the background when a command starts and prints after the
   command's own output — `↑ pano 0.3.0 is available (you have 0.2.0) · brew
   upgrade pano` — plus the release URL and how to disable it. The TUI shows
   the same as a header chip. The hint matches how the binary was installed
   (Caskroom → brew, Go bin dir → `go install`, otherwise the release page).
3. **It is off whenever a person is not reading:** no TTY on stdout or
   stderr, `--json`, `--quiet`, `CI` set, and never inside `pano mcp`
   (stdout is the protocol), `pano daemon`, the watchdog, `pano env` or shell
   completion.
4. **Four independent opt-outs**, all documented: `PANO_NO_UPDATE_CHECK=1`,
   the cross-tool `DO_NOT_TRACK=1`, `[updates] check = false` in
   `config.toml`, and the build-time `-X
   github.com/orron/pano/internal/update.Default=off` so distribution
   packagers do not need a patch. Development builds (`dev`, git-describe
   suffixes, `-dirty`) never check.
5. **The request is minimal and direct.** One GET to GitHub's public
   releases endpoint carrying pano's version in the `User-Agent` and nothing
   else; no identifiers, no OS or architecture. It uses a transport with
   `Proxy: nil` so it bypasses the system proxy — pano never records its own
   check — and times out after 3 s; a failure is silent.
6. **`pano version --check` is the explicit form**: it ignores the cache and
   the opt-outs because the user asked, and still only prints.

## Consequences

- SECURITY.md now says exactly what pano sends on its own: this request and
  nothing else. The old sentence "pano makes no network requests of its own"
  is replaced rather than quietly contradicted.
- Upgrading is `brew upgrade pano`; the cask's pre-uninstall hook runs
  `pano off` so a daemon never outlives its binary across an upgrade.
- A daily unauthenticated GitHub API call is far inside the 60/hour limit.
- If pano is ever packaged by a distribution, the packager sets `Default=off`
  and the notice disappears; `pano version --check` keeps working.
