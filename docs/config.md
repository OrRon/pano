# Configuration

pano reads `~/.pano/config.toml` over built-in defaults; the file only needs
the keys you change and may be absent. `pano config init` writes a complete
file with defaults, `pano config edit` opens it in `$EDITOR`, `pano config
get` prints the effective configuration (from the running daemon when there
is one). The daemon reads the file at start; restart it (`pano stop && pano
start`) after editing, except for `bypass`, which `pano bypass add/rm` updates
live and writes back.

```toml
[proxy]
port = 9091
mcp_port = 9092
bind = "127.0.0.1"
bypass = ["*.apple.com", "*.icloud.com", "*.icloud-content.com", "*.mzstatic.com",
          "*.cdn-apple.com", "*.push.apple.com", "*.apple-cloudkit.com",
          "*.ls.apple.com", "*.crashlytics.com"]

[capture]
enabled = true
max_body_bytes = 4194304
max_inflight_bytes = 268435456
persist = true
websocket_frames = true
ring_size = 10000

[retention]
max_age = "7d"
max_flows = 200000
max_db_bytes = 2147483648

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
| `bypass` | []string | Apple/Crashlytics globs above | host globs tunneled without decryption (recorded as `kind=tunnel`, tag `bypass`). Managed live by `pano bypass`. |

### `[capture]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `true` | record flows at start. `pano_capture`/`POST /v1/capture` toggles at runtime; the proxy keeps forwarding when off. |
| `max_body_bytes` | int | `4194304` (4 MiB) | per-body capture cap; larger bodies are forwarded in full but stored truncated (`trunc` flag). |
| `max_inflight_bytes` | int | `268435456` (256 MiB) | total capture buffer budget across all in-flight exchanges; beyond it bodies are counted but not buffered. |
| `persist` | bool | `true` | write flows and bodies to `pano.db`. With `false` only the in-memory ring exists (no `--q` search, no sessions history, no HAR history beyond the ring). |
| `websocket_frames` | bool | `true` | parse and store WebSocket frames (`ws_messages`, `GET /v1/flows/{id}/ws`). Frames are forwarded verbatim either way. |
| `ring_size` | int | `10000` | flows kept in memory (newest win). Lists, `pano tail`, replay summaries and HAR export read from this ring. |

### `[retention]`

Enforced by a pruner that runs every minute.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `max_age` | duration | `"7d"` | delete flows older than this |
| `max_flows` | int | `200000` | keep at most this many flows (oldest deleted) |
| `max_db_bytes` | int | `2147483648` (2 GiB) | while `pano.db` is larger, drop the oldest 10 % of flows per round (up to 8 rounds), then vacuum |

Zero disables a limit.

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
├── config.toml          this file (0600; written by `pano config init/edit` and `pano bypass`)
├── pano.sock            control API Unix socket (0600; $TMPDIR/pano-<uid>.sock if the path exceeds ~100 bytes)
├── daemon.pid           pid of the detached daemon
├── daemon.log           daemon + watchdog log (rotated to daemon.log.1 at 20 MiB on start)
├── audit.log            reveal_secrets uses and system-proxy toggles, one line each
├── ca.pem               root certificate (0644; valid 2 years, regenerated when expired; the only file you ever hand to other software)
├── ca.key               root private key (0600; generated per user on first run; refused if group/world readable)
├── leaf.key             shared private key of all minted leaf certificates (0600)
├── certs/<host>.pem     disk cache of minted leafs (30-day TTL)
├── pano.db, -wal, -shm  SQLite store (flows, blobs, blob_text, ws_messages, sessions, flows_fts)
├── rules.json           persisted rules (0600, rewritten atomically on every change)
└── sysproxy.json        macOS proxy snapshot; exists only while pano owns the system proxy
```

To relocate: `export PANO_HOME=/path` for every command (including the one
that spawns the daemon). To start over: `pano stop && pano off && pano ca
uninstall && rm -rf ~/.pano`.

## Known gaps

- `Paths.Token()` (`~/.pano/token`) is reserved for a bearer-token TCP
  control listener that is not enabled in this version; the file is never
  written.
