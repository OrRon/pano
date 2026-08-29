# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org).

## [Unreleased]

### Security
- Root CA now lives 2 years (was 10), is rotated automatically once expired (leaf cache wiped, `pano ca install` prompted again), and `pano ca status` / `pano status` / `pano doctor` / `pano_status` warn 30 days ahead; leaf certificates never outlive the root.
- `pano ca install` grants keychain trust for the TLS server policy only (`-p ssl -p basic`), so the root cannot vouch for code signing, S/MIME or software updates; `pano ca uninstall` also sweeps roots left by earlier rotations.

### Added
- HTTPS MITM engine: CONNECT interception, local CA with per-host leaf certificates, HTTP/1.1 and HTTP/2 inside tunnels, live SSE streaming, WebSocket splice with frame capture, bypass list for pinned hosts, loop guard.
- Capture pipeline: in-memory ring, content-addressed blob store, write-behind SQLite (WAL, contentless FTS5 full-text search), sessions, retention pruning.
- Control API over a Unix socket (`~/.pano/pano.sock`) shared by the CLI and the MCP server.
- CLI: `start/stop/status`, `on/off` (macOS system proxy with snapshot/restore and crash watchdog), `ca install|uninstall|status`, `tail`, `flows`, `show` (summary/schema/truncated/pretty/raw views, JSON path selection), `explain`, `diff`, `replay`, `rules`, `bp`, `session`, `bypass`, `export/import har`, `run --`, `env`, `doctor`, `config`, `mcp`.
- MCP server (stdio + Streamable HTTP): 15 `pano_*` tools, resources (`pano://ca.pem`, `pano://status`, `pano://flows/{id}`), prompts, token-efficient rendering with `next:` hints.
- Secret redaction on by default (headers, bearer/API-key/JWT patterns, sensitive JSON keys) with audited `reveal_secrets`.
- LLM traffic explain: Anthropic Messages, OpenAI Chat Completions and Responses, Gemini — stream reassembly, final message, tool calls, usage.
- Live rules engine: delay, set/remove header, set query, rewrite body (JSON patch / regex / template), mock, block, redirect, throttle, tag, breakpoints with hold/resume/drop; presets `slow_network`, `fail_rate`, `offline_host`, `timeout`, `rate_limit`, `hold`; TTLs.
- HAR 1.2 export/import.
- `docs/mcp-protocol.md`: the agent↔pano wire as measured on a real Claude Code session — topology and handshake diagrams, tool metadata (`annotations`, `_meta.anthropic/*`), off/on states, sizes, a worked token-efficient search.
- `pano ui`: interactive terminal UI (Bubble Tea v2) with live list, detail tabs, explain, diff, rules and held-request drawers; design notes in `docs/tui-design.md`.

### Changed
- pano is no longer "always on". `pano mcp` never starts the daemon (the `[mcp] autostart` config key is gone); while the daemon is down every MCP tool and resource answers *"pano is off … ask the user to run `pano on`"* and starts working again the moment it is back, without a client restart. `pano off` now stops the daemon after restoring the proxy settings (`--keep-daemon` keeps the old proxy-only behaviour). See ADR 0006.
- `pano ui`: colour-coded Explain page (provider/model/status, request and usage segments, `[text]`/`[tool_use]`/`[thinking]` items, stop reasons, errors, chat roles), a body syntax palette (strings / numbers / booleans) shared by the summary, schema and pretty views, numbered detail tabs (`1 Summary  2 Request …`) and tab-aware footer hints so Explain → Request/Response is discoverable. `Enter` and `x` open the same detail view (Summary vs Explain tab); `x` inside the view jumps to Explain instead of toggling, and falls back to Summary with a toast when explain is unavailable for the flow.

### Fixed
- Bodyless requests (GET and friends) were forwarded upstream as half-open HTTP/2 streams: the net/http server hands the proxy a non-nil body sentinel even when there is no body, and wrapping it in the capture reader left the length unknown so Go's transport withheld END_STREAM. Origins that reject a GET claiming a body — e.g. LinkedIn's Cloudflare image-resizer Worker — returned HTTP 500, so images loaded direct but 500'd through pano. Bodyless requests now go out with END_STREAM; request-body capture is unchanged.
- WebSocket upstream dials failed with "missing port in address" when the Host header had no port.
- Status filters (`status=!2xx`) no longer match flows without a status (tunnels, transport failures).

### Performance
- Apple M-series, 64 concurrent clients, small JSON responses, capture on: ~70k req/s through pano vs ~146k direct; p50 added latency ≈ 0.4 ms, p99 ≈ 3 ms. The SQLite writer drops events under sustained load beyond ~5k flows/s (counted and surfaced in `pano status`); the proxy path is never blocked.
