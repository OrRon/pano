# Rules and breakpoints

Rules change live traffic as it passes through pano: add latency, fail a
fraction of requests, mock an endpoint, rewrite headers or bodies, redirect a
host, throttle downloads, or park an exchange on a breakpoint so you can edit
it by hand. Agents add them with `pano_rule_add`, humans with
`pano rules add`; both go through the same engine (`internal/rules`).

Rules take effect immediately, persist in `~/.pano/rules.json`, and survive
daemon restarts. Set `ttl_s` on anything experimental.

## Rule document

```json
{
  "id": "r_7k3q2",
  "name": "slow openai",
  "enabled": true,
  "priority": 0,
  "match": {
    "host": "api.openai.com",
    "path": "/v1/chat/*",
    "method": ["POST"],
    "scheme": "https",
    "header": { "X-Env": "staging*" },
    "status": "",
    "phase": "request"
  },
  "actions": [
    { "type": "delay", "ms": 2000, "jitter_ms": 500 }
  ],
  "probability": 1,
  "max_hits": 0,
  "ttl_s": 600
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | optional; 1–64 chars of `[A-Za-z0-9_.-]`; generated as `r_xxxxx` when omitted; must be unique |
| `name` | string | free text, shown in listings; also matches `--rule NAME` filters |
| `enabled` | bool | default `true` |
| `priority` | int | higher runs first; ties: oldest first |
| `match` | object | all set fields must hold (see below) |
| `actions` | array | at least one; run in order |
| `probability` | float 0–1 | gate evaluated per matching exchange; `0` or `1` = always |
| `max_hits` | int | disable the rule once it has fired this many times (re-enabling resets the counter) |
| `ttl_s` | int | remove the rule N seconds after creation (overrides `expires`) |
| `expires` | RFC3339 | absolute expiry; must be in the future |
| `hits`, `created_at` | | read-only |

### `match`

| Field | Syntax |
|---|---|
| `host` | glob, case-insensitive: `api.openai.com`, `*.googleapis.com`, `*`. A pattern containing `:` is matched against `host:port` (`localhost:3000`). |
| `path` | prefix (`/v1/`), glob (`/v1/*/models`; `*` also crosses `/`), or a regexp when wrapped in slashes and the inner text uses regexp syntax (`/^\/v[12]\//`) |
| `method` | list, case-insensitive |
| `scheme` | `http` or `https` |
| `header` | map of header name → glob matched against the request header value; `""` only requires the header to be present |
| `status` | response status spec: `500`, `4xx`, `400-499`, `!2xx`, `200\|204`; forces the response phase |
| `phase` | `request`, `response` or `both`; derived from the actions when empty |

## Phases

Every rule runs in the request phase (before the origin is contacted), the
response phase (once response headers are known), or both. When `phase` is
empty it is derived: if every action is pinned to the response side the rule
is a response rule; if `status` is set it is a response rule; otherwise it is
a request rule. An action can pin itself with `"on": "request"` or
`"on": "response"` where allowed:

| Action | Default side | Allowed `on` |
|---|---|---|
| `delay`, `set_header`, `remove_header`, `rewrite_body`, `breakpoint`, `tag` | wherever the rule runs | request, response |
| `set_query`, `redirect` | request | request only |
| `mock`, `mock_every_n`, `block` | request | request, response |
| `throttle` | response | response only |

The engine rejects a rule whose action cannot run in its phase (e.g. a
`throttle` in a request-only rule) and a `match.status` in a request-only
rule.

## Actions

`mock`, `mock_every_n`, `block` and a dropped `breakpoint` end the evaluation
for that exchange; everything else continues to the next action/rule. Every
applied action is recorded on the flow as a rule hit (visible in `pano show`
as `rules: r_7k3q2:delay` and as a flag in list rows).

### `delay`

```json
{ "type": "delay", "ms": 2000, "jitter_ms": 500 }
```
Sleep `ms` plus a random `0..jitter_ms`; at least one must be > 0. Cancelled
if the client goes away.

### `set_header` / `remove_header`

```json
{ "type": "set_header", "name": "X-Debug", "value": "1", "on": "response" }
{ "type": "remove_header", "name": "Cache-Control" }
```

### `set_query`

```json
{ "type": "set_query", "name": "debug", "value": "true" }
```
Sets `name=value` on the request URL (request phase only).

### `rewrite_body`

Exactly one of `json_patch`, `regex` + `replace`, or `template`:

```json
{ "type": "rewrite_body", "json_patch": { "model": "gpt-4.1-mini", "messages.0.content": "hi" } }
{ "type": "rewrite_body", "regex": "\"temperature\":\\s*[0-9.]+", "replace": "\"temperature\":0" }
{ "type": "rewrite_body", "template": "{{.Body}}\n<!-- {{.Method}} {{.Host}}{{.Path}} status {{.Status}} ua {{.Header \"User-Agent\"}} -->", "on": "response" }
```

- `json_patch`: dotted paths → values. Missing objects are created; a
  numeric segment indexes an array and may equal its length to append. Empty
  body is treated as `{}`.
- `regex`: RE2, `ReplaceAll` with Go expansion (`$1`).
- `template`: Go `text/template` with `.Host .Path .Method .Status .Body`
  and `.Header "Name"` (request headers in the request phase, response
  headers in the response phase).
- Bodies are buffered up to **8 MiB**; larger bodies pass through untouched
  and the hit note says `skipped`. Only identity and gzip encodings are
  decoded for rewriting (`br`/`zstd` bodies are skipped). Rewritten bodies
  are served plain with a fresh `Content-Length`.

### `mock`

```json
{ "type": "mock", "status": 503, "headers": { "Retry-After": "5" }, "body": "{\"error\":\"down\"}" }
```
Answer without contacting the origin. `status` defaults to 200.
`Content-Type` defaults to `application/json` when the body parses as JSON,
otherwise `text/plain; charset=utf-8`.

### `mock_every_n`

Same fields as `mock` plus `"value": "5"` — fire on every 5th hit of this
rule (counter per action).

### `block`

```json
{ "type": "block", "mode": "reset" }
{ "type": "block", "mode": "timeout", "ms": 30000 }
{ "type": "block", "mode": "status", "status": 502, "body": "…" }
```
`reset` drops the connection; `timeout` hangs for `ms` (default 30000) or
until the client gives up, then drops; `status` (the default when `mode` is
empty) answers with `status` (default 502) and `body` (default
`{"error":"blocked by pano rule <id>"}`).

### `redirect`

```json
{ "type": "redirect", "upstream": "http://localhost:3000", "preserve_host": false }
```
Send the request to `upstream` instead (scheme + host required; a path
prefix is prepended). The `Host` header is rewritten unless `preserve_host`
is true. The flow's host/port are updated so it shows where the request
actually went.

### `throttle`

```json
{ "type": "throttle", "kbps": 64 }
```
Limit the response body to `kbps` KiB/s (`kbps ≥ 1`) with a 32 KiB burst.
The token bucket is **per rule**, so several concurrent responses matched by
the same rule share the budget.

### `breakpoint` (alias `hold`)

```json
{ "type": "breakpoint", "on": "request" }
```
Park the exchange until it is resumed or dropped; see below.

### `tag`

```json
{ "type": "tag", "tags": ["experiment-a"] }
```
Add tags to the flow (filter with `--tag` / `tag=`).

## Presets

Presets are expanded server-side into ordinary rules. `pano rules presets`
and `pano_rules_list presets=true` print this table with defaults.

| Preset | Description | Params (default) | Expands to |
|---|---|---|---|
| `slow_network` | add latency to every matching request | `host` (`*`), `path`, `ms` (2000), `jitter_ms` (500) | `delay` |
| `fail_rate` | answer a fraction of requests with an injected error | `host` (required), `path`, `rate` (0.3), `status` (503), `body` (`{"error":"injected failure"}`) | `mock` with `probability = rate` |
| `offline_host` | reset every connection to a host | `host` (required), `path` | `block mode=reset` |
| `timeout` | hang matching requests until the client gives up | `host` (required), `path`, `after_ms` (30000) | `block mode=timeout` |
| `rate_limit` | answer every nth request with 429 | `host` (required), `path`, `every_n` (5), `status` (429), `retry_after` (2) | `mock_every_n` with `Retry-After` and `{"error":"rate limited"}` |
| `hold` | park matching exchanges on a breakpoint | `host` (required), `path` (`*`), `on` (`request`; `response`, `both`) | `breakpoint` with `match.phase = on` |

Unknown parameters and missing required ones are rejected. `name` and
`ttl_s` on the add request override the preset's own.

## Probability, `max_hits`, `ttl_s`

- `probability` is evaluated once per matching exchange per phase. A rule
  with `probability: 0.3` fires on roughly 30 % of matches; the other 70 %
  are untouched and not counted as hits.
- `max_hits` counts fired evaluations; when reached the rule disables itself
  (it stays in the list as `off`). `pano rules toggle --on` re-enables it and
  resets the counter.
- `ttl_s` becomes an absolute `expires`. Expired rules are removed lazily by
  the next evaluation and skipped when `rules.json` is loaded.

## Breakpoints

```
rule with breakpoint/hold ──▶ matching exchange arrives
                              │  flow.state = held · event "held" published
                              │  pano tail shows "HELD …" · pano bp ls / pano_flows state=held / pano_tail lists it
                              ▼
        ┌──────────── you decide ────────────┐
        │ pano bp resume <id> [-H …] [--body] │──▶ continues with edits applied
        │ pano bp drop <id>                   │──▶ connection reset
        └────────────────────────────────────┘
        no decision within [breakpoints] hold_timeout (120 s) ──▶ continues unmodified ("hold timeout")
        client disconnects while held ──▶ dropped ("client disconnected while held")
```

- Request-phase edits: `url` (absolute or path+query), `method`,
  `set_headers`, `remove_headers`, `body`, `body_patch` (dotted paths).
- Response-phase edits: `status`, `set_headers`, `remove_headers`, `body`,
  `body_patch`.
- `pano_breakpoint_resume` is the MCP equivalent; `action=drop` to drop.
- Stopping the daemon releases every held exchange unmodified.
- Held bodies are buffered up to 8 MiB; bigger bodies cannot be edited
  (the hit note says so) but still resume.

## Recipes

Each recipe shows the CLI form and, where useful, the JSON you would pass to
`pano rules add --file` or `pano_rule_add rule=…`.

### 1. Slow down an API

```sh
pano rules add --preset slow_network --param host=api.openai.com --param ms=3000 --param jitter_ms=1000 --ttl 600
```

MCP: `pano_rule_add preset=slow_network params={"host":"api.openai.com","ms":3000,"jitter_ms":1000} ttl_s=600`

### 2. Fail 30 % of requests with a 503

```sh
pano rules add --preset fail_rate --param host=api.anthropic.com --param rate=0.3 --param status=503 --ttl 600
```

Equivalent document:

```json
{
  "name": "flaky anthropic",
  "match": { "host": "api.anthropic.com" },
  "probability": 0.3,
  "actions": [ { "type": "mock", "status": 503, "body": "{\"error\":\"injected failure\"}" } ],
  "ttl_s": 600
}
```

### 3. Mock an endpoint

```sh
pano rules add --host api.example.com --path /v1/orders --method GET \
  --action 'mock:200:{"orders":[{"id":1,"status":"shipped"}]}' --name mock-orders
```

With headers (needs `--file`):

```json
{
  "name": "mock orders",
  "match": { "host": "api.example.com", "path": "/v1/orders", "method": ["GET"] },
  "actions": [ { "type": "mock", "status": 200,
                 "headers": { "X-Mock": "1", "Cache-Control": "no-store" },
                 "body": "{\"orders\":[]}" } ]
}
```

### 4. Rewrite a JSON field in the request

Force every OpenAI call onto a cheaper model and cap `max_tokens`:

```json
{
  "name": "downgrade model",
  "match": { "host": "api.openai.com", "path": "/v1/chat/completions" },
  "actions": [ { "type": "rewrite_body", "on": "request",
                 "json_patch": { "model": "gpt-4.1-mini", "max_tokens": 256 } } ]
}
```

```sh
pano rules add --file downgrade.json --ttl 1800
```

### 5. Redirect a production host to localhost

```json
{
  "name": "prod → local",
  "match": { "host": "api.example.com" },
  "actions": [ { "type": "redirect", "upstream": "http://localhost:3000", "preserve_host": true } ]
}
```

```sh
pano rules add --host api.example.com --action redirect:http://localhost:3000
```

The flow's host changes to `localhost:3000`, so filter on `--rule` or
`--tag` rather than the original host. Add `"preserve_host": true` (file
form) when the local server routes on the `Host` header.

### 6. Throttle downloads

```sh
pano rules add --host 'cdn.example.com' --action throttle:64 --name slow-cdn
```

Response bodies from that host are paced at 64 KiB/s (shared across
concurrent responses). Combine with `--path '*.zip'` to hit only archives.

### 7. Hold a request and edit it

```sh
pano rules add --preset hold --param host=api.stripe.com --param path=/v1/charges --ttl 900
# … trigger the request in your app; it blocks …
pano bp ls
#   HELD 2k8  request POST https://api.stripe.com/v1/charges  age 3.1s  rule r_9x1ab
pano bp resume 2k8 -H 'Idempotency-Key: test-1' --body @charge.json
# or:  pano bp drop 2k8
```

To edit the *response* instead: `--param on=response`, then
`pano bp resume <id> --status 402 --body '{"error":"card_declined"}'`.

### 8. Rate-limit every 3rd request and tag the victims

```json
{
  "name": "burst limiter",
  "match": { "host": "api.openai.com" },
  "actions": [
    { "type": "tag", "tags": ["ratelimit-test"] },
    { "type": "mock_every_n", "value": "3", "status": 429,
      "headers": { "Retry-After": "1" }, "body": "{\"error\":\"rate limited\"}" }
  ],
  "ttl_s": 600
}
```

Afterwards: `pano flows --tag ratelimit-test --state mocked` shows exactly
which calls were rejected. Clean up with `pano rules rm --all` (or let the
TTL expire).
