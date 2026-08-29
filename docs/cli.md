# CLI reference

`pano` is a single binary. Most commands talk to the daemon over
`~/.pano/pano.sock`; `pano start`, `pano on` and `pano run` start the daemon
if it is not running (`pano mcp` deliberately does not — see
[mcp.md](mcp.md)). Output is coloured on a TTY and plain
otherwise; `--json` prints the control-API response verbatim.

```
pano [command] [flags]
```

## `pano ui`

Interactive terminal UI over the same control API: live flow list, detail
tabs (Summary / Request / Response / Explain / Diff), JSON path selection,
replay, marking two flows for diff, rules and held-request drawers. Starts the
daemon if needed. Aliases: `pano tui`, `pano watch`. Keys and layout are
documented in [tui-design.md](tui-design.md).

## Global flags

| Flag | Meaning |
|---|---|
| `--json` | output JSON (also disables colour) |
| `--sock PATH` | control socket (default `~/.pano/pano.sock`, or `$PANO_SOCK`) |
| `--no-color` | disable ANSI colour (also honours `NO_COLOR`) |
| `-q, --quiet` | less output |
| `-v, --verbose` | more output |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | error (printed as `error: …` on stderr) |
| `2` | `pano doctor` found problems |
| `3` | the daemon is not running (any command that needs it, and `pano status`) |

## Lifecycle

### `pano start`

Start the proxy daemon detached (logs to `~/.pano/daemon.log`). Prints
`pano started (pid N) — proxy 127.0.0.1:9091` and reminds you to run
`pano ca install` if the CA is not trusted yet. Idempotent.

| Flag | Meaning |
|---|---|
| `--port N` | proxy port (default from config: 9091) |
| `--mcp-port N` | Streamable HTTP MCP port (default from config: 9092) |
| `--bind ADDR` | bind address (default 127.0.0.1) |
| `--foreground` | run in this process, log to stderr (Ctrl-C to stop) |

```sh
pano start
pano start --foreground --port 9099      # dev daemon
```

### `pano stop`

Graceful shutdown via `POST /v1/shutdown`: restores the system proxy if pano
set it, flushes the store, removes the pid file.

### `pano status`

Daemon, capture, CA and system-proxy state. Exit 3 when not running.

```
● pano 0.1.0  pid 4242  up 1m05s
  proxy        127.0.0.1:9091
  mcp          stdio: pano mcp   http: http://127.0.0.1:9092/mcp
  system proxy ● ON  2 services (Wi-Fi, Thunderbolt Bridge) → 127.0.0.1:9091
  ca           ● trusted  ~/.pano/ca.pem
  capture      ● capturing  session "a1b2c3d4"  312 flows in memory, 4021 total
  rules        1 (1 enabled)  held 0  active conns 3
```

### `pano on` / `pano off`

`on` routes the Mac's HTTP and HTTPS traffic through pano by setting the
system proxy of every enabled network service (previous settings are
snapshotted and restored by `off`, `stop`, or the watchdog if the daemon
dies). The first time, it offers to run `pano ca install`. In a
non-interactive shell it refuses unless the CA is already trusted or `--yes`
is given.

| Flag | Meaning |
|---|---|
| `-y, --yes` | do not prompt (installs the CA if needed) |

On success pano's mascot wakes up next to the status line (a short glance
around on a colour terminal; one static frame when piped, with `--json`,
`--quiet` or `NO_COLOR`):

```
  ╭─────╮
  │ ◉ ◉ │  ✓ system proxy ON → 127.0.0.1:9091  Wi-Fi
  ╰─────╯    watch with pano ui · pano tail   turn off with pano off
```

`off` restores the snapshot and then stops the daemon, so nothing keeps
running — and the MCP tools report "pano is off" — until the next `on`.
If the daemon is dead but `~/.pano/sysproxy.json` exists, `off` restores it
directly.

| Flag | Meaning |
|---|---|
| `--keep-daemon` | restore the proxy settings but leave the daemon running (for `pano run --` or MCP without the system proxy) |

### `pano doctor`

Checks: daemon reachable, config parses, CA files present, CA trusted, proxy
port free (when not running), stale system-proxy state, dropped events.
Exit 2 when anything fails.

### `pano version`

`pano 0.1.0 (abc1234, 2026-08-28T00:00:00Z)`; `--json` for a map.

## Certificate authority

### `pano ca install`

Trust the root in the **login keychain** via `security add-trusted-cert`
for the TLS server policy only (`-p ssl -p basic`; macOS shows one password
prompt). Prints the Firefox reminder (`security.enterprise_roots.enabled`).
On other OSes prints manual instructions.

| Flag | Meaning |
|---|---|
| `--system` | install into the System keychain for all users (runs `sudo`) |

