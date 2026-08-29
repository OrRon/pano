# Security

## What pano is

pano is a **local man-in-the-middle proxy**. When you run `pano ca install`, you add a
certificate authority that pano generated on *this machine* to your login keychain.
From then on, any software on this machine that trusts the keychain will accept
certificates pano mints on the fly, and pano can decrypt that software's HTTPS traffic.

That is the feature. It is also the risk. Please understand it before installing.

## The certificate authority

- The CA is generated on first run, per user, on this machine — nothing is shipped in
  the binary and no two installs share key material. Its private key lives in
  `~/.pano/ca.key` with mode `0600`; it never leaves the machine, is never logged, and
  is never served over the control API or MCP.
- The root is valid for **two years**, not the ten most interception tools use. A leaked
  key stops being useful when the root expires. Leaf certificates last 30 days and never
  outlive the root. An expired root is replaced automatically and you re-run
  `pano ca install`; `pano status` / `pano doctor` warn 30 days ahead.
- Trust is installed for the **TLS server policy only** (`-p ssl -p basic`), so even a
  trusted pano root cannot vouch for signed code, S/MIME mail, software updates or
  packages.
- `pano ca reset` untrusts the outgoing root before generating a new one, and
  `pano ca uninstall` removes every pano root from the keychain, including any left by
  earlier rotations. No pano root should ever remain trusted without a live key behind it.
- Toward origins pano is an ordinary TLS client that verifies real certificates against
  the system roots. It never downgrades or skips verification upstream.

## The rest of the surface

- pano binds to `127.0.0.1` only. The control socket `~/.pano/pano.sock` is `0600`.
- `pano mobile` is the one exception, and it is scoped: it adds a second
  listener for the **proxy only** on the Mac's LAN address (never `0.0.0.0`,
  never the control API or MCP), admits private and link-local source
  addresses only, is terminal-only (no MCP tool can open it), is audited, and
  ends with `pano mobile off` / `pano off`. The setup page it serves hands out
  the CA *certificate*, never the key.
- pano refuses to run as root.
- The optional Streamable HTTP MCP endpoint (`127.0.0.1:9092/mcp`) is loopback-only with
  DNS-rebinding protection; set `[mcp] expose_http = false` if you only use stdio.
- Captured bodies and headers are stored in `~/.pano/`. Secrets (API keys, cookies,
  bearer tokens, JWTs) are **redacted by default** in every view; revealing them
  requires an explicit flag and is written to `~/.pano/audit.log`.
- Installing the CA is deliberately **not** available over MCP — only from a terminal.
- Toggling the system proxy over MCP requires an explicit confirmation argument.
- pano has no telemetry. The one request it makes for itself is the once-a-day
  release check: a GET to GitHub's public releases endpoint with pano's version
  in the `User-Agent` and nothing else, sent directly (never through the system
  proxy, never recorded as a flow), only from an interactive terminal, and only
  ever printing a hint — it never downloads or installs. Off with
  `PANO_NO_UPDATE_CHECK=1`, `DO_NOT_TRACK=1`, `[updates] check = false` or at
  build time (`docs/adr/0010-updates-notify-only.md`).

Remove trust at any time with `pano ca uninstall`.

## Reporting a vulnerability

Please report security issues privately via
[GitHub Security Advisories](https://github.com/orron/pano/security/advisories/new).
Do not open a public issue. You should hear back within 72 hours.
