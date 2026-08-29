# FAQ

## Is this safe?

pano is a man-in-the-middle proxy for **your own machine**. `pano ca install`
adds a root certificate that was generated on this Mac to your login
keychain; from then on any software that trusts the keychain will accept
certificates pano mints, and pano can decrypt that software's HTTPS. That is
the feature and the risk. What limits it:

- The root is generated on first run, per user, on this machine; no two
  installs share key material. Its private key (`~/.pano/ca.key`, mode 0600)
  never leaves the machine, is never logged, and is never served by the
  control API or MCP. pano refuses to load it if it is group/world readable.
- The root is valid for two years (leaf certificates 30 days, never past the
  root). When it expires pano generates a new one and asks you to run
  `pano ca install` again; `pano status` and `pano doctor` warn 30 days
  ahead, and `pano ca reset` renews early.
- Trust is installed for the TLS server policy only, so the root cannot
  vouch for signed code, S/MIME mail or software updates even if its key
  leaked.
- Toward origins pano is a normal TLS client that verifies real certificates
  with the system roots. It does not downgrade anything upstream.
- The proxy and the MCP HTTP endpoint bind `127.0.0.1`; the control socket is
  mode 0600; the daemon refuses to run as root.
- Secrets are redacted in every view by default; `--reveal` /
  `reveal_secrets` is written to `~/.pano/audit.log`.
- Installing the CA is terminal-only; toggling the system proxy over MCP
  needs `confirm: "yes"` and is audited.
- pano makes no network requests of its own and has no telemetry.

Remove trust at any time with `pano ca uninstall`. See
[SECURITY.md](../SECURITY.md).

## A site works when I go direct but returns errors only through pano

If the TLS handshake succeeds (no tunnel error, you see real request/response
headers) but a request gets a server error — classically a Cloudflare **500
"Worker threw exception"** — that the same request returns 200 for without the
proxy, suspect a **bodyless request forwarded as a half-open HTTP/2 stream**.

Go's HTTP server represents a bodyless request (a GET, HEAD, most DELETEs) with
a non-nil body value, and if a proxy forwards that as-is the upstream HEADERS
frame loses its END_STREAM flag — the origin sees a GET that claims a request
body is coming. Some origins reject that. LinkedIn's on-the-fly image resizer
(`media.licdn.com/dms/image/…`, a Cloudflare Worker) is one: it 500s, while the
static `media.licdn.com/media/…` blobs on the same host are untouched, so *some*
images load and some don't.

pano fixes this itself (bodyless requests are sent with END_STREAM as of the
change logged in `CHANGELOG.md`); this entry is here because the symptom looks
like a pinning or certificate failure but is neither — the tell is that the
response is a genuine origin error page, not a `client rejected pano
certificate` tunnel error.

## Some site "doesn't work" — how do I tell whether it is pinning?

Look for tunnel flows with an error:

```sh
pano flows --kind tunnel --errors
#  2k4  14:02:58 TUN  gateway.icloud.com  …  ↳ client rejected pano certificate (CA not trusted, or the app pins certificates — run `pano decrypt never add gateway.icloud.com`)
```

`client rejected pano certificate` means the client saw pano's leaf and
refused it. Either the CA is not trusted yet (`pano ca status`,
`pano doctor`) or the app **pins** its certificates. You do not have to hunt
for these rows: every host that refused the certificate in the last hour is
listed under **rejected** in `pano decrypt`, `pano status`, `pano doctor`,
`pano_status` and the TUI header. Pinned apps cannot be decrypted; put them
on the `never` list so they are spliced through untouched:

```sh
pano decrypt never add whatsapp.net '*.bank.example'   # or: pano decrypt never add --rejected
```

pano suggests, it never adds a host by itself. `never` hosts still appear as
`kind=tunnel` rows (method `TUN`, tag `never`) with byte counts but no
headers or bodies. The default `never` list covers Apple services and
Crashlytics; `pano decrypt` prints it in full.

