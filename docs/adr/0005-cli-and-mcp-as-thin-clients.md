# ADR 0005: CLI and MCP server are thin clients of one control API

Status: accepted (2026-08)

## Context

pano has two front-ends — a cobra CLI for humans and an MCP server for
agents — and one long-running daemon that owns the proxy, the store and the
rules. Early designs had the CLI open the SQLite file directly and the MCP
server embed the daemon. Both create two implementations of every feature
(filters, views, redaction, budgets), two places for bugs, and a locking
problem on the database.

Constraints:

- Behaviour must be identical whether a human or an agent asks: the same
  filter syntax, the same one-line rows, the same redaction, the same
  limits. Otherwise documentation and the agent's mental model diverge.
- Redaction and body budgets are security- and cost-relevant; they must be
  enforced in one place that cannot be bypassed by a client flag.
- `pano mcp` must keep stdout clean (protocol only) and be cheap to start;
  Claude Code spawns it per session.
- Other local tools (editors, scripts, a future web UI) should be able to
  integrate without linking Go.

## Decision

- The daemon exposes an HTTP/1.1 + JSON **control API** on a Unix socket
  (`~/.pano/pano.sock`, mode 0600), versioned under `/v1`, with one response
  envelope (`{ok, data}` / `{ok:false, error:{code, message, hint}}`) and
  typed request/response structs in `internal/api` — the single source of
  truth for the wire format, shared by server and clients.
- `internal/client` is the only Go client. Both `internal/cli` and
  `internal/mcpserver` are built on it and contain no business logic: the
  CLI formats `api.FlowRow` into a coloured table, the MCP server formats the
  same rows into a fixed-column text block and appends a `next:` hint.
- **All rendering, budgets and redaction run server-side** in the daemon
  (`internal/view`, `internal/explain`, `daemon/backend.go`). A client can
  ask for `reveal_secrets`, but the daemon decides, applies it, and writes
  the audit line.
- The daemon also mounts the MCP server over Streamable HTTP on loopback so
  non-spawning clients use the identical code.
- `pano mcp` auto-starts the daemon (configurable) so "register once, it
  just works" holds.

## Consequences

- One code path per feature. Adding a filter or a view means touching
  `internal/api`, the daemon backend, and optionally the two renderers.
- A `pano`-shaped feature is testable through the API with `httptest` and a
  temp socket, independent of cobra or MCP.
- Every CLI call pays a local HTTP round trip (sub-millisecond on a Unix
  socket); `pano tail` uses the SSE endpoint to avoid polling.
- Third parties can drive pano with `curl --unix-socket ~/.pano/pano.sock
  http://pano/v1/flows`.
- The client library is `internal/`, so there is no public Go API promise
  before 1.0; the HTTP API is the compatibility surface.
