# ADR 0009: `pano on` is the app — closing its window turns pano off

**Status:** accepted (2026-08-30); amends [ADR 0006](0006-mcp-follows-the-daemon.md)

## Context

After ADR 0006 pano had a clear rule — it runs only between the user's
`pano on` and `pano off` — but reaching the UI took three commands
(`pano on`, `pano ui`, later `pano off`), and the two halves could drift:
a closed terminal left the daemon capturing and the Mac's proxy pointed at
it until the user remembered `pano off`. The owner's ask was to make pano
behave like an app: opening it shows the UI, quitting the UI turns it off,
and running it headless is the explicit exception rather than the default.

Two constraints shape the answer:

- **Quitting must be enforced by the daemon, not the UI's exit path.** A
  closed terminal window, an SSH drop or a `kill -9` never runs the UI's
  cleanup. The kqueue watchdog already covers a dying *daemon*; the new
  rule must cover a dying *UI* the same way, or the proxy is left pointing
  at a running daemon nobody is looking at.
- **`pano on` is run by things that are not a person.** Scripts, Makefiles
  and agents' shells run it (the MCP off-message literally tells the user
  to). A UI waiting for keys there would hang forever.

## Decision

- **`pano on` opens the UI** (daemon up → CA trusted → system proxy on →
  UI). The UI attaches to the daemon over a long-lived `GET /v1/attach?own=1`
  request. The daemon tracks attachments in a small `lifecycle` struct:
  a count of UIs and, at most, one *owner*. When the owner's request
  context ends — `q`, ctrl-c, a closed window, a kill — the daemon runs
  `pano off` on itself (restore the proxy snapshot, then shut down; the
  mobile listener closes with the proxy). `pano status` shows
  `lifecycle app — closing its window turns pano off`; `pano_status`
  says the same to agents.
- **`pano on -b` / `--background`** is the previous behaviour: banner, prompt
  back, daemon runs until `pano off`. The same happens automatically when
  stdin or stdout is not a terminal, or with `--json`, so no script or agent
  needs the flag.
- **`pano ui` attaches** (`own=0`) to a running daemon; leaving it never
  stops anything. With no daemon running it does exactly what `pano on`
  does, owner included, so there is one story: whichever command opened
  the window that started pano owns pano.
- **`q` asks.** The list's `q` opens a two-item overlay with both choices
  and their keys on screen: `q` *quit and turn pano off* (`POST /v1/off`),
  `b` *keep pano running in the background* (`POST /v1/disown`, then quit).
  The highlighted default follows ownership; `esc` stays. ctrl-c quits
  without asking and lets the daemon apply the ownership rule.
- The daemon closing the attachment (a `pano off` in another terminal, a
  crash) makes the UI exit with "pano was turned off" instead of showing a
  dead screen.
- `pano off`, `pano start`/`pano stop`, `pano run --` and the MCP server are
  unchanged. `pano off` remains the inverse of `pano on -b` and the way to
  stop a disowned daemon.

## Consequences

- Two lines cover the common case: `pano on` … press `q`. The README
  quickstart drops to that.
- "Is pano on?" keeps one answer, and gains "who turns it off": `pano
  status` and `pano_status` print `lifecycle app` / `background` with the
  number of attached UIs.
- Ownership is enforced where the connection lives, so a terminal closed
  mid-session leaves nothing behind — the same guarantee the watchdog gives
  for a dead daemon, extended to a dead UI.
- A second `pano ui` window can attach to an app-mode daemon; it is a
  viewer, and the owner's window still decides. Two `pano on` windows are
  not a supported way to share ownership: the later one takes it.
- The attach stream is one more long-lived request per UI on the control
  socket, heartbeated every 15 s like `/v1/events`.
- ADR 0006's "`pano off` is the symmetric inverse of `pano on`" becomes
  "quitting the UI or `pano off`"; everything else in 0006 stands.
