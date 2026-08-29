# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org).

## [Unreleased]

### Fixed
- Turning pano off took up to 8 s while a browser held a streaming or long-poll request open: shutdown waited for the proxy to drain. It now drains for 0.7 s and then closes the remaining connections (the system proxy is already restored by then, so new connections go direct).

### Changed
- **`pano on` is the app** (ADR 0009): it starts the daemon, routes the Mac's traffic and opens the UI; quitting the UI turns pano off. `q` asks — *quit and turn pano off* (`q`) or *keep pano running in the background* (`b`) — with the default following how the window was opened; ctrl-c, a closed terminal or a kill turn pano off as well, because the daemon watches its owning window's connection (`GET /v1/attach`) and restores the proxy itself when it drops. `pano on -b` / `--background` keeps the old behaviour (daemon until `pano off`), and is automatic without a terminal (scripts, agents, `--json`). `pano ui` attaches a window to a running daemon — leaving it never stops anything — and, with pano off, behaves exactly like `pano on`. New `lifecycle` line in `pano status` and `pano_status` (`app` / `background`, UIs attached); new control routes `GET /v1/attach`, `POST /v1/disown`, `POST /v1/off`.

### Security
- Root CA now lives 2 years (was 10), is rotated automatically once expired (leaf cache wiped, `pano ca install` prompted again), and `pano ca status` / `pano status` / `pano doctor` / `pano_status` warn 30 days ahead; leaf certificates never outlive the root.
- `pano ca install` grants keychain trust for the TLS server policy only (`-p ssl -p basic`), so the root cannot vouch for code signing, S/MIME or software updates; `pano ca uninstall` also sweeps roots left by earlier rotations, and `pano ca reset` untrusts the outgoing root before replacing it (previously it stayed trusted for all policies with no key behind it).

### Fixed
- TUI: leaving a filter (`esc`) could leave a partial list — the filtered reload used to replace the local table with its matches, an unfiltered reload was capped at 200 rows, and a slow filtered answer could land after the filter was cleared. The list now keeps one full table that only merges, tracks which rows the daemon matched (live arrivals still show on local match), drops answers to a superseded filter, and applies `since`/`until` locally too.

### Added
- **`pano mobile`** — phones and tablets in one command: opens a second proxy listener on the Mac's LAN address (proxy only, private sources only, terminal-only, audited), prints a QR code, and serves a setup page that detects the platform, hands out the proxy settings as tap-to-copy cards and the CA as an iOS configuration profile / Android `.crt` / PEM, and ticks each step off live — first proxied request, first accepted TLS handshake (probed via `https://pano.internal`) — naming the "installed but not trusted" state when it sees rejections. `http://pano.internal` (and `/ssl`) reaches the same page from any device already proxied. Devices are named from their User-Agent and listed with their state in `pano mobile status`, `pano status`, the TUI header (`▯`) and `pano_status`; the TUI has a Mobile drawer (`M`, fourth tab after Decrypt) with the QR code and the device list where `⏎` opens or closes the listener; flows carry a `client` filter (`--client <ip>` / `client=remote`) and a `▯` flag for remote clients. `pano mobile off` closes the listener and stops the daemon unless `pano on` is active (`--keep-daemon`), and warns that a phone may still point at the closed port; `pano off` closes it too. Docs: `docs/mobile.md`, ADR 0008.
- Decrypt policy (`pano decrypt`, `pano_decrypt`, TUI `D`): mode `all` / `only` / `off` plus an `only` list and a `never` list that wins in every mode. A bare host covers its subdomains. Hosts that refused pano's certificate in the last hour are listed as **rejected** (pinning suspects) in `pano decrypt`, `pano status`, `pano doctor`, `pano_status` and the TUI header, with a one-step `never add --rejected` / `n` action — suggested, never auto-added. In the TUI, `o` on any flow opens an options menu: decrypt only / never its host, filter to it, replay, mark, explain. Default `never` list is the five macOS daemons that pin (`*.push.apple.com *.icloud.com *.icloud-content.com *.apple-cloudkit.com *.ls.apple.com`). Every list is always printed in full. Changes are partial updates over `PATCH /v1/decrypt`, persist to `config.toml` and are audited with their source. See ADR 0007.
- HTTPS MITM engine: CONNECT interception, local CA with per-host leaf certificates, HTTP/1.1 and HTTP/2 inside tunnels, live SSE streaming, WebSocket splice with frame capture, loop guard.
- Capture pipeline: in-memory ring, content-addressed blob store, write-behind SQLite (WAL, contentless FTS5 full-text search), sessions, retention pruning.
- Control API over a Unix socket (`~/.pano/pano.sock`) shared by the CLI and the MCP server.
- CLI: `start/stop/status`, `on/off` (macOS system proxy with snapshot/restore and crash watchdog), `ca install|uninstall|status`, `tail`, `flows`, `show` (summary/schema/truncated/pretty/raw views, JSON path selection), `explain`, `diff`, `replay`, `rules`, `bp`, `session`, `decrypt`, `export/import har`, `run --`, `env`, `doctor`, `config`, `mcp`.
- MCP server (stdio + Streamable HTTP): 16 `pano_*` tools, resources (`pano://ca.pem`, `pano://status`, `pano://flows/{id}`), prompts, token-efficient rendering with `next:` hints.
- Secret redaction on by default (headers, bearer/API-key/JWT patterns, sensitive JSON keys) with audited `reveal_secrets`.
- LLM traffic explain: Anthropic Messages, OpenAI Chat Completions and Responses, Gemini — stream reassembly, final message, tool calls, usage.
- Live rules engine: delay, set/remove header, set query, rewrite body (JSON patch / regex / template), mock, block, redirect, throttle, tag, breakpoints with hold/resume/drop; presets `slow_network`, `fail_rate`, `offline_host`, `timeout`, `rate_limit`, `hold`; TTLs.
- HAR 1.2 export/import.
- `docs/mcp-protocol.md`: the agent↔pano wire as measured on a real Claude Code session — topology and handshake diagrams, tool metadata (`annotations`, `_meta.anthropic/*`), off/on states, sizes, a worked token-efficient search.
- `pano ui`: interactive terminal UI (Bubble Tea v2) with live list, detail tabs, explain, diff, rules and held-request drawers; design notes in `docs/tui-design.md`.

