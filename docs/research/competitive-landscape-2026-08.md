# Competitive landscape (research snapshot, August 2026)

Internal research note. Facts carry URLs; numbers are as of 2026-08-28 and
will go stale. Not marketing copy — the honest comparison for the README is at
the end.

## Summary

- **No shipped tool combines pano's pieces**: local-first HTTPS/H2/WS/SSE
  decrypting proxy + MCP-native token-budgeted views + LLM-stream reassembly
  (Anthropic/OpenAI/Gemini) + agent-installable chaos rules (delay / fail-rate /
  mock / breakpoint) + crash-safe system-proxy toggle + per-process capture,
  under Apache-2.0. Every individual piece, however, now has a competitor.
- **Closest overall: Proxyman's official MCP** (macOS, shipped 2026-01-19, v3
  with ~40 tools incl. breakpoints, map-local, scripting; default redaction;
  HTTP/2 beta). Proprietary, GUI-first, no LLM explain, no delay/fail-rate
  rules via MCP. https://docs.proxyman.com/mcp
- **Closest architecturally: `yorishiro-proxy`** (Go, Apache-2.0, MCP-native
  MITM with H2/WS/SSE/gRPC, intercept/replay/fuzz, PII masking; 16★, beta).
  https://github.com/usk6666/yorishiro-proxy
- **Closest on LLM explain: `claude-tap`** (3.1k★, token breakdowns,
  auto-redaction; LLM endpoints only, no MCP) and MockServer's AI traffic
  inspection (Java). https://github.com/liaohch3/claude-tap ·
  https://www.mock-server.com/mock_server/ai_traffic_inspection.html
- **Closest on token efficiency: `Charles-mcp`** (summary-first, 2 KB body
  caps, 301★) and `caido-mcp-server` (adaptive caps, default header redaction,
  114★). https://github.com/heizaheiza/Charles-mcp ·
  https://github.com/c0tton-fluff/caido-mcp-server
- **Volume threat: browser MCPs.** Chrome DevTools MCP (49.8k★) and Playwright
  MCP (36.6k★) give agents request lists, bodies, throttling and route mocking —
  browser-only, no proxy, no redaction.
  https://github.com/ChromeDevTools/chrome-devtools-mcp ·
  https://github.com/microsoft/playwright-mcp
- **mitmproxy has no official MCP** (issue #7656 open since 2025-04, assigned;
  third-party `snapspecter/mitmproxy-mcp` 111★ is scraping-oriented).
  https://github.com/mitmproxy/mitmproxy/issues/7656
- **Demand is vendor-validated**: Proxyman, Fiddler (2025-10), Burp (official,
  1.1k★), Caido (endorsed community MCP), HTTP Toolkit (launcher in progress)
  and Kampala (YC W26, commercial "MITM + full MCP") all moved within 10 months.

## Comparison

| Tool | HTTPS | H2 | WS | SSE live | MCP | Token-efficient views | LLM explain | Redaction | Agent mutations | Sys proxy toggle | Local-first | License |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| **pano** | Y | Y | Y | Y | Y | Y | Y | default | delay/fail/mock/rewrite/throttle/hold | Y (crash-safe) + `pano run --` | Y | Apache-2.0 |
| Proxyman MCP | Y | beta | app | app | Y | partial | N | default | breakpoint/map local+remote/scripting | app | Y | proprietary |
| Charles-mcp | via Charles | ? | N | N | 3rd-party | Y | N | ? | throttle/replay | via app | Y | MIT (+paid Charles) |
| mitmproxy-mcp (snapspecter) | Y | ? | N | N | 3rd-party | partial | N | N | inject/block/replay/fuzz | N | Y | MIT |
| yorishiro-proxy | Y | Y | Y | Y | Y | ? | N | PII mask | intercept/replay/fuzz | N | Y | Apache-2.0 |
| Burp MCP (official) | Y | Y | Y | N | Y | partial | N | N | intercept/repeater | N | Y | GPL-3.0 (+paid Burp) |
| caido-mcp-server | Y | ? | Y | N | Y | Y | N | default | intercept/tamper/replay | N | Y | MIT |
| Chrome DevTools MCP | browser | n/a | ? | ? | Y | partial | N | N | throttle/offline | n/a | Y | Apache-2.0 |
| Playwright MCP | browser | n/a | ? | ? | Y | partial | N | N | route mock/offline | n/a | Y | Apache-2.0 |
| Fiddler Everywhere MCP | Y | ? | ? | ? | Y | ? | N | default | rules | app | needs login | proprietary |
| claude-tap | LLM only | ? | N | Y | N | Y (HTML) | Y | auto | N | N | Y | MIT |

## Where pano is behind

No GUI; macOS-only system integration; no iOS/Android/Docker/JVM
interceptors (HTTP Toolkit, Proxyman, Charles); no scripting/addon layer
(mitmproxy Python, Proxyman JS, yorishiro Starlark); no gRPC/protobuf decoding;
no fingerprint-preserving replay; no security tooling; tiny adoption vs.
36–50k★ browser MCPs.

## What pano can own

Chaos-as-MCP-tools with presets *plus* capture in one process; generic-proxy
LLM stream reassembly for three providers exposed as an MCP view; inferred
JSON schemas and JSON-path selection as token-budget tools; crash-safe system
proxy with watchdog restore and `pano run --`; SQLite+FTS history the agent can
query; a single static binary with no account or network dependency.

## Honest README comparison

- **vs Proxyman MCP** — Proxyman has the GUI, iOS/Android, Windows/Linux,
  scripting and mature rules; pano is OSS, headless, adds LLM-stream explain,
  schema/JSON-path views, delay/fail-rate/throttle rules and `pano run --`. If
  you want a GUI, use Proxyman.
- **vs Chrome DevTools / Playwright MCP** — they see only the browser and give
  DOM/console context pano cannot; pano sees every process and mutates traffic
  below the browser. Use both.
- **vs mitmproxy** — more mature, cross-platform, scriptable, has local-capture
  mode; no official MCP and no token-efficient views.
- **vs claude-tap / claude-trace** — better viewers for LLM-only tracing; pano
  captures LLM calls *and* everything else, over MCP, redacted by default.
- **vs Burp / Caido MCP** — security tooling pano does not attempt.