### `pano ca uninstall`

Delete every certificate with pano's subject from the login keychain, then
sweep any `pano Root CA (…)` left behind by an earlier rotation.

### `pano ca status`

Whether the root is present and trusted (`security verify-cert`), when it
expires, and a warning once fewer than 30 days remain or the root was just
regenerated. `--json` adds `not_after` and `warning`.

### `pano ca path`

Print `~/.pano/ca.pem` (creating the CA if needed). Handy for
`curl --cacert "$(pano ca path)"`.

### `pano ca reset`

Remove the current root from the login keychain (including any left by
earlier rotations), delete it, its key, the shared leaf key and the cert
cache, and generate a new root. Asks for confirmation. Also the way to renew a root
that is about to expire (an already-expired root is replaced automatically on
the next start). Re-run `pano ca install`
afterwards.

## Looking at traffic

### Filter flags (shared by `flows`, `tail`, `export har`)

| Flag | Matches |
|---|---|
| `--q TEXT` | full-text search over URL, headers and decoded text bodies (SQLite FTS5: whitespace-separated terms are ANDed, `foo*` is a prefix; over the in-memory ring, `tail` matches substrings of URL/headers/error) |
| `--host GLOB` | host glob: `api.openai.com`, `*.googleapis.com` (`*` any run, `?` one char, case-insensitive) |
| `--path P` | path prefix (`/v1/`), glob (`/v1/*/models`) |
| `-m, --method GET,POST` | one or more methods |
| `--status SPEC` | `500`, `4xx`, `400-499`, `!2xx`, `200\|204` |
| `--since T` / `--until T` | RFC3339, relative (`15m`, `2h`, `1d`), or (since only) a flow id |
| `--type CLASS` | `json\|sse\|html\|js\|css\|img\|bin\|text\|font\|form\|xml\|media` or a MIME prefix such as `application/` |
| `--min-bytes N` | request + response size ≥ N |
| `--errors` | status ≥ 400 or a transport/TLS error |
| `--tag T` | flows tagged by a `tag` rule (or `imported`, `bypass`) |
| `--rule ID` | flows a given rule (id or name) acted on |
| `--state S` | `all\|held\|active\|done\|failed\|replayed\|mocked\|blocked` |
| `--kind K` | `http\|websocket\|tunnel` |
| `--session ID` | session id (see `pano session ls`) |

### `pano flows` (alias `ls`)

List captured flows, newest first. Extra flags: `-n, --limit N` (default 50,
max 200) and `--cursor C` (printed at the bottom of a page as
`next page: --cursor before:…`).

```sh
pano flows --host api.anthropic.com --since 10m
pano flows --status !2xx --errors -n 20
pano flows --q "rate limit" --type json
pano ls --state held
```

Row format:

```
id     time     meth host                        path                                      st  dur     up     down   type  flags
2k7    14:03:12 POST api.anthropic.com           /v1/messages                              200 4.21s   8.1k   22.4k  sse   llm,stream
2k5    14:03:01 POST api.openai.com              /v1/chat/completions                      429 92ms    1.4k   210    json  llm,err
2k4    14:02:58 TUN  gateway.icloud.com          /                                         -   1.2s    3.1k   9.8k   tunnel bypass
```

`meth` shows `TUN` for undecrypted tunnels and `WS` for WebSockets; `st` is
`…` while a flow is still active; asset rows (js/css/img/font) are dimmed.
Flags: `llm`, `stream`, `err`, `held`, `active`, `replay`, rule actions that
hit (`mock`, `delay`, …), `trunc`, then tags. Transport errors appear on a
second line (`↳ client rejected pano certificate …`).

### `pano tail`

Follow flows live over the `/v1/events` SSE stream (Ctrl-C to stop). By
default shows completed (`done`) and `held` flows; held ones are prefixed
`HELD`. Accepts the filter flags plus:

| Flag | Meaning |
|---|---|
| `--all` | show every event (started, headers, done) |

```sh
pano tail --host '*.openai.com'
pano tail --errors --json | jq .host
```

### `pano show <id>`

One flow: status line, `error:`/`rules:`/`timing:` lines, then request and
response headers and a rendered body.

| Flag | Meaning |
|---|---|
| `--part request\|response\|both` | which side (default `both`) |
| `-b, --body VIEW` | `summary` (default), `schema`, `truncated`, `pretty`, `raw` |
| `--path P` | select a JSON value, e.g. `choices.0.message.content`, `$.usage`, `messages.#.role` |
| `--max-bytes N` | body budget for truncated/pretty/raw (default 4096, cap 1 MiB) |
| `--headers` | include headers (default true; `--headers=false` to omit) |
| `--reveal` | show secrets unredacted (written to `audit.log`) |
| `--out FILE` | write the decoded body (response unless `--part request`) to FILE instead of rendering |

