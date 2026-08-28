# ADR 0003: `pano_tail` is cursor polling, not MCP push

Status: accepted (2026-08)

## Context

Agents want to watch traffic while a user reproduces a bug. MCP offers
server-initiated messages (resource subscriptions, `notifications/…`) and
the Go SDK (`modelcontextprotocol/go-sdk` v1.7.0, spec 2026-07-28) can send
them. But the client that matters most, Claude Code, has behaviour we could
verify only for tool calls:

- Tool results are capped at roughly 25k tokens.
- HTTP requests are subject to a ~60 s per-request timer.
- Tool descriptions and server instructions are truncated at 2 KB.
- How (or whether) it surfaces `subscriptions/listen`-style updates to the
  model is undocumented and has changed between releases.

A push design would therefore be best-effort at best and, worse, would
consume context tokens the model did not ask for.

## Decision

- `pano_tail` is a **long-poll**: it takes a cursor (`since="now"` or the
  last cursor returned), returns immediately if newer completed flows exist,
  otherwise waits up to `wait_ms` (default 10 s, **capped at 25 s**) for the
  first one, and always returns a new cursor plus a `next: pano_tail
  since=<cursor>` hint. The cap keeps every call well inside the client's
  request timer.
- The daemon implements it with the in-memory ring plus a bus subscription
  (`POST /v1/tail`); the same bus feeds an SSE endpoint (`GET /v1/events`)
  that the human CLI `pano tail` streams from, because a terminal has no
  timeout problem.
- Held breakpoints are reported inside the tail result so an agent polling
  for traffic also learns that something is waiting for it.
- MCP resources exist (`pano://flows/latest`, `pano://flows/{id}`) but pano
  does not rely on `ResourceUpdated` notifications for correctness.

## Consequences

- Watching traffic costs one tool call per batch; the agent controls the
  cadence and the token spend, and nothing arrives unrequested.
- Latency from event to agent is bounded by the poll interval, which is fine
  for "reproduce, then look".
- If a future Claude Code documents reliable push semantics, a
  notification path can be added without changing the polling contract.
