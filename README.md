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
pano on                            # route the Mac's HTTP/HTTPS through pano and open the UI
                                   # … press q to quit: proxy settings restored, pano off
pano on -b                         # or: run in the background (pano ui · pano tail · pano off)
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

## On, off, and in the background

`pano on` behaves like an app. It starts the daemon, points the Mac's system
proxy at it and opens the UI — that window *is* pano. Quit the window and
pano is off: the proxy settings you had before come back and the daemon
stops. Nothing keeps capturing after you stop looking.

`q` asks what leaving should mean, with both answers on screen:

```
 QUIT   esc stays
 q   quit and turn pano off               restores the Mac's proxy settings and stops pano
 b   keep pano running in the background  pano ui reopens this window · pano off stops it
 ● pano is on · system proxy → 127.0.0.1:9091 · this window owns it
```

Closing the terminal, ctrl-c or a kill count as quitting. That is not
cleanup code in the UI: the daemon holds the window's connection and turns
itself off the moment that connection drops — the same way its watchdog
restores your proxy if the daemon itself dies. There is no way to leave
the Mac pointing at a proxy nobody is watching.

When you want pano without a window:

```sh
pano on -b        # background: capture until `pano off`
pano ui           # open a window on it — closing that one leaves pano running
pano off          # restore the proxy settings and stop
```

Scripts, Makefiles, agents' shells, piped output and `--json` get the
background behaviour automatically, so nothing that automates pano needs the
flag. `pano status` (and `pano_status` for agents) says which mode you are
in: `lifecycle ● app — closing its window turns pano off` or `○ background
— pano off stops it`. Stopping takes about a second even with a browser's
streaming connections open. The reasoning is in [ADR 0009](docs/adr/0009-app-lifecycle.md).

## Phones and tablets

```sh
pano mobile
```

Prints a QR code. Scan it with an iPhone, iPad or Android device on the same
Wi-Fi: the page it opens hands over the proxy settings (tap to copy) and the
certificate in the form the device installs, and turns each step green as the
phone gets there — first request through pano, first HTTPS connection it
trusts. Devices then show up in `pano status`, the TUI and `pano_status`;
filter their traffic with `client=<ip>`. Only the proxy port opens, only to
private addresses, only until `pano mobile off`. Details, platform notes and
the Android `network_security_config` caveat: [docs/mobile.md](docs/mobile.md).

Mobile support is **beta** — tested on iPhone; Android, iPad and the odd
network layout less so. Feedback is very welcome: what worked, what didn't,
what the page should have said —
[open an issue](https://github.com/OrRon/pano/issues).

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

### Secrets are redacted unless you ask

Captured traffic is full of API keys, cookies and bearer tokens. Every place
pano renders a header or body — `pano show`, `pano_flow`, diff, explain, HAR
export, the TUI — masks them before they leave the daemon:

- Whole headers: `Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`,
  `X-Auth-Token`, `OpenAI-Organization`, `X-Amz-Security-Token`, … (the
  Authorization scheme and cookie names stay visible).
- By shape, anywhere in text: `sk-…`, `sk-ant-…`, `AKIA…`, `AIza…`, `ghp_…`,
  `xoxb-…`, JWTs, `password` / `secret` / `token` / `api_key`-style JSON and
  form values, `user:pass@` in URLs.
- The mask keeps enough to correlate — `sk-ant-…a1b2 hash:9f3c` — so you can
  still see that two requests used the same key without seeing the key.

Redaction happens server-side, so the CLI, MCP and TUI cannot disagree. The
one way out is explicit and per call: `reveal_secrets: true` on `pano_flow`
or `pano_har` (`--reveal` on `pano show` / `pano export har`) returns the
real values **and appends a line to `~/.pano/audit.log`**. There is no
"reveal everything" mode. For an agent this means it never sees your keys by
default, and when it needs one — say, to reproduce a request with curl — it
has to ask for it, and you can see that it did.

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
- [docs/mobile.md](docs/mobile.md) — iPhone, iPad and Android behind pano: `pano mobile`, the setup page, `pano.internal`, platform notes, troubleshooting
- [docs/cli.md](docs/cli.md) — every command, filter syntax, view modes, `pano run --` environment, exit codes
- [docs/rules.md](docs/rules.md) — rule schema, actions, phases, presets, breakpoints, recipes
- [docs/config.md](docs/config.md) — `config.toml` keys and defaults, environment variables, `~/.pano` layout
- [docs/faq.md](docs/faq.md) — pinning and the never list, decrypting only your app, Firefox, Apple hosts, ECH, HTTP/3, Linux/Windows, removal, performance, comparison with other proxies
- [docs/tui-design.md](docs/tui-design.md) — the visual rules behind `pano ui`
- [docs/adr/](docs/adr/) — architecture decision records