## Can I decrypt only my own app and leave the rest of the machine alone?

Yes — that is what mode `only` is for:

```sh
pano decrypt only add api.myapp.example localhost
pano decrypt only
```

Everything not on the list is tunneled (rows tagged `unlisted`), so browser
sessions, mail and chat apps never pass through pano in plaintext. `pano
decrypt off` goes one step further and decrypts nothing — useful before the
CA is trusted, or when you only need to see which hosts an app talks to.
`pano decrypt all` restores the default. The mode and both lists are always
shown in full by `pano status` and in the TUI (`D`).

## Why is some Apple traffic not decrypted?

`*.push.apple.com`, `*.icloud.com`, `*.icloud-content.com`,
`*.apple-cloudkit.com` and `*.ls.apple.com` are on the `never` list by
default because those macOS daemons pin certificates and visibly break (push
notifications, iCloud sync, CloudKit, Maps) when intercepted. The list is
deliberately minimal: other Apple hosts (App Store, software update, CDNs)
are decrypted like everything else, and any that turn out to pin appear
under **rejected** for you to add. Remove a glob with `pano decrypt never rm`
if you really need it.

## Firefox does not trust the certificate

Firefox uses its own trust store. Either set
`about:config → security.enterprise_roots.enabled = true` so it also
honours the macOS keychain, or import `~/.pano/ca.pem` under Settings →
Privacy & Security → Certificates → View Certificates → Authorities.

## What about ECH (Encrypted Client Hello)?

ECH hides the real server name in the TLS ClientHello; the visible SNI is a
public name such as `cloudflare-ech.com`. pano does not implement ECH: it
completes the handshake without it and keys the minted certificate on the
**CONNECT target** whenever the SNI is missing or differs from the tunnel
destination, so the leaf names the host the client actually asked for.
Whether a browser then accepts that and continues without ECH is up to the
browser and has changed between releases. If an ECH-enabled site refuses to
load through pano, disable ECH in the browser
(`chrome://flags/#encrypted-client-hello`; Firefox
`network.dns.echconfig.enabled`) or add the host to the `never` list.

## HTTP/3 / QUIC?

pano speaks HTTP/1.1 and HTTP/2 over TCP; there is no QUIC listener.
Browsers that are configured to use an HTTP proxy send origin traffic
through the proxy's CONNECT tunnel instead of QUIC, so it falls back to
h2/h1 through pano. Apps that ignore proxy settings and talk QUIC directly
are not seen at all; use `pano run --` with a client that honours
`HTTPS_PROXY`, or disable QUIC in the app.

## Linux / Windows?

The engine, store, rules, views, CLI and MCP server are pure Go
(`CGO_ENABLED=0`) and build and run on Linux. Three pieces are macOS-only
and stubbed elsewhere:

| Feature | macOS | Linux / Windows |
|---|---|---|
| `pano on` / `pano off` / `pano_system_proxy` | `networksetup` snapshot/restore | error: not supported — use `pano run --` or `HTTP(S)_PROXY` |
| `pano ca install` / `uninstall` / `status` | login keychain | prints manual steps (`update-ca-certificates`, `trust anchor`, `certutil`) |
| watchdog | kqueue `NOTE_EXIT` | polls the pid every 500 ms |

`pano run -- <cmd>`, `pano env`, `pano tail`, rules, MCP — everything else —
work on both. Releases ship darwin and linux builds for arm64 and amd64.
Windows does not build yet: the CLI uses Unix-only process attributes
(`Setsid`) and `syscall.Exec`; the daemon, store and MCP packages themselves
are portable.

## `curl: SSL certificate problem` / `UNABLE_TO_VERIFY_LEAF_SIGNATURE` / `CERTIFICATE_VERIFY_FAILED`

The process is using pano as a proxy but does not trust the CA:

- Wrap it: `pano run -- curl …` sets `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`,
  `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`, `AWS_CA_BUNDLE`,
  `DENO_CERT`, `CARGO_HTTP_CAINFO` to `~/.pano/ca.pem`.