```sh
pano show 2k5
pano show 2k5 --part response --path error.message
pano show 2k7 -b schema
pano show 2k7 -b raw --max-bytes 65536 --part response
pano show 2k9 --out image.png
```

#### View modes

| View | What you get |
|---|---|
| `summary` | type-aware digest: JSON key list with types/lengths and `notable:` values (`error`, `usage`, `*_id`, `status`, …); SSE event counts and first/last event; HTML title + visible text; form fields; capped at ~1.5 KB |
| `schema` | inferred shape: `{ role: "user"\|"assistant", created_at: str<date-time>, usage?: { … } }` (`?` = optional key); per-event-name shapes for SSE; ~2 KB |
| `truncated` | first 75 % and last 25 % of the budget with `… [skipped N bytes] …`; JSON is pretty-printed first when it fits 4× the budget |
| `pretty` | indented JSON / `key: value` forms / decoded text, head+tail truncated to the budget |
| `raw` | decoded bytes, head only, up to the budget |

Every body starts with `body: <mime> <wire bytes> [gzip→<decoded bytes>]
sha256:<8 hex> [truncated to X of Y] [path:<p>]`. Binary bodies are never
inlined.

### `pano explain <id>`

Digest an LLM API call (Anthropic Messages, OpenAI Chat Completions and the
many compatible APIs, OpenAI Responses, Gemini generateContent). Streams are
reassembled into the final message; non-LLM flows get a generic summary.

| Flag | Meaning |
|---|---|
| `--include LIST` | sections: `final,usage,tools,stop,errors` (default) plus `system`, `messages`, `thinking`, `request` |
| `--max-chars N` | budget (default 4000) |
| `--provider P` | force `anthropic\|openai-chat\|openai-responses\|gemini` |

```sh
pano explain 2k7
pano explain 2k7 --include final,usage,messages,thinking
```

### `pano diff <a> <b>`

Compare two flows: status, URL, headers (noisy ones ignored by default) and a
structural JSON body diff (`+ added`, `- removed`, `~ path: old → new`).

| Flag | Meaning |
|---|---|
| `--part request\|response\|both` | default `response` |
| `--path P` | restrict the body diff to a JSON path |
| `--max-changes N` | default 50 |
| `--ignore-headers LIST` | override the default ignore list |

```sh
pano diff 2k3 2k5 --part both
```

### `pano replay <id>`

Re-send a captured request through the proxy itself, so the replay is
captured as a new flow (flagged `replay`) and live rules apply.

| Flag | Meaning |
|---|---|
| `--url URL` | override the URL |
| `-X, --method M` | override the method |
| `-H, --header 'Name: value'` | set a header (repeatable) |
| `--remove-header LIST` | remove headers |
| `--body STR\|@file` | replace the body |
| `--set path=value` | patch a JSON body field (repeatable; values that parse as JSON numbers/booleans/objects are coerced) |
| `--no-rules` | bypass live rules for this replay |
| `--timeout MS` | default 30000 |

```sh
pano replay 2k5 -H 'Authorization: Bearer sk-…' --set max_tokens=64
pano replay 2k5 --body @fixed.json --no-rules
```

## Rules and breakpoints

See [rules.md](rules.md) for the schema, presets and recipes.

### `pano rules ls`

One line per rule: `● r_7k3q2 name  host=… path=… → delay,set_header p=0.30 ttl=600s  hits=12`.

### `pano rules add`

Three ways to specify a rule:

| Flag | Meaning |
|---|---|
| `--preset NAME` | `slow_network\|fail_rate\|offline_host\|timeout\|rate_limit\|hold` |
| `--param key=value` | preset parameter (repeatable; numbers and booleans are coerced) |
| `--file rule.json` | a full rule document |
| `--host GLOB`, `--path P`, `--method LIST`, `--status SPEC` | match fields for `--action` rules |
| `--action SPEC` | repeatable mini-syntax: `delay:MS` · `set_header:Name=value` · `remove_header:Name` · `mock:STATUS[:body]` · `block[:reset\|timeout]` · `redirect:URL` · `throttle:KBPS` · `breakpoint[:request\|response]` · `tag:name` |
| `--name NAME` | rule name |
| `--ttl SECONDS` | auto-remove after N seconds |

```sh
pano rules add --preset slow_network --param host=api.openai.com --param ms=3000 --ttl 600
pano rules add --preset fail_rate --param host=api.anthropic.com --param rate=0.5
pano rules add --host api.openai.com --action delay:2000 --action set_header:X-Debug=1
pano rules add --host api.example.com --path /v1/orders --action 'mock:503:{"error":"down"}' --ttl 300
pano rules add --file rule.json
```

