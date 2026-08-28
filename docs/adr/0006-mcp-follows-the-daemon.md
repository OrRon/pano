# ADR 0006: MCP availability follows the daemon, and the daemon follows `pano on`/`off`

Status: accepted (2026-08); reverses the auto-start clause of [ADR 0005](0005-cli-and-mcp-as-thin-clients.md)

## Context

Until now `pano mcp` started the daemon whenever the control socket did not
answer (`[mcp] autostart = true`), and `pano off` only restored the macOS
proxy settings and left the daemon running. Because Claude Code spawns
`pano mcp` at the start of every session, the practical effect was that the
daemon was running whenever a session had been opened since the last reboot
— capturing, growing `pano.db`, holding a listener on `:9091` — without the
user having asked for it that day. The owner's requirement is the opposite:
**pano runs only after the user turns it on in a terminal, and MCP tools are
usable only during that window.**

Two constraints shape the answer:

- MCP over stdio is spawn-at-session-start. If `pano mcp` exited when the
  daemon was down, Claude Code would mark the server failed and the tools
  would stay unavailable for the whole session even after `pano on`, until
  the user reconnected the server by hand. That is worse than "off".
- The MCP process is already stateless: every tool call is one fresh HTTP
  request over `~/.pano/pano.sock` (ADR 0005). Nothing needs a daemon at
  `initialize` time.

## Decision

- `pano mcp` **never starts the daemon**. The `[mcp] autostart` option is
  removed rather than defaulted to `false`, so there is one behaviour to
  document and test.
- The MCP server always comes up and serves `initialize`, `tools/list`,
  `resources/list` and `prompts/list`. While the daemon is down, every tool
  returns `isError: true` with the fixed text *"pano is off: the daemon is
  not running. Ask the user to run `pano on` (or `pano start`) in a
  terminal, then retry."* and resources fail with the same message. The
  server instructions carry the same rule so the agent asks instead of
  trying to work around it.
- Because each call dials the socket anew, the tools resume the moment
  the daemon answers — no client restart, no reconnect, no notification.
- `pano off` restores the proxy snapshot **and then stops the daemon**; it
  is the symmetric inverse of `pano on`. `pano off --keep-daemon` restores
  the proxy only, for users of `pano run --` who want MCP without the system
  proxy. `pano start` / `pano stop` remain the daemon-only pair.
- The Streamable HTTP MCP endpoint is mounted inside the daemon, so it is
  gated by the same lifecycle with no extra code.

## Consequences

- "Is pano on?" has one answer for humans and agents: `pano status` says
  "not running", `pano_status` says "pano is off"; both point at `pano on`.
- An agent can no longer wake pano up as a side effect of being asked
  about traffic. This is the intended safety property: starting a MITM
  proxy stays a deliberate user action, like installing the CA.
- Users who relied on `pano mcp` bringing the daemon up must run `pano on`
  or `pano start` first; `pano run -- <cmd>` still starts it, since that is
  an explicit terminal action too.
- A future launchd/login-item mode ("always on, opt-in") would be a separate
  decision and does not conflict with this one.
