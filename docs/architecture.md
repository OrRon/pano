# Architecture

This is the mental model of `pano`: what the processes are, where HTTPS is
decrypted, how a captured exchange becomes a searchable flow, and how the
agent-facing surface stays cheap in tokens. Everything here is derived from
the code under `internal/`; when the code and this page disagree, the code
wins and this page has a bug.

## Process topology

One daemon owns the proxy, the capture store, the rules engine and the
system-proxy state. The CLI and the MCP server are **thin clients of the
same control API**, so `pano flows` and `pano_flows` produce the same rows
from the same code path. All rendering, limits and redaction happen inside
the daemon.

```
  Claude Code ───stdio───▶ pano mcp ─────────┐
  other MCP clients ──Streamable HTTP──▶ 127.0.0.1:9092/mcp (mounted in daemon)
  terminal: pano <cmd> ───────────────────────┤
                                              │  HTTP/1.1 + JSON
                                              ▼  ~/.pano/pano.sock (0600)
                                   ┌─────────────────────────┐
                                   │   pano daemon (panod)   │
                                   │  control API  /v1/*     │
                                   │  proxy  127.0.0.1:9091  │──▶ origins
                                   │  store  ~/.pano/pano.db │
                                   │  rules  ~/.pano/rules.json
                                   │  sysproxy snapshot      │
                                   └─────────────────────────┘
                                              ▲
  browsers / apps ── CONNECT / absolute-URI ───┘   (pano on, or pano run --)
```

- `pano start` execs a detached `pano daemon` (own session, stdout/stderr to
  `~/.pano/daemon.log`, pid in `~/.pano/daemon.pid`) and waits up to 6 s for
  `GET /v1/status` to answer. `pano start --foreground` runs it in-process.
- `pano mcp` speaks MCP on stdin/stdout. It never starts the daemon: while
  the socket is not answering every tool returns `isError` "pano is off …
  run `pano on`", and calls succeed again as soon as the daemon is back
  (ADR 0006). Logs go to stderr only.
- The daemon also mounts the same MCP server on loopback TCP
  (`[mcp] expose_http`, `[proxy] mcp_port`) using the stateless Streamable
  HTTP transport, for clients that cannot spawn a process.
- The control API is `internal/control` (routes) + `internal/client`
  (Go client). Responses use one envelope: `{"ok":true,"data":…}` or
  `{"ok":false,"error":{"code","message","hint"}}`. Error codes:
  `not_found` (404), `bad_request` (400), `conflict` (409), `unsupported`
  (501), `internal` (500).
- The daemon refuses to run as root and binds `127.0.0.1` by default.

### Control API routes

| Method | Path | Purpose |
|---|---|---|
| GET | `/v1/status` | daemon, capture, CA, system-proxy state |
| GET | `/v1/stats` | counters (memory ring, SQLite writer, bus) |
| POST | `/v1/capture` | `start`, `stop`, `clear`, `session` |
| GET/POST | `/v1/sessions`, DELETE `/v1/sessions/{id}` | session CRUD |
| GET | `/v1/flows` | list/search (query params = filter fields) |
| GET | `/v1/flows/diff?a&b` | structural diff |
| GET | `/v1/flows/{id}` | rendered flow (`part`, `view`, `path`, `max_bytes`, `headers`, `reveal`) |
| GET | `/v1/flows/{id}/raw` | the `flow.Flow` record as JSON |
| GET | `/v1/flows/{id}/body/{request\|response}?decode=1` | body bytes |
| GET | `/v1/flows/{id}/ws` | captured WebSocket messages |
| POST | `/v1/flows/{id}/replay` | re-send with overrides |
| GET | `/v1/flows/{id}/explain` | LLM digest |
| POST | `/v1/tail` | long-poll for flows newer than a cursor |
| GET | `/v1/events` | SSE stream of bus events (`types=`, `host=`) |
| GET/POST | `/v1/rules`, GET `/v1/rules/presets`, PATCH/DELETE `/v1/rules/{id}`, DELETE `/v1/rules` | rules |
| GET | `/v1/held`, POST `/v1/held/{id}` | breakpoints |
| GET/PATCH | `/v1/decrypt` | decrypt policy: mode, `only`/`never` lists, rejected hosts; PATCH is a partial update (`add_only`, `remove_never`, …, `source`) |
| POST | `/v1/har` | export / import |
| GET | `/v1/ca.pem` | root certificate |
| GET/POST | `/v1/sysproxy` | system proxy state / toggle (`confirm:"yes"`) |
| GET | `/v1/config` | effective configuration |
| POST | `/v1/shutdown` | graceful stop |
| GET | `/debug/pprof/*` | Go profiler |

