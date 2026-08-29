<p align="center">
  <img src="docs/assets/mascot.svg" alt="" width="110"><br>
  <img src="docs/assets/logo.png" alt="pano" width="420">
</p>

**All-seeing HTTPS proxy for AI agents.** *(short for Panoptes — the hundred-eyed giant who never stops watching)*

`pano` is a fast, local HTTPS-decrypting proxy for macOS, built for a world where the
thing reading your network traffic is often an AI agent. It exposes everything it
captures through an [MCP](https://modelcontextprotocol.io) server with
token-efficient views, and through a CLI for humans.

- **Decrypts HTTPS** with a local CA you control (HTTP/1.1, HTTP/2, WebSocket, SSE) —
  all hosts, only the ones you list, or none (`pano decrypt all|only|off`); pinned
  apps go on a `never` list and pano names the ones that refuse its certificate.
- **MCP-native**: `pano_flows`, `pano_flow`, `pano_flow_explain`, rules, breakpoints,
  replay, HAR — designed around what an LLM needs, not what a GUI shows.
- **Token-efficient**: one line per flow, body summaries, inferred JSON shapes,
  JSON-path selection, secret redaction on by default, LLM-stream reassembly
  (Anthropic, OpenAI Chat + Responses, Gemini) into final message + usage.
- **Agent-driven chaos**: agents install live rules — latency, failure rates,
  mocks, header rewrites, breakpoints — to test apps under bad conditions.
- **A terminal UI that doesn't look like a terminal UI**: `pano ui` — live
  list, detail views, explain, diff, rules and breakpoints, designed rather
  than default.
- **Fast**: single static Go binary, streaming bodies, zero-copy passthrough,
  write-behind SQLite with full-text search.

> pano is a man-in-the-middle proxy for **your own machine**. Trusting its CA lets
> software on this Mac decrypt this Mac's traffic. See
> [Safety](#safety-how-pano-treats-its-ca) below and [SECURITY.md](SECURITY.md).

## Quickstart (60 seconds)

```sh
brew install orron/tap/pano        # or: go install github.com/orron/pano/cmd/pano@latest
pano ca install                    # one-time: trust the local CA (one password prompt)
pano on                            # route the Mac's HTTP/HTTPS through pano
pano ui                            # interactive terminal UI (or: pano tail)
pano off                           # restore previous proxy settings
```

Give it to Claude Code:

```sh
claude mcp add --scope user --transport stdio pano -- pano mcp
```

Then ask things like:

- *"what did my app send to api.openai.com in the last 5 minutes and why did it 429?"*
- *"explain the last Claude call — final message, tool calls, token usage"*
- *"make api.stripe.com fail 30% of the time for 10 minutes while I run the checkout tests"*
- *"hold the next POST to /v1/orders so I can edit it before it goes out"*
- *"diff the failing request against the one that worked"*

Prefer not to touch system settings? Wrap a single process instead:

```sh
pano run -- curl https://api.github.com/zen
pano run -- npm test
```

## How it works

```
Browser ──CONNECT host:443──▶ pano: hijack, 200 Connection Established
Browser ══TLS══▶ pano mints a leaf cert for `host` signed by ~/.pano/ca.pem  ← TLS terminates here
Browser ══HTTP══▶ pano: plaintext → capture · rules · breakpoints
                  pano ══ separate, fully-verified TLS ══▶ origin
```

See [docs/architecture.md](docs/architecture.md) for the full design and
[docs/mcp.md](docs/mcp.md) for the tool catalog.

## Safety: how pano treats its CA

A trusted root certificate is the most dangerous thing a dev tool can ask
for, so pano handles its own with care:

| | |
|---|---|
| **Yours alone** | Generated on first run, on this machine, for this user. Nothing is shipped in the binary; no two installs share a key. |
| **Key stays put** | `~/.pano/ca.key`, mode `0600`, refused if anyone else can read it. Never logged, never served over the control API or MCP. |
| **Short-lived** | The root is valid for **2 years** (most interception tools use 10). Leaf certs last 30 days and never outlive the root. |
| **Rotates itself** | An expired root is replaced automatically; `pano status` and `pano doctor` warn 30 days ahead. `pano ca reset` renews early and untrusts the old root first. |
| **TLS only** | Keychain trust is granted for the SSL policy only, never code signing, S/MIME or software updates — a leaked root could not sign an app. |
| **Terminal only** | `pano ca install` is not exposed to agents over MCP; the one macOS password prompt is yours. |
| **Reversible** | `pano ca uninstall` removes every pano root from the keychain, including ones left by earlier rotations. |

Toward origins pano is an ordinary TLS client that verifies real certificates
against the system roots; it never weakens the upstream side.
[SECURITY.md](SECURITY.md) has the full list.

## Performance

Apple M-series, 64 concurrent clients, small JSON responses, capture on
(`bench/run.sh`): **~70k req/s through pano vs ~146k direct, p50 added latency
≈ 0.4 ms, p99 ≈ 3 ms**. Bodies stream through unbuffered (SSE tokens arrive
live); persistence is write-behind and drops under sustained overload rather
than slowing the proxy.

## Status

Pre-1.0. macOS is the primary target; the engine is pure Go and builds on Linux,
where `pano run --` and `pano env` work but `pano on/off` and `pano ca install`
print manual instructions instead. See [docs/roadmap.md](docs/roadmap.md) for
what is deliberately not done yet.

## License

Apache-2.0 — see [LICENSE](LICENSE).

## Documentation

- [docs/architecture.md](docs/architecture.md) — process topology, where decryption happens, capture pipeline, rules engine, system proxy + watchdog, data layout, package map
- [docs/mcp.md](docs/mcp.md) — registering the MCP server, tool catalog, escalation ladder, resources, prompts, token budgets, redaction, safety
- [docs/mcp-protocol.md](docs/mcp-protocol.md) — the wire: process topology, the JSON-RPC handshake as captured, what each tool advertises (`annotations`, `_meta`), off/on states, a measured token-efficient search
- [docs/cli.md](docs/cli.md) — every command, filter syntax, view modes, `pano run --` environment, exit codes
- [docs/rules.md](docs/rules.md) — rule schema, actions, phases, presets, breakpoints, recipes
- [docs/config.md](docs/config.md) — `config.toml` keys and defaults, environment variables, `~/.pano` layout
- [docs/faq.md](docs/faq.md) — pinning and the never list, decrypting only your app, Firefox, Apple hosts, ECH, HTTP/3, Linux/Windows, removal, performance, comparison with other proxies
- [docs/tui-design.md](docs/tui-design.md) — the visual rules behind `pano ui`
- [docs/adr/](docs/adr/) — architecture decision records
