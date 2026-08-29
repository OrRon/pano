# Configuration

pano reads `~/.pano/config.toml` over built-in defaults; the file only needs
the keys you change and may be absent. `pano config init` writes a complete
file with defaults, `pano config edit` opens it in `$EDITOR`, `pano config
get` prints the effective configuration (from the running daemon when there
is one). The daemon reads the file at start; restart it (`pano stop && pano
start`) after editing, except for `[decrypt]`, which `pano decrypt …`,
`pano_decrypt` and the TUI update live and write back.

```toml
[proxy]
port = 9091
mcp_port = 9092
bind = "127.0.0.1"

[decrypt]
mode = "all"          # all | only | off
only = []             # decrypted when mode = "only"
never = ["*.push.apple.com", "*.icloud.com", "*.icloud-content.com",
         "*.apple-cloudkit.com", "*.ls.apple.com"]   # never decrypted, in every mode

[capture]
enabled = true
max_body_bytes = 4194304
max_inflight_bytes = 268435456
websocket_frames = true
ring_size = 10000

[redaction]
enabled = true
extra_patterns = []
extra_headers = []

[views]
default_max_bytes = 4096
list_page_size = 50
string_truncate = 200

[breakpoints]
hold_timeout = "120s"

[mcp]
expose_http = true

[system_proxy]
restore_on_exit = true

[limits]
max_conns = 10000

[updates]
check = true
```

## Keys

Durations accept Go syntax (`90s`, `2m`, `1h30m`) plus a trailing `d` for
days (`7d`, `0.5d`).

### `[proxy]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `port` | int | `9091` | proxy listen port (1–65535). Override per run with `pano start --port`. |
| `mcp_port` | int | `9092` | loopback port for the Streamable HTTP MCP endpoint (`/mcp`). `pano start --mcp-port`. |
| `bind` | string | `"127.0.0.1"` | bind address for the proxy and MCP HTTP listeners. Keep it loopback; nothing authenticates remote clients. `pano start --bind`. |
| `bypass` | []string | — | **deprecated** → `[decrypt] never`. Read once with a warning when no `[decrypt]` table exists, ignored otherwise, never written back. |

### `[decrypt]`

Which HTTPS tunnels are TLS-terminated. Managed live by `pano decrypt`,
`pano_decrypt` and the TUI (`D`); every change is written back here and to
`audit.log`. See [ADR 0007](adr/0007-decrypt-policy.md).

| Key | Type | Default | Meaning |
|---|---|---|---|
| `mode` | string | `"all"` | `all` decrypts every host except `never`; `only` decrypts just `only`; `off` decrypts nothing (tunnels record host, bytes, timing). |
| `only` | []string | `[]` | hosts decrypted when `mode = "only"`. |
| `never` | []string | the five Apple globs above | never decrypted, in every mode (pinned apps). Recorded as `kind=tunnel`, tag `never`; `unlisted`/`off` tag the other two reasons. |

Entries are hosts or globs. A bare host covers its subdomains
(`whatsapp.net` matches `mmg.whatsapp.net`); `*` and `?` are wildcards
(`*.example.com` does **not** match `example.com`). Entries are lowercased
and lose a trailing dot or `:port`; empty entries or anything with a space
or `/` fail validation.

### `[capture]`

Everything captured is held in the daemon's memory and gone when it stops
(ADR 0011); nothing is written to disk.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `true` | record flows at start. `pano_capture`/`POST /v1/capture` toggles at runtime; the proxy keeps forwarding when off. |
| `max_body_bytes` | int | `4194304` (4 MiB) | per-body capture cap; larger bodies are forwarded in full but stored truncated (`trunc` flag). |
| `max_inflight_bytes` | int | `268435456` (256 MiB) | total capture buffer budget across all in-flight exchanges; beyond it bodies are counted but not buffered. |
| `websocket_frames` | bool | `true` | parse and keep WebSocket messages (`GET /v1/flows/{id}/ws`; the newest 1 000 per flow, 64 MiB overall). Frames are forwarded verbatim either way. |
| `ring_size` | int | `10000` | flows kept in memory; when full the oldest is evicted. Every list, search, replay summary and HAR export reads from this ring. Bodies live in a separate 256 MiB LRU, so a very old flow's body can be gone before the flow is. |