`set_query`, `rewrite_body` and `mock_every_n` need `--file` (or MCP).

### `pano rules rm <id>` / `pano rules rm --all`

### `pano rules toggle <id> [--on|--off]`

Flip a rule's enabled state (or force it with `--on`/`--off`).

### `pano rules presets`

List presets with their parameters and defaults.

### `pano bp ls`

Held requests/responses: `HELD 2k8  request POST https://…  age 4.2s  rule r_7k3q2`.

### `pano bp resume <id>`

Release a held exchange, optionally edited.

| Flag | Meaning |
|---|---|
| `-H, --header 'Name: value'` | set a header (repeatable) |
| `--body STR\|@file` | replace the body |
| `--status N` | override the status (response phase only) |

### `pano bp drop <id>`

Drop it; the client sees a connection reset.

## Sessions, bypass, HAR

### `pano session ls | new <name> | rm <id> | clear`

Sessions group flows; `new` ends the current one and becomes current;
`rm` deletes a session and its flows (not the current one); `clear`
forgets in-memory flows only (persisted history stays).

### `pano bypass ls | add <glob>... | rm <glob>...`

Hosts tunneled without decryption (recorded as `kind=tunnel`). Changes take
effect immediately and are saved to `config.toml`. Defaults: `*.apple.com
*.icloud.com *.icloud-content.com *.mzstatic.com *.cdn-apple.com
*.push.apple.com *.apple-cloudkit.com *.ls.apple.com *.crashlytics.com`.

```sh
pano bypass add '*.bank.example' api.pinned.example
```

### `pano export har`

Write flows from the in-memory ring to a HAR 1.2 file. Accepts the filter
flags plus `-o, --out FILE` (required) and `--reveal` (unredacted, audited).
Bodies are inlined decoded (text as UTF-8, otherwise base64) up to 1 MiB
each; a `_pano` object per entry keeps id, kind, session, error, tags, rule
hits and truncation flags.

```sh
pano export har --host api.openai.com --since 1h -o openai.har
```

### `pano import har`

`-i, --in FILE` — import a HAR (from pano, Chrome, Firefox, other proxies)
into the current session; imported flows are tagged `imported`.

## Per-process capture

### `pano run -- <command> [args...]`

Exec a command with proxy and CA environment set. No system settings change
and no keychain trust is needed. Starts the daemon if needed. Because it
passes all arguments through, use `pano help run` for its help text.

```sh
pano run -- curl https://api.github.com/zen
pano run -- npm test
pano run -- python app.py
```

Environment set for the child:

| Variable | Value |
|---|---|
| `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` (and lower-case) | `http://127.0.0.1:9091` |
| `NO_PROXY`, `no_proxy` | `localhost,127.0.0.1,::1,*.local` |
| `SSL_CERT_FILE` | `~/.pano/ca.pem` (Go, OpenSSL-based tools) |
| `NODE_EXTRA_CA_CERTS` | Node.js |
| `REQUESTS_CA_BUNDLE` | Python requests |
| `CURL_CA_BUNDLE` | curl |
| `GIT_SSL_CAINFO` | git |
| `AWS_CA_BUNDLE` | AWS SDKs / CLI |
| `DENO_CERT` | Deno |
| `CARGO_HTTP_CAINFO` | cargo |

### `pano env`

Print the same variables as shell `export` lines:

```sh
eval "$(pano env)"
```

## Configuration

### `pano config get | path | init | edit`

`get` prints the effective configuration (from the daemon when running,
otherwise from the file over defaults); `path` prints `~/.pano/config.toml`;
`init` writes a file with defaults (refuses to overwrite); `edit` opens it in
`$EDITOR` (default `vi`). See [config.md](config.md).

## MCP

### `pano mcp`

Serve MCP on stdio (see [mcp.md](mcp.md)). Does not start the daemon: tools
answer "pano is off" until the user runs `pano on`. `--http` prints the
Streamable HTTP URL (`http://127.0.0.1:9092/mcp`) instead of serving.

### `pano mcp install [--scope user|project|local]`

Run `claude mcp add --scope … --transport stdio pano -- <pano> mcp`. If the
`claude` CLI is missing, prints the command and a `.mcp.json` snippet.

## Shell completion

```sh
pano completion zsh > "${fpath[1]}/_pano"      # zsh
pano completion bash > /etc/bash_completion.d/pano
pano completion fish > ~/.config/fish/completions/pano.fish
```

## Hidden commands

`pano daemon` (what `pano start` execs) and `pano _watchdog --pid N --state
FILE` (spawned by the daemon when the system proxy is enabled) are internal.