### Changed
- `pano bypass` and `[proxy] bypass` are replaced by `pano decrypt never` and `[decrypt] never`; an existing `bypass` key is read as `never` with a warning and rewritten on the next save. Tunnel rows are tagged with the reason they were not decrypted (`never`, `unlisted`, `off`) instead of `bypass`.
- pano has a mascot: the logo's rounded rectangle with a pair of eyes. `pano on` wakes it up (a short glance around and a blink on a colour TTY; a static frame when piped or with `--json`/`--quiet`/`NO_COLOR`) and `pano off` puts it to sleep. In `pano ui` it sits at the left of the header and its eyes report state — open while capturing, wide when a flow just arrived, shut while paused, crossed when the daemon is unreachable — and the empty state shows the full box. The README carries it as an animated SVG (`docs/assets/mascot.svg`). See `docs/tui-design.md` → Mascot.
- pano is no longer "always on". `pano mcp` never starts the daemon (the `[mcp] autostart` config key is gone); while the daemon is down every MCP tool and resource answers *"pano is off … ask the user to run `pano on`"* and starts working again the moment it is back, without a client restart. `pano off` now stops the daemon after restoring the proxy settings (`--keep-daemon` keeps the old proxy-only behaviour). See ADR 0006.
- `pano ui`: colour-coded Explain page (provider/model/status, request and usage segments, `[text]`/`[tool_use]`/`[thinking]` items, stop reasons, errors, chat roles), a body syntax palette (strings / numbers / booleans) shared by the summary, schema and pretty views, numbered detail tabs (`1 Summary  2 Request …`) and tab-aware footer hints so Explain → Request/Response is discoverable. `Enter` and `x` open the same detail view (Summary vs Explain tab); `x` inside the view jumps to Explain instead of toggling, and falls back to Summary with a toast when explain is unavailable for the flow.

### Fixed
- `pano ui`: the cursor row and the header/footer/title bars are now highlighted edge to edge — nested styles reset the background after the first coloured cell, so only the gutter of the selected row was painted. Leaving the detail view (`esc`, `←`, `q`) closes the pane instead of leaving it on screen next to the list.
- Bodyless requests (GET and friends) were forwarded upstream as half-open HTTP/2 streams: the net/http server hands the proxy a non-nil body sentinel even when there is no body, and wrapping it in the capture reader left the length unknown so Go's transport withheld END_STREAM. Origins that reject a GET claiming a body — e.g. LinkedIn's Cloudflare image-resizer Worker — returned HTTP 500, so images loaded direct but 500'd through pano. Bodyless requests now go out with END_STREAM; request-body capture is unchanged.
- WebSocket upstream dials failed with "missing port in address" when the Host header had no port.
- Status filters (`status=!2xx`) no longer match flows without a status (tunnels, transport failures).

### Performance
- Apple M-series, 64 concurrent clients, small JSON responses, capture on: ~70k req/s through pano vs ~146k direct; p50 added latency ≈ 0.4 ms, p99 ≈ 3 ms. The SQLite writer drops events under sustained load beyond ~5k flows/s (counted and surfaced in `pano status`); the proxy path is never blocked.
