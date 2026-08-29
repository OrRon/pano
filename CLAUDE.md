# Working on pano

Guidance for anyone (human or AI assistant) changing this repository. Keep it
short and current; the detailed design lives in `docs/`.

## Build, test, lint

```sh
export PATH="/opt/homebrew/bin:$PATH"   # Homebrew Go on macOS
make build        # → bin/pano
make test         # go vet + go test -race ./...   (must be green)
make lint         # golangci-lint v2, config in .golangci.yml (must be 0 issues)
make fuzz         # short fuzz pass over the parsers
make bench-proxy  # direct vs proxied throughput via oha (brew install oha)
```

CI runs the same on macOS (tests) and Linux (build). Coverage targets:
≥85 % for `proxy`, `ca`, `rules`, `view`, `explain`; ≥70 % overall.

## Architecture in one paragraph

One daemon (`internal/daemon`) owns the CA (`internal/ca`), the MITM engine
(`internal/proxy`), capture storage (`internal/store`: ring + blobs + SQLite
with FTS5), the rules engine (`internal/rules`) and the macOS system-proxy
manager (`internal/sysproxy` + crash-restore `internal/watchdog`). It serves a
control API over `~/.pano/pano.sock` (`internal/control`). **The CLI
(`internal/cli`), the MCP server (`internal/mcpserver`) and the TUI
(`internal/tui`) are thin clients of that API via `internal/client`** — all
rendering, limits and redaction happen server-side in `internal/view` so every
front end behaves identically. `internal/api` holds the wire types and is the
single source of truth. Read `docs/architecture.md` and `docs/adr/` before
changing boundaries.

## Invariants (do not break)

- **Redaction is on by default** everywhere a body or header is rendered;
  `reveal_secrets` is explicit and audited (`~/.pano/audit.log`).
- **Never install the CA over MCP.** Terminal only (`pano ca install`).
- **`pano_system_proxy` requires `confirm:"yes"`** and its description leads
  with "CHANGES macOS SYSTEM SETTINGS".
- **The proxy hot path never blocks on storage.** The SQLite writer is
  write-behind with a drop policy; dropped counts are surfaced, never hidden.
- **Bodies are stored as raw wire bytes** (`Content-Encoding` preserved);
  decoding is lazy (`view.Decode`).
- **Tunnels (undecrypted CONNECTs) stay visible in lists**, like every other
  proxy. Filter with `kind=http`; do not hide them by default.
- **The `never` list wins over every decrypt mode**, and hosts that reject
  pano's certificate are *suggested*, never added automatically
  (`proxy.DecryptPolicy.Decide`, ADR 0007).
- **Host lists are printed in full everywhere** (`pano status`, `pano doctor`,
  `pano_status`, TUI): every entry, no counts, no `…` — wrap instead.
- **Streaming responses are flushed per read** (SSE/LLM tokens must arrive
  live) — see `proxy.isStreamy`.
- **Replay goes through the proxy itself** using internal headers
  `X-Pano-Replay-Of` / `X-Pano-No-Rules` / `X-Pano-Flow-Id`, which are stripped
  before leaving pano and from captured headers.
- Loopback binds only; socket 0600; refuse to run as root. The one
  exception is `pano mobile` (ADR 0008): an extra listener for the **proxy
  only**, on the Mac's LAN IP (never `0.0.0.0`), wrapped in
  `mobile.PrivateOnly`, terminal-only (no MCP tool), audited. Control API
  and MCP HTTP never leave loopback.
- **Requests addressed to pano itself are served locally, never forwarded**
  (self addresses and `pano.internal`, any scheme) and never become flows;
  `pano.internal` is TLS-terminated whatever the decrypt mode.

## Gotchas learned the hard way

- `go mod tidy` prunes deps nothing imports yet — run it only when the tree is
  complete, and `go mod tidy -diff` must be clean for CI.
- MCP go-sdk `jsonschema:"…"` tags must not contain `=` (parsed as
  `key=value`); the constructor test in `internal/mcpserver` catches panics.
- Unix socket paths are limited to ~104 bytes on macOS; `Paths.Socket()` falls
  back to `$TMPDIR/pano-<uid>.sock` when `$PANO_HOME` is long.
- Old daemon shutdown must not delete a replacement daemon's socket/pid; the
  listener unlinks its own socket on Close.
- `httptrace` callbacks run on transport goroutines — never touch the flow
  from them (see `traceTimes`).
- `pano stop` restores the system proxy and `pano start` does **not** re-enable
  it; after a rebuild the user must run `pano on` again.
- WebSocket upstream dials need an explicit port (`:443`/`:80`) — the Host
  header usually has none.
- Bubble Tea v2 (`charm.land/bubbletea/v2`): `View()` returns `tea.View`; use
  `ansi.StringWidth`/`ansi.Truncate` for all width math; the TUI snapshot test
  (`PANO_SNAPSHOT_DIR=… go test ./internal/tui/`) writes frames at 80×24,
  120×40, 200×60 for visual review — look at them before calling UI work done.
- `golangci-lint fmt ./...` fixes gofumpt/goimports findings.
- **Bodyless requests must go upstream with END_STREAM.** The net/http server
  hands a non-nil, non-`NoBody` body sentinel even for a GET with no body.
  Wrapping it in the capture `teeReader` leaves `ContentLength == -1`, so Go's
  HTTP/2 transport omits END_STREAM on HEADERS and sends the GET as a half-open
  *bodied* stream. Origins that reject a GET-with-a-body (e.g. LinkedIn's
  Cloudflare image-resizer Worker → HTTP 500) then fail *only through pano*,
  looking exactly like a decryption/pinning problem though the handshake is
  fine. `handleExchange` gates body capture on `r.ContentLength != 0` (–1 for
  chunked still counts as a body); regression tests
  `TestBodylessRequestSetsEndStream` / `TestChunkedRequestBodyForwarded`.

## Conventions

Conventional Commits (`feat(ui): …`, `fix(proxy): …`); doc comments on every
export; `doc.go` per package; tests next to code; golden files support
`-update` where present; no personal paths in fixtures (use `/home/dev/app`).
Visual work follows `docs/tui-design.md` — deliberate, not default.