The earlier keys `persist` and the whole `[retention]` table are accepted
and ignored (with a warning in `pano config get` and the daemon log); a
leftover `pano.db` is deleted the next time the daemon starts.

### `[redaction]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `true` | mask secrets in every rendered header, body, diff, explain digest and HAR export. `pano status` warns when off. |
| `extra_patterns` | []string | `[]` | additional regular expressions whose whole match is masked (applied at daemon start). |
| `extra_headers` | []string | `[]` | additional header names to mask wholesale (applied at daemon start). |

### `[views]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `default_max_bytes` | int | `4096` | body budget for `truncated`/`pretty`/`raw` when `max_bytes` is not given (hard cap 1 048 576 regardless). Must be > 0. |
| `list_page_size` | int | `50` | default `limit` for flow lists (max 200). Must be > 0. |
| `string_truncate` | int | `200` | strings longer than this are elided in `summary` and `schema` views |

### `[breakpoints]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `hold_timeout` | duration | `"120s"` | how long a breakpoint parks an exchange before it continues unmodified |

### `[mcp]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `expose_http` | bool | `true` | mount the MCP server on `bind:mcp_port/mcp` (Streamable HTTP, stateless, unauthenticated loopback) |

### `[system_proxy]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `restore_on_exit` | bool | `true` | restore the snapshotted macOS proxy settings when the daemon shuts down cleanly. The crash watchdog restores regardless. |

### `[limits]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `max_conns` | int | `10000` | concurrent CONNECT tunnels; beyond it clients get `503 pano: too many connections` |

### `[updates]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `check` | bool | `true` | once a day, from an interactive terminal, ask GitHub whether a newer release exists and print a one-line hint. Notify-only: pano never downloads or installs itself. `PANO_NO_UPDATE_CHECK=1` and `DO_NOT_TRACK=1` override this to off; `pano version --check` asks regardless. See ADR 0010. |

## Environment variables

| Variable | Read by | Effect |
|---|---|---|
| `PANO_HOME` | CLI, daemon, MCP | directory for everything (default `~/.pano`). `pano start` passes it to the daemon it spawns. |
| `PANO_SOCK` | CLI, MCP | control socket path (same as `--sock`). |
| `NO_COLOR` | CLI | disable ANSI colour (same as `--no-color`; colour is also off when stdout is not a TTY or `--json` is set). |
| `EDITOR` | `pano config edit` | editor to open the config in (default `vi`). |

`pano run -- <cmd>` and `pano env` *set* proxy and CA variables for child
processes; see [cli.md](cli.md#per-process-capture). The daemon itself never
consults `HTTP_PROXY`/`HTTPS_PROXY` for upstream connections (that could loop
back into pano).

## File layout under `~/.pano`

```
~/.pano/                 0700
├── config.toml          this file (0600; written by `pano config init/edit` and every decrypt change)
├── pano.sock            control API Unix socket (0600; $TMPDIR/pano-<uid>.sock if the path exceeds ~100 bytes)
├── daemon.pid           pid of the detached daemon
├── daemon.log           daemon + watchdog log (rotated to daemon.log.1 at 20 MiB on start)
├── audit.log            reveal_secrets uses, system-proxy toggles and decrypt changes (with source cli/mcp/tui), one line each
├── ca.pem               root certificate (0644; valid 2 years, regenerated when expired; the only file you ever hand to other software)
├── ca.key               root private key (0600; generated per user on first run; refused if group/world readable)
├── leaf.key             shared private key of all minted leaf certificates (0600)
├── certs/<host>.pem     disk cache of minted leafs (30-day TTL)
├── rules.json           persisted rules (0600, rewritten atomically on every change)
├── sysproxy.json        macOS proxy snapshot; exists only while pano owns the system proxy
└── update-check.json    when the release check last ran and what it found (0600)
```

To relocate: `export PANO_HOME=/path` for every command (including the one
that spawns the daemon). To start over: `pano stop && pano off && pano ca
uninstall && rm -rf ~/.pano`.

## Known gaps

- `Paths.Token()` (`~/.pano/token`) is reserved for a bearer-token TCP
  control listener that is not enabled in this version; the file is never
  written.