## Where HTTPS is decrypted

The engine (`internal/proxy`) is hand-rolled on `net/http`, `crypto/tls`
and `golang.org/x/net/http2`; see [ADR 0001](adr/0001-hand-rolled-engine.md)
for why. A client that is configured to use pano as an HTTP proxy sends
`CONNECT host:443`. From there:

```
client ──CONNECT host:443──▶ pano: hijack the TCP conn, write "HTTP/1.1 200 Connection Established"
                              ├─ DecryptPolicy.Decide(host): on the never list → splice, tag "never"
                              │                              mode off          → splice, tag "off"
                              │                              mode only, unlisted → splice, tag "unlisted"
                              │   (raw TCP splice = recorded as kind=tunnel with bytes and timing, never decrypted)
                              └─ else: tls.Server(conn, ca.TLSConfig())
client ══TLS ClientHello════▶ GetCertificate(SNI, or the CONNECT target when SNI is absent/ECH outer name)
                              mints a leaf for `host` signed by ~/.pano/ca.pem  ◀── TLS session #1 terminates HERE
                              handshake succeeds only if the client trusts the pano CA
                              ALPN: "h2" → http2.Server.ServeConn ; "http/1.1" → http.Server.Serve(oneConnListener)
client ══encrypted HTTP═════▶ tlsConn.Read() = PLAINTEXT request → handleExchange (capture · rules · breakpoints)
                              pano ══ TLS session #2: a normal http.Transport that VERIFIES the origin's real cert ══▶ origin
                              PLAINTEXT response ◀── capture · rules · throttle
client ◀══ re-encrypted with the minted leaf ═══ pano
```

Key facts:

- **Two independent TLS sessions**, plaintext in the middle. The upstream
  side is an ordinary Go client with system roots; pano does not weaken
  verification toward origins.