- Or point the tool at the CA yourself: `curl --cacert "$(pano ca path)"`.
- Java, some Go programs with custom roots, and anything with its own bundle
  (e.g. `certifi` when `REQUESTS_CA_BUNDLE` is ignored) need the CA added to
  *their* store.
- System-wide: `pano ca install` (keychain) covers browsers and most native
  apps.

## The daemon is running but I see no flows

- `pano status`: is `system proxy` on? If not, either `pano on` or run the
  client with `pano run --`.
- Is capture paused (`capture ○ paused`)? `pano_capture action=start` or
  `POST /v1/capture {"action":"start"}`.
- Does the app honour system proxy settings? Many CLI tools only read
  `HTTPS_PROXY`; use `eval "$(pano env)"`.
- `pano flows --kind tunnel` shows connections that arrived but were not
  decrypted (`never` list, mode `only`/`off`, or a handshake failure); `pano
  decrypt` shows the mode, the lists and recently rejected hosts.

## Performance expectations

The proxy is a single static Go binary with streaming bodies, a shared
upstream connection pool (h1+h2, 64 idle conns per host), pooled 32 KiB copy
buffers and a lock-free rule set. Captured bytes are teed into a bounded
buffer and handed to a write-behind SQLite writer that batches ≤ 256 events
per 50 ms and never blocks the proxy (it drops and counts instead:
`pano status` shows `N events dropped (store fell behind)`). Bodies larger
than `[capture] max_body_bytes` (4 MiB) are forwarded in full but stored
truncated. Server-sent events are flushed per read, so LLM token streams
arrive with no added buffering. Expect sub-millisecond added latency per
request on a laptop and thousands of requests per second before the store
starts dropping; the first request to a new host pays for one certificate
mint (cached in memory and on disk afterwards).

## How is this different from mitmproxy, Proxyman or Charles?

mitmproxy is a Python proxy with a TUI/web UI and a rich addon API; Proxyman
and Charles are macOS GUI applications with map-local/breakpoint features
and (Proxyman) a paid licence. All three are built for a person looking at a
screen. pano has no UI: it is a Go daemon whose primary interface is an MCP
server designed around token budgets (one-line flow lists, summary/schema
views, JSON-path selection, LLM stream reassembly into final message and
usage, `next:` hints), plus a CLI for humans, and it lets agents install
live rules (latency, failure rates, mocks, rewrites, breakpoints) over the
same API. It persists to SQLite with full-text search, handles HTTP/2,
WebSocket and SSE, and is pre-1.0 and macOS-first, with a smaller feature
surface than any of the three (no scripting language, no HAR viewer, no
mobile-device helpers).

## How do I completely remove pano?

```sh
pano off                 # restore the macOS proxy settings (safe if already off)
pano stop                # stop the daemon
pano ca uninstall        # remove the root from the login keychain
rm -rf ~/.pano           # CA keys, captures, rules, config, logs
brew uninstall pano      # or delete the binary you installed
```

If you registered the MCP server: `claude mcp remove pano`. If you used
`pano ca install --system`, remove the root from the System keychain with
Keychain Access or `sudo security delete-certificate -c "pano Root CA" /Library/Keychains/System.keychain`.

## The daemon died and my Mac has no internet

That is exactly what the watchdog is for: it restores the previous proxy
settings when the daemon exits for any reason. If it did not (for example
the watchdog was killed too), run `pano off` — it restores from
`~/.pano/sysproxy.json` even when the daemon is down — or `pano doctor`,
which reports the stale state. As a last resort: System Settings → Network →
your service → Details → Proxies, and untick Web Proxy and Secure Web Proxy.

## Can I run two daemons?

Use a different home: `PANO_HOME=~/.pano-dev pano start --port 9191
--mcp-port 9192`. Every later command needs the same `PANO_HOME` (or
`--sock`). The control socket refuses to start if another daemon is already
listening on the same path.
