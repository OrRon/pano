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
- **Fast and forgetful**: single static Go binary, streaming bodies,
  zero-copy passthrough, everything captured kept in memory — nothing on
  disk, every `pano on` starts empty.

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

Updating is `brew upgrade pano`; pano tells you once a day when there is
something to upgrade to — see [Installing and updating](#installing-and-updating).

Give it to Claude Code (details in [Using pano from an AI agent](#using-pano-from-an-ai-agent)):

```sh
pano mcp install                   # or: claude mcp add --scope user --transport stdio pano -- pano mcp
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

## Using pano from an AI agent

pano's main interface is not the CLI or the TUI — it is the MCP server. `pano mcp`
speaks the [Model Context Protocol](https://modelcontextprotocol.io) over stdio, so
any agent that can run a local MCP server gets the full proxy: list and search
traffic, read exchanges in token-sized pieces, decode LLM calls, diff, replay, and
inject latency, failures, mocks and breakpoints into live traffic.

### Installing the server

**Claude Code** — one command, either form:

```sh
pano mcp install                   # runs `claude mcp add` for you, using the pano on your PATH
claude mcp add --scope user --transport stdio pano -- pano mcp
```

`--scope user` (the default) makes pano available in every project. Use
`--scope project` to commit it to a repo's `.mcp.json` for teammates, or
`--scope local` for this checkout only. Restart Claude Code (or run `/mcp`)
and you should see `pano` listed with its tools.

**Any other MCP client** that can spawn a local server: add the standard
stdio entry to its `mcpServers` config:

```json
{
  "mcpServers": {
    "pano": {
      "type": "stdio",
      "command": "pano",
      "args": ["mcp"]
    }
  }
}
```

If the client does not inherit your shell `PATH`, use the absolute path from
`which pano` as `command`.

**Clients that cannot spawn a process** (remote agents, notebooks, anything
talking HTTP): the daemon also mounts the server as Streamable HTTP on
loopback. `pano mcp --http` prints the URL, normally

```
http://127.0.0.1:9092/mcp
```

It is stateless, loopback-only and unauthenticated — fine on a single-user
Mac, and switched off with `expose_http = false` in `~/.pano/config.toml`
if other people share the machine.

**Before the tools do anything useful** you need two things done in a
terminal, because neither is exposed to agents on purpose:

```sh
pano ca install                    # trust the local CA (one password prompt)
pano on                            # or pano on -b — pano only captures while you have it on
```

`pano mcp` never starts the daemon. While pano is off every tool answers
`pano is off: … ask the user to run pano on` instead of failing the
connection, and works again the moment the daemon is back — no client
restart needed. A good first message to an agent is simply *"is pano on?"*;
it will call `pano_status` and tell you.

### What an agent can do with it

Every tool returns plain text with a `next:` hint, and every result is
rendered server-side with the same limits and secret redaction as the CLI
and the TUI. Grouped by the job you would give an agent:

**See what is happening on the wire**

| Tool | What you get |
|---|---|
| `pano_status` | Is pano on, is the system proxy pointed at it, is the CA trusted, what is being decrypted, which phones are connected, how many flows, any dropped events. Agents call this first. |
| `pano_flows` | One ~40-token line per flow, newest first, with filters: `host` glob, `path`, `method`, `status` (`500`, `4xx`, `!2xx`), `since` (`15m`, `2h`, a flow id), `content_type`, `min_bytes`, `has_error`, `state` (`held`, `failed`, `replayed`, `mocked`…), `kind` (`http`, `websocket`, `tunnel`), `client` (a phone's IP), full-text `q`. |
| `pano_tail` | Long-poll for new flows with a cursor — the agent loops it while you reproduce a bug, and it also reports requests waiting at a breakpoint. |

**Read one exchange without flooding the context window**

| Tool | What you get |
|---|---|
| `pano_flow view=summary` | The default: status, timing, headers, then a ~1.5 KB digest of each body — JSON key types and lengths, notable values, `body:` size and hash. |
| `pano_flow path=error.message` | A single value picked out of a JSON body with a gjson path (`choices.0.message.content`, `messages.#.role`). |
| `pano_flow view=schema` | The inferred shape of an unfamiliar payload. |
| `pano_flow view=truncated\|pretty\|raw` | The body itself, gated by `max_bytes` (default 4 KB, hard cap 1 MiB) — the last resort, not the first. |

The server instructions push agents down that ladder — status → flows →
summary → path → raw — so a typical investigation reads a few hundred tokens,
not a few hundred kilobytes.

**Understand LLM traffic**

`pano_flow_explain` recognises Anthropic, OpenAI (Chat and Responses) and
Gemini calls, streamed or not, and reassembles the SSE stream into what you
actually want to know: provider and model, how many messages and tools were
sent, token usage with cache reads and writes, the stop reason, the final text
and every tool call, and the error if there was one. `include=` adds the
system prompt, the full message list, thinking blocks or the raw request.
Ask *"why is my agent loop burning tokens?"* and this is the tool that answers.

**Compare and reproduce**

| Tool | What you get |
|---|---|
| `pano_flow_diff a= b=` | Status, URL, headers and a structural JSON diff of two flows (`+`, `-`, `~ path: old → new`), with volatile headers like `date` and `x-request-id` ignored by default. The fastest way to answer "what is different about the one that failed?" |
| `pano_flow_replay` | Re-sends a captured request through the proxy, optionally with a new URL, method, headers, body or `body_patch{path: value}`, and hands back the new flow id so the agent can diff it against the original. |

**Shape traffic to test how your app copes**

`pano_rule_add` installs a rule that takes effect on the next request. Presets
cover the common scenarios — `slow_network`, `fail_rate`, `offline_host`,
`timeout`, `rate_limit`, `hold` — and a full `rule{}` ([docs/rules.md](docs/rules.md))
can match on host, path, method, headers or body and delay, fail, block,
mock a response, rewrite headers or hold the exchange. Every rule can carry a
`ttl_s` so it expires on its own; `pano_rules_list`, `pano_rule_update` and
`pano_rule_remove` manage them, and the instructions tell agents to clean up
when they are done.

**Breakpoints**

A `hold` rule stops a matching request (or response) inside pano. `pano_tail`
reports it as held, and `pano_breakpoint_resume` releases it — as is, edited
(URL, method, headers, body, `body_patch`, or the response `status`), or
dropped. *"Hold the next POST to /v1/orders so I can change the amount before
it goes out"* is one rule and one resume.

**Export, capture control, decryption policy**

| Tool | What you get |
|---|---|
| `pano_har` | Export the current session (or a filtered subset) to a HAR 1.2 file, or import one — the file path comes back, never the contents. |
| `pano_capture` | Start or stop recording, clear the session, or name a new one — without touching system settings. |
| `pano_decrypt` | Switch between decrypting `all` hosts, `only` a list, or `off`; add hosts to the `never` list; see which hosts recently refused pano's certificate (pinned apps) and add them all with `@rejected`. |

**Resources and prompts**

Alongside the tools, the server publishes `pano://ca.pem` (the public root
certificate, for `SSL_CERT_FILE` / `NODE_EXTRA_CA_CERTS`), `pano://status`,
`pano://flows/latest`, `pano://flows/{id}` and
`pano://flows/{id}/raw.request|response`. Two prompts package whole
workflows: `debug_failing_request` walks the escalation ladder for one
failing flow and ends with a root cause and a fix; `simulate_conditions`
turns *slow / flaky / offline / timeout / rate_limited / hold* into a rule,
tails the traffic while you exercise the app, and reports resilience bugs.

### What an agent cannot do

Three things stay in your terminal on purpose:

- **Trusting the CA.** `pano ca install` has no MCP equivalent. The password
  prompt is yours.
- **Turning pano on.** `pano mcp` never starts the daemon; nothing captures
  unless you ran `pano on`.
- **Opening the proxy to the network.** `pano mobile` has no tool; agents can
  only see which devices connected.

`pano_system_proxy` — routing the whole Mac through pano — does exist, but it
refuses without `confirm: "yes"`, is annotated destructive, and every toggle
is written to `~/.pano/audit.log`. Agents are told to prefer
`pano run -- <cmd>` for one-off commands. And secrets are redacted in every
result unless the agent passes `reveal_secrets: true` on a single call, which
is audited too — see [Secrets are redacted unless you ask](#secrets-are-redacted-unless-you-ask).

The full tool catalog, inputs, defaults, token budgets and a worked example are
in [docs/mcp.md](docs/mcp.md); what the handshake and tool metadata look like
on the wire is in [docs/mcp-protocol.md](docs/mcp-protocol.md).

## Installing and updating

| | |
|---|---|
| **Homebrew** (macOS) | `brew install orron/tap/pano` · later `brew upgrade pano` |
| **Go** | `go install github.com/orron/pano/cmd/pano@latest` — same command upgrades |
| **Tarball** (macOS, Linux) | download from [Releases](https://github.com/OrRon/pano/releases), verify with `shasum -a 256 -c checksums.txt --ignore-missing`, put `pano` on your `PATH` |

pano never updates itself. Once a day, when you run it in a terminal, it asks
GitHub whether a newer release exists and prints one line after the command's
output — `↑ pano 0.3.0 is available (you have 0.2.0) · brew upgrade pano` —
with the command that matches how you installed it. `pano version --check`
asks right now. Turn the check off with `PANO_NO_UPDATE_CHECK=1`,
`DO_NOT_TRACK=1` or `[updates] check = false` in `~/.pano/config.toml`; it is
already off in scripts (`--json`, no terminal, `CI`) and inside `pano mcp`.
Exactly what that request contains is in [Safety](#the-update-check) below.

Uninstalling: `pano ca uninstall` first (keychain trust is not a file, so no
package manager removes it), then `brew uninstall pano` — `--zap` also
deletes `~/.pano` — or delete the binary and `rm -rf ~/.pano`.

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

### The update check

This is the only request pano ever makes for itself: once every 24 hours, one
GET to `https://api.github.com/repos/OrRon/pano/releases/latest`, carrying
pano's version in the `User-Agent` and nothing else — no identifier, no OS,
no architecture, no cookies. It goes direct, not through the system proxy,
so it is never recorded as a flow and never loops through pano. A failure is
silent; a success is cached in `~/.pano/update-check.json`. It only runs
when a person will see the answer (a terminal, no `--json`, not in CI, never
inside `pano mcp`, `pano daemon` or the watchdog) and it only prints — it
never downloads or installs anything. `PANO_NO_UPDATE_CHECK=1`,
`DO_NOT_TRACK=1`, `[updates] check = false`, or building with
`-X github.com/orron/pano/internal/update.Default=off` turn it off ([ADR
0010](docs/adr/0010-updates-notify-only.md)).

## Performance

Apple M-series, 64 concurrent clients, small JSON responses, capture on
(`bench/run.sh`): **~71k req/s through pano vs ~148k direct, p50 added latency
≈ 0.4 ms, p99 ≈ 3.4 ms**. Bodies stream through unbuffered (SSE tokens arrive
live); captures are kept in memory only, so the proxy never waits on
storage.

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