- The CA (`internal/ca`) is an ECDSA P-256 root generated per user on first
  run (2-year validity, capped at 825 days, CN
  `pano Root CA (<hostname>, <date>)`) plus **one shared leaf key**. An
  expired root is rotated on load (`RotatedFrom`, leaf cache wiped) and the
  API/CLI carry an `ExpiryWarning` from 30 days out. Leafs are per host (DNS
  SAN or IP SAN, `serverAuth` EKU, 30-day TTL, capped at 397 days and never
  past the root's `NotAfter`), looked up in an in-memory LRU (4096), then
  `~/.pano/certs/<host>.pem`, then minted under `singleflight`. Keychain
  trust is granted for the `ssl` and `basic` policies only.
- Plain `http://` proxying (absolute-URI requests) uses the same handler
  without the TLS step. `GET /` or `/_pano/ca.pem` on the proxy port serves
  a tiny page and the root certificate.
- Handshake failures are recorded as flows with `kind=tunnel`,
  `state=failed` and an error such as `client rejected pano certificate
  (CA not trusted, or the app pins certificates — run pano decrypt never add
  <host>)`. The proxy also counts these per host for an hour
  (`Server.Rejected`, bounded, in memory); the daemon exposes them as
  `Decrypt.Rejected` so `pano decrypt`, `pano status`, `pano_status` and the
  TUI can name the pinning app. They are suggestions only — nothing is ever
  added to `never` automatically ([ADR 0007](adr/0007-decrypt-policy.md)).
  This is the first thing to look at when a site "does not work".
- The policy (`proxy.DecryptPolicy`) is an atomic pointer swapped by
  `PATCH /v1/decrypt`; open tunnels keep the policy they started with. A bare
  host entry covers its subdomains (`proxy.HostMatch`), globs are exact.
- A loop guard refuses to proxy to pano's own port; a connection
  semaphore (`[limits] max_conns`) bounds concurrent tunnels.

### The handler path

`handleExchange` is protocol-agnostic and shared by plain HTTP, h1-in-tunnel
and h2-in-tunnel:

1. WebSocket upgrade? → dial the origin, forward the upgrade, splice both
   ways; when `[capture] websocket_frames` is on, an RFC 6455 frame parser
   tees the copied bytes into `WSMessage` events (frames are still forwarded
   verbatim).
2. Allocate a `flow.Flow` (monotonic ID rendered as short Crockford
   base32, e.g. `f8k2q`), strip hop-by-hop headers, tee the request body
   into a capped capture buffer, publish `started`.
3. Run **request-phase rules**. A rule may mutate headers/URL/body, mock the
   response, block, redirect, or hold the request on a breakpoint.
4. `transport.RoundTrip` on one shared `http.Transport` (`Proxy: nil` to
   avoid loops, h1+h2, `DisableCompression: true` so bodies are stored
   exactly as sent, `ResponseHeaderTimeout: 0` because LLM APIs can take
   minutes to first byte, LRU TLS session cache). `httptrace` timestamps
   (DNS, connect, TLS, first byte, connection reuse) land in `Flow.T`.
5. Run **response-phase rules** (delay, header edits, body rewrite, mock,
   block, throttle, breakpoint, tag).
6. Copy headers untouched and stream the body, teeing into the capture
   buffer. "Streamy" responses (`text/event-stream`, unknown length,
   `X-Accel-Buffering: no`, `Cache-Control: no-transform` JSON) are flushed
   after every read so LLM token streams pass through in real time.
7. Store the two body blobs, publish `done` (or `failed` with an error such
   as `client disconnected`, `upstream timeout: …`, `dns: …`).

Bodies stay gzip/br/zstd on the wire and in the blob store; decoding is lazy
(`internal/store/decode.go`, `internal/view/decode.go`) with an 8 MiB output
bound.

## Capture pipeline

```
proxy ──Sink──▶ daemon.Started/Updated/Done ──▶ Mem ring (10k flows, newest-first iteration)
                                             └─▶ bus.Publish(Event)
                                                    ├─▶ SQLite subscriber ──queue(8192)──▶ writer goroutine
                                                    │       batches ≤256 items / 50 ms → one transaction
                                                    │       flows upsert · blobs · ws_messages · FTS5 rows
                                                    ├─▶ /v1/events SSE clients (pano tail)
                                                    └─▶ /v1/tail long-poll waiters (pano_tail)
proxy ──Sink.Blob(bytes)──▶ MemBlobs (content-addressed sha256, 256 MiB LRU) ──▶ SQLite.PutBlob (queued)
```

- **Sink** (`proxy.Sink`) is implemented by the daemon: every event goes to
  the in-memory ring first, so reads never wait for disk.
- **Event bus** (`internal/bus`): fan-out with a bounded queue per
  subscriber. When a subscriber's queue is full the oldest event is
  dropped and a single coalesced `dropped` event is delivered in front, so a
  slow consumer never stalls the proxy.
- **SQLite** (`internal/store/sqlite*.go`, pure-Go `modernc.org/sqlite`,
  WAL, one writer connection + a read-only pool): ingestion is
  **write-behind**. `Enqueue` never blocks; a full queue drops the item and
  increments the `dropped` counter that `pano status` and `pano_status`
  surface as a warning. Blobs are deduplicated by hash with trigger-maintained
  reference counts; `blob_text` caches the decoded UTF-8 text of textual
  blobs; a contentless **FTS5** table indexes host, path, headers and up to
  256 KiB of decoded text per body for finished flows (`--q` / `q=`).
- **Reads**: lists come from the ring unless the filter needs the database
  (`q=`, a non-current `session=`, or nothing matched in memory). Single
  flows are looked up in the ring, then SQLite.
- **Retention**: a pruner runs every minute enforcing `[retention]`
  `max_age` / `max_flows` / `max_db_bytes`, deletes orphan blobs, runs
  `incremental_vacuum` and checkpoints the WAL. Flows still `active`/`held`
  from a previous process are marked `failed ("daemon restarted")` on open.
- **Sessions** group flows; the current session id is stamped on every new
  flow. `pano session new` ends the previous one.

## Rules engine

`internal/rules` implements `proxy.Hooks`. The compiled rule set lives behind
an `atomic.Pointer` and is replaced wholesale on every change, so the hot
path is lock-free and allocates nothing for rules that do not match.

- **Phases**: every rule is evaluated in the request phase, the response
  phase, or both. If `match.phase` is empty it is derived from the actions
  (`throttle` is response-only, `mock`/`block`/`redirect`/`set_query`
  default to request; a `match.status` forces response).
- **Order**: higher `priority` first, then oldest first. Actions run in
  order; `mock`, `block` and a dropped `breakpoint` end evaluation.
- **Gates**: `probability` (per evaluation), `max_hits` (disables the rule
  when reached), `ttl_s` (lazy expiry on the hot path). Every applied action
  appends a `RuleHit` to the flow (`rules:` line in `pano show`, the
  `mock`/`delay`/… flags in list rows).
- **Breakpoints** (`hold.go`): the exchange is parked on a channel, the flow
  is set to `state=held` and a `held` event is published. `Resume` applies
  optional edits (URL, method, headers, body, JSON patch; status on the
  response side) or drops the connection. After `[breakpoints]
  hold_timeout` (120 s) the exchange continues unmodified; if the client
  disconnects meanwhile it is dropped.
- **Persistence**: `~/.pano/rules.json` is rewritten atomically on every
  change and loaded at start (expired rules skipped).

See [rules.md](rules.md) for the schema and recipes.

## System proxy, snapshot/restore and the watchdog

`internal/sysproxy` (macOS only; other platforms return `unsupported`) drives
`/usr/sbin/networksetup`:

1. `pano on` → `POST /v1/sysproxy {enabled:true, confirm:"yes"}`.
2. The daemon **snapshots** the web/secure proxy and bypass-domain settings
   of every enabled network service to `~/.pano/sysproxy.json` (0600)
   *before* changing anything. An existing snapshot is kept, so a later
   restore always returns to the pre-pano state.
3. It applies `-setwebproxy`, `-setsecurewebproxy` and
   `-setproxybypassdomains` (existing list ∪ `localhost 127.0.0.1 *.local
   169.254/16`). If `networksetup` refuses for lack of privileges the command
   is retried through `osascript … with administrator privileges` (the
   standard macOS password dialog) and the snapshot remembers that.
4. The daemon spawns a detached **watchdog** (`pano _watchdog --pid <daemon>
   --state ~/.pano/sysproxy.json`) that waits on a kqueue
   `EVFILT_PROC/NOTE_EXIT` for the daemon to die (polling on other OSes) and
   then restores the snapshot. A clean `pano off` / `pano stop` restores and
   deletes the file first, so the watchdog simply exits.
5. On start the daemon calls `RestoreStale`; `pano doctor` reports a stale
   file and `pano off` restores it even when the daemon is down.

The presence of `sysproxy.json` is the single source of truth for "pano owns
the system proxy right now".

## Token-efficiency design

The agent surface (`internal/mcpserver`, `internal/view`, `internal/explain`)
is built around the assumption that the reader pays per token:

- **One line per flow.** `pano_flows` returns a fixed-column table
  (`id time meth host path st dur up down type flags`), roughly 40 tokens a
  row, with an opaque cursor for the next page.
- **Views before bytes.** `pano_flow` defaults to `view=summary`: a
  type-aware digest (key list with types and lengths, notable fields such as
  `error`, `usage`, `*_id`, SSE event counts) capped at ~1.5 KB. `schema`
  infers a compact shape (`{ role: "user"|"assistant", created_at:
  str<date-time> }`). `truncated` shows 75 % head / 25 % tail within the
  budget. `pretty` and `raw` need an explicit `max_bytes` (default 4096,
  hard cap 1 MiB). Binary bodies are never inlined.
- **Path selection.** `path=choices.0.message.content` (gjson, JSONPath
  accepted) fetches one value instead of a body.
- **Every body starts with a header line** (`body: application/json 5120B
  [gzip→18342B] sha256:1f2e3d4c [truncated to 4096 of 18342]`) so the
  reader knows what it is not seeing.
- **`explain` instead of reading LLM bodies.** Streams from Anthropic,
  OpenAI Chat/Responses and Gemini are reassembled into the non-streaming
  object and rendered as a few lines (model, request shape, usage, stop
  reason, final content, tool calls). Thinking and tool inputs are hidden
  unless asked for.
- **`next:` hints.** Every tool result ends with the most useful follow-up
  call, and the server instructions (< 2 KB, Claude Code truncates longer
  ones) teach the ladder `flows → flow summary → path → raw`.
- **Redaction on by default.** Secret headers and well-known credential
  shapes are masked to `sk-ant-…a1b2 hash:9f3c` — a stable fingerprint that
  still lets the reader match two occurrences. `reveal_secrets=true` is
  audited.
- **Polling, not push.** `pano_tail` is a long-poll (≤ 25 s) that fits inside
  Claude Code's request timeout; see [ADR 0003](adr/0003-mcp-polling-not-push.md).

## Data layout: `~/.pano`

| Path | Mode | What |
|---|---|---|
| `config.toml` | 0600 | overrides over the built-in defaults ([config.md](config.md)) |
| `pano.sock` | 0600 | control API Unix socket (falls back to `$TMPDIR/pano-<uid>.sock` if the path is too long) |
| `daemon.pid`, `daemon.log` | 0600 | detached daemon pid and log (rotated to `daemon.log.1` at 20 MiB on start) |
| `ca.pem` / `ca.key` | 0644 / 0600 | root certificate and its private key (never served, never logged) |
| `leaf.key` | 0600 | the single private key shared by all minted leaf certificates |
| `certs/<host>.pem` | 0600 | disk cache of minted leafs |
| `pano.db` (+ `-wal`, `-shm`) | | SQLite: `flows`, `blobs`, `blob_text`, `ws_messages`, `sessions`, `flows_fts` |
| `rules.json` | 0600 | persisted rules |
| `sysproxy.json` | 0600 | system-proxy snapshot; exists only while pano owns the system proxy |
| `audit.log` | 0600 | one line per `reveal_secrets` use and system-proxy toggle |

`PANO_HOME` relocates the whole directory; `PANO_SOCK` / `--sock` point a
client at a different socket.

## Package map

| Package | Role |
|---|---|
| `cmd/pano` | entrypoint; wires daemon, MCP and watchdog hooks into the CLI |
| `internal/cli` | cobra command tree, human renderers, `--json` |
| `internal/client` | Go client for the control API (shared by CLI and MCP) |
| `internal/api` | request/response and filter types — the single source of truth for the wire format |
| `internal/control` | control API server: routes, envelope, SSE events, pprof |
| `internal/daemon` | composition root; implements `control.Backend` and `proxy.Sink` |
| `internal/proxy` | MITM engine: CONNECT, TLS, h1/h2, WebSocket, capture, streaming copy |
| `internal/ca` | root CA, leaf minting, LRU/disk cache, macOS keychain trust |
| `internal/flow` | data model (`Flow`, `BodyRef`, `Timing`, `RuleHit`, events) and IDs |
| `internal/bus` | fan-out event bus with drop-oldest queues |
| `internal/store` | memory ring, blob cache, filters, SQLite write-behind + FTS5, retention, sessions, HAR-free row rendering |
| `internal/rules` | live rules, presets, breakpoints, throttling, body rewrites |
| `internal/view` | summary/schema/truncated/pretty/raw, gjson path, redaction, diffs |
| `internal/explain` | LLM provider detection and SSE reassembly into digests |
| `internal/har` | HAR 1.2 export/import |
| `internal/mcpserver` | MCP tools, resources, prompts, instructions |
| `internal/sysproxy` | macOS `networksetup` snapshot/apply/restore |
| `internal/watchdog` | crash-restore helper process |
| `internal/config` | `config.toml` schema, defaults, paths |
| `internal/glob`, `internal/mimeclass` | tiny helpers: host/path wildcards, content-type classes |
