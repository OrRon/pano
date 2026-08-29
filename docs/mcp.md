# MCP server

`pano mcp` exposes the proxy to AI agents over the
[Model Context Protocol](https://modelcontextprotocol.io). It is a thin
client of the daemon's control API: every tool maps to one HTTP call, and all
rendering, budgets and redaction happen inside the daemon (see
[architecture.md](architecture.md)).

Server name: `pano`. Tool prefix: `pano_`. Resource scheme: `pano://`.
For what happens on the wire — spawn, handshake, tool metadata, the
off/on states — see [mcp-protocol.md](mcp-protocol.md).

## Registering

**Claude Code** (stdio, recommended):

```sh
claude mcp add --scope user --transport stdio pano -- pano mcp
# or let pano run that for you (uses the pano binary on your PATH):
pano mcp install            # --scope user|project|local
```

**`.mcp.json`** (project scope, or any client that reads this format):

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

**Streamable HTTP** (for clients that cannot spawn a process): the daemon
mounts the same server at

```
http://127.0.0.1:9092/mcp
```

`pano mcp --http` prints the exact URL. The transport is stateless and
loopback-only; it is controlled by `[mcp] expose_http` and `[proxy] mcp_port`
in [config.md](config.md). There is no authentication on this port beyond
the loopback bind, so disable it (`expose_http = false`) if other local
users share the machine.

`pano mcp` **never starts the daemon** — pano is only on between `pano on`
(or `pano start`) and `pano off` (or `pano stop`, or quitting the UI that
`pano on` opened — ADR 0009), and only the user runs those. Claude Code spawns `pano mcp` once per session, so the server stays up
regardless and, while the daemon is down, every tool returns `isError` with
*"pano is off: … ask the user to run `pano on`"*; resources fail with the same
message. Each call dials `~/.pano/pano.sock` fresh, so the tools work again
the moment the daemon is back — no client restart or reconnect needed. See
[ADR 0006](adr/0006-mcp-follows-the-daemon.md). Nothing but protocol
messages is written to stdout; logs go to stderr.

## Server instructions

Sent to the client at `initialize` (kept under 2 KB because Claude Code
truncates longer instructions):

> pano is a local HTTPS proxy that records this Mac's traffic, including LLM API calls, and lets you shape it.
> Workflow: pano_status → pano_flows (filters; ONE line per flow) → pano_flow with view=summary or path=… → only then view=raw with an explicit max_bytes.
> For api.anthropic.com / api.openai.com flows use pano_flow_explain instead of reading bodies: it reassembles streams into the final message, tool calls and token usage.
> To test an app under bad conditions use pano_rule_add presets (slow_network, fail_rate, offline_host, timeout, rate_limit, hold) with ttl_s so they expire; remove rules when done.
> pano_tail polls for new flows with a cursor — loop it while a user reproduces something.
> Secrets (API keys, cookies, bearer tokens) are redacted by default; pass reveal_secrets=true only when the user needs the actual value.
> pano_decrypt controls which HTTPS hosts are decrypted (mode all|only|off plus only/never lists); hosts that refuse pano's certificate show up in pano_status as "rejected" — add them to never if the user wants that app left alone, never silently.
> pano_system_proxy CHANGES macOS SYSTEM SETTINGS — only call it when the user explicitly asks. Installing the CA is terminal-only (pano ca install).
> pano only runs while the user has it on: if a tool answers "pano is off", ask the user to run pano on in a terminal, then retry — you cannot start it yourself.
> Flow ids are short strings like "f8k2q"; every result ends with a next: hint.

## Tool catalog

All tools return a single text content block. Errors come back as
`isError: true` text with a `next:` hint rather than protocol errors.
Annotations: **RO** = `readOnlyHint: true, openWorldHint: false`;
**M** = `destructiveHint: false, openWorldHint: false` (with
`idempotentHint` as noted). `alwaysLoad` marks `_meta["anthropic/alwaysLoad"]`;
`200k` marks `_meta["anthropic/maxResultSizeChars"] = 200000`.

| Tool | Purpose | Key inputs (defaults) | Output | Annotations |
|---|---|---|---|---|
| `pano_status` | Daemon state. Call first. | none | version, pid, uptime, proxy addr, MCP HTTP addr, system proxy on?, CA trusted?, capturing, session, flow counts + last id, rules/held/conns, redaction, the full decrypt policy (mode, `only`, `never`, rejected hosts), the mobile listener (off, or address + every device seen with proxy/https state), dropped-events warning | RO, alwaysLoad |
| `pano_capture` | Control recording (does not touch system settings). | `action` = `start`\|`stop`\|`clear`\|`session`; `name` (required for `session`) | `capture <action> → capturing=… session=… flows=…` | M, idempotent |
| `pano_flows` | List / search flows, newest first, one line each. | filters: `q`, `host` (glob), `path` (prefix/glob), `method[]`, `status` (`500`, `4xx`, `400-499`, `!2xx`), `since` (`15m`, `2h`, RFC3339, flow id), `until`, `content_type` (`json\|sse\|html\|js\|css\|img\|bin\|text` or MIME prefix), `min_bytes`, `has_error`, `tag`, `rule`, `state` (`all\|held\|active\|done\|failed\|replayed\|mocked\|blocked`), `kind` (`http\|websocket\|tunnel`), `client` (an IP from `pano_status` devices, or `remote` for every non-loopback client), `limit` (50, max 200), `cursor` | fixed-column table `id time meth host path st dur up down type flags`, footer `N of M flows · cursor=…`, `next:` pointing at the newest row | RO, alwaysLoad |
| `pano_flow` | Inspect one flow. | `id`; `part` (`both`); `view` (`summary`; `schema`, `truncated`, `pretty`, `raw`); `path` (gjson/JSONPath into a JSON body); `max_bytes` (4096, cap 1 MiB); `headers` (true); `reveal_secrets` (false, audited) | status line, `error:`/`rules:`/`timing:` lines, then `== request ==` / `== response ==` with headers and a rendered body that starts with a `body:` header line | RO, alwaysLoad, 200k |
| `pano_flow_diff` | Compare two flows. | `a`, `b`; `part` (`response`); `path`; `ignore_headers` (defaults: date, age, etag, x-request-id, cf-ray, set-cookie, content-length, traceparent, x-amzn-trace-id, x-amz-request-id); `max_changes` (50) | `~ status`, `~ url`, header diff, structural JSON body diff (`+`, `-`, `~ path: old → new`) or a text diff | RO |
| `pano_flow_replay` | Re-send a captured request through the proxy. | `id`; `url`, `method`, `set_headers{}`, `remove_headers[]`, `body`, `body_patch{path: value}`, `follow_rules` (true), `timeout_ms` (30000) | `replayed <id> → new flow <id2>: status, size, duration`, then the response summary; `next: pano_flow_diff a=<id> b=<id2>` | M, not idempotent |
| `pano_flow_explain` | LLM-traffic digest. | `id`; `include[]` (`final,usage,tools,stop,errors`; add `system`, `messages`, `thinking`, `request`); `max_chars` (4000); `provider` (`anthropic\|openai-chat\|openai-responses\|gemini`, auto-detected) | `provider/model/stream/status` line, `request:` shape, `usage:` + `stop:`, `final:` content/tool calls, `errors:`; non-LLM flows fall back to a generic summary | RO |
| `pano_tail` | Long-poll for new completed flows. | `since` (`now` or a cursor); `wait_ms` (10000, max 25000); same filters as `pano_flows` | table of new rows or `no new flows`, `HELD: n request(s) …` when breakpoints are waiting, `cursor=…`, `next: pano_tail since=<cursor>` | RO |
| `pano_rules_list` | List live rules. | `presets` (false) | one line per rule (`on/off id "name" match=… actions=… hits=… ttl=…`), optionally the preset catalog with default params | RO |
| `pano_rule_add` | Add a rule that takes effect immediately. | either `preset` + `params{}` or `rule{}` ([rules.md](rules.md)); `name`; `ttl_s` (recommended 600) | `added <rule line>` + the normalised rule JSON; `next: pano_rule_remove id=…` | M, not idempotent |
| `pano_rule_update` | Toggle, rename or replace a rule. | `id`; `enabled`; `name`; `rule{}` (full replacement) | `updated <rule line>` | M, idempotent |
| `pano_rule_remove` | Remove one rule or all. | `id` or `all=true` | `removed <id>` / `removed N rules` | M, idempotent |
| `pano_breakpoint_resume` | Release or drop a held exchange. | `id`; `action` (`resume`\|`drop`); edits: `url`, `method`, `set_headers{}`, `remove_headers[]`, `body`, `body_patch{}`, `status` (response phase) | `resume <id>` / `drop <id>`; `next: pano_flow id=…` | M, not idempotent |
| `pano_har` | Export or import HAR 1.2. | `action` (`export`\|`import`); `path` (absolute); `reveal_secrets` (false); filters as `pano_flows` (export) | `export N flows (bytes) path` — never the file contents | M, 200k |
| `pano_decrypt` | Which HTTPS hosts are decrypted. | `action` = `status`\|`mode`\|`add`\|`remove`; `mode` (`all`\|`only`\|`off`); `list` (`only`\|`never`); `hosts[]` (bare host covers subdomains; globs; `"@rejected"` with `list=never` adds every host that recently refused the certificate) | `decrypt: <mode>` then `only:` / `never:` with every entry, and `rejected recently (…): host ×N` with the `@rejected` hint when present | M, idempotent |
| `pano_system_proxy` | **Changes macOS system settings.** | `enabled`; `confirm` must be `"yes"` | `system proxy enabled=… set_by_pano=… <detail>` | `destructiveHint: true, openWorldHint: true` |

Export only covers flows currently in the in-memory ring (the last 10 000
by default), not the whole SQLite history.

### Flags column

`pano_flows` rows end with a comma-separated flags list: `llm` (known LLM
host), `stream` (`text/event-stream`), `err` (status ≥ 400 or transport
error), `held`, `active`, `replay`, one entry per rule action that hit
(`mock`, `delay`, `block`, …), `trunc` (a body was cut at
`[capture] max_body_bytes`), then the flow's tags. A second line
`↳ <error>` follows rows with a transport error.

## The escalation ladder

Read the least you can, then go deeper only where the summary points:

```
pano_status
  └─▶ pano_flows host=… status=!2xx since=15m          (one line per flow)
        └─▶ pano_flow id=… view=summary                (types, lengths, notable fields; ~1.5 KB)
              ├─▶ pano_flow_explain id=…               (LLM hosts: final message, usage, errors)
              ├─▶ pano_flow id=… part=response path=error.message   (one value)
              ├─▶ pano_flow id=… view=schema           (shape of an unfamiliar payload)
              └─▶ pano_flow id=… view=raw max_bytes=8192            (last resort)
```

### Worked example

The user asks: *"why did my app get a 429 from OpenAI just now?"*
(Outputs below are illustrative and abbreviated.)

**1. `pano_flows`** `{"host":"api.openai.com","status":"!2xx","since":"15m"}`

```
id     time     meth host                       path                                  st  dur     up     down   type flags
2k5    14:03:01 POST api.openai.com             /v1/chat/completions                  429 92ms    1.4k   210    json llm,err
1 of 1 flows
next: pano_flow_explain id=2k5
```

**2. `pano_flow`** `{"id":"2k5","view":"summary"}`

```
2k5 POST https://api.openai.com/v1/chat/completions → 429 Too Many Requests  HTTP/2.0 92ms  client 127.0.0.1:61234
timing: ttfb 88ms total 92ms (conn reused)

== request ==
Authorization: Bearer sk-…Wq2A hash:7c1e
Content-Type: application/json
User-Agent: openai-node/4.52.0
body: application/json 1402B sha256:9ab31c02
json object (5 keys)
model: str "gpt-4.1"
messages: array[3] of object{role, content}
temperature: float 0.2
stream: bool false
max_tokens: int 1024

== response ==
Content-Type: application/json
Retry-After: 2
body: application/json 210B sha256:5f0a2d11
json object (1 keys)
error: object[4] = {"message":"Rate limit reached for gpt-4.1 in organization org-… on tokens per min (TPM): Limit 30000, Used 29500, Requested 1200…
next: pano_flow_explain id=2k5
```

**3. `pano_flow`** `{"id":"2k5","part":"response","path":"error.message"}` — one
value, no noise. (`part=response` matters: with `part=both` the request body
has no `error.message` and that side reports `path not found` with the
request's top-level keys instead.)

```
== response ==
body: application/json 210B sha256:5f0a2d11 path:error.message
Rate limit reached for gpt-4.1 in organization org-… on tokens per min (TPM): Limit 30000, Used 29500, Requested 1200. Please try again in 2s.
```

**4. `pano_flow_explain`** `{"id":"2k5"}`

```
provider: openai-chat  model: gpt-4.1  stream: no  status: 429
request: 3 messages (u2/s1) · max_tokens 1,024 · temperature 0.2 · stream no
usage: -
errors: HTTP 429: tokens: Rate limit reached for gpt-4.1 in organization org-… Please try again in 2s.
next: pano_flow id=2k5 part=request view=summary  (or include=messages)
```

The agent now knows the limit, the usage, the `Retry-After`, and that the
request itself was well-formed — without ever reading a body. A successful
streamed call looks like this:

```
provider: anthropic  model: claude-opus-5  stream: yes  status: 200
request: system 155 chars · 4 messages (u3/a1) · 6 tools (Read, Edit, Bash, Grep, Glob, …(+1)) · max_tokens 8,192 · temperature 0.2 · stream yes · thinking budget 2,048
usage: in 4,812 (cache_read 4,100, cache_write 0) · out 356 · total 5,168 · stop: tool_use
final:
  [thinking] (154 chars, hidden — include=thinking to show)
  [text] "I'll read the store package first." (34 chars)
  [tool_use] Read {"file_path":"/home/dev/app/internal/store/store.go","limit":120}
errors: none
```

## Resources

| URI | MIME | Content |
|---|---|---|
| `pano://ca.pem` | `application/x-pem-file` | the root certificate (hand it to tools via `SSL_CERT_FILE` / `NODE_EXTRA_CA_CERTS`) |
| `pano://status` | `text/plain` | same text as `pano_status` |
| `pano://flows/latest` | `text/plain` | the 50 most recent flows, one line each |
| `pano://flows/{id}` | `text/markdown` | one flow rendered with `view=summary` |
| `pano://flows/{id}/raw.{request\|response}` | `text/plain` or `application/octet-stream` (blob) | the decoded body, up to 1 MiB |

## Prompts

| Prompt | Arguments | What it does |
|---|---|---|
| `debug_failing_request` | `id` (required) | Walks the ladder for one failing flow: summary (or explain for LLM hosts) → find a similar 2xx flow → `pano_flow_diff` → fetch only the relevant `path` → root cause in two sentences + a fix, offering `pano_flow_replay`. |
| `simulate_conditions` | `host`, `scenario` = `slow\|flaky\|offline\|timeout\|rate_limited\|hold` (required) | Maps the scenario to a preset (`slow_network{ms:3000,jitter_ms:1000}`, `fail_rate{rate:0.3,status:503}`, `offline_host`, `timeout{after_ms:30000}`, `rate_limit{every_n:3,status:429,retry_after:2}`, `hold{on:"request"}`), adds it with `ttl_s=600`, loops `pano_tail` while the user exercises the app, summarises resilience bugs, removes the rule. |

## Token budgets

| Surface | Budget |
|---|---|
| `pano_flows` row | ~40 tokens; `limit` ≤ 200 |
| `pano_flow view=summary` | ≤ ~1.5 KB body digest per part (+ headers) |
| `pano_flow view=schema` | ≤ ~2 KB per part |
| `pano_flow view=truncated\|pretty\|raw` | `max_bytes` (default 4096; hard cap 1 048 576) |
| `pano_flow_explain` | `max_chars` (default 4000); text previews 200 chars, thinking 800, tool input 200 |
| `pano_flow_diff` | `max_changes` (default 50) then `… and N more` |
| `pano_tail` | `wait_ms` ≤ 25 000 so the call returns inside Claude Code's ~60 s HTTP timer |
| Server instructions / tool descriptions | < 2 KB each (Claude Code truncates) |
| Tool results | Claude Code caps MCP output at ~25k tokens; `pano_flow` and `pano_har` declare `maxResultSizeChars = 200000` |

Strings longer than `[views] string_truncate` (200) are elided in summary and
schema views; binary bodies are replaced by
`(binary; use pano export har or `pano show --out FILE` to save)`.

## Redaction and `reveal_secrets`

Redaction is on by default (`[redaction] enabled`) and applies to every
rendered header, body view, diff, explain digest and HAR export:

- Masked wholesale: `Authorization`, `Proxy-Authorization`, `Cookie`,
  `Set-Cookie`, `X-Api-Key`, `Api-Key`, `X-Goog-Api-Key`, `X-Auth-Token`,
  `OpenAI-Organization`, `X-Amz-Security-Token`, `X-Csrf-Token`.
  Authorization keeps its scheme, cookies keep their names.
- Masked by shape anywhere in text: `Bearer …`, `sk-…`, `sk-ant-…`,
  `sk-proj-…`, `AKIA…`, `AIza…`, `gh[pousr]_…`, `xox[baprs]-…`, JWTs
  (`eyJ….….…`), `key-…`, `password|secret|token|access_token|refresh_token|client_secret|api_key|apikey|private_key`
  values in JSON and form bodies, and `user:password@` in URLs.
- The mask is `<known prefix>…<last 4> hash:<4 hex of sha256>`, e.g.
  `sk-ant-…a1b2 hash:9f3c`, so two occurrences of the same secret still
  match each other.

`reveal_secrets: true` on `pano_flow` or `pano_har` returns the real values
and appends `reveal_secrets flow=<id>` / `reveal_secrets har=<path>` to
`~/.pano/audit.log`. Nothing else in the API can un-redact.

## Safety notes

- Opening the proxy to the network (`pano mobile`) has no MCP tool, on
  purpose; `pano_status` reports whether it is on and which devices have
  connected, and the agent can point the user at the command.

- **`pano_system_proxy`** reroutes every application on the Mac. It refuses
  without `confirm: "yes"`, is annotated `destructiveHint`, and every toggle
  is written to `audit.log`. The description tells the agent to prefer
  `pano run -- <cmd>` for single commands.
- **`pano_decrypt`** may change the decrypt mode and lists without a
  confirmation gate: narrowing is always safe, and widening only matters
  once the user has trusted the CA in a terminal. Every change is written to
  `audit.log` as `decrypt source=mcp …`, and rejected hosts are only ever
  suggested — the tool description says so.
- **CA installation is not exposed over MCP.** The only ways to trust the
  root are `pano ca install` and `pano on` in a terminal.
- The CA private key is never served by any tool, resource or endpoint;
  `pano://ca.pem` is the public certificate only.
- Rules affect live traffic immediately; the instructions and `next:` hints
  push agents to set `ttl_s` and to remove rules when done.
- The HTTP transport on `127.0.0.1:9092` is unauthenticated (loopback
  only); the stdio transport inherits the user's permissions on
  `~/.pano/pano.sock` (mode 0600).
