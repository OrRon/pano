# Security

## What pano is

pano is a **local man-in-the-middle proxy**. When you run `pano ca install`, you add a
certificate authority that pano generated on *this machine* to your login keychain.
From then on, any software on this machine that trusts the keychain will accept
certificates pano mints on the fly, and pano can decrypt that software's HTTPS traffic.

That is the feature. It is also the risk. Please understand it before installing:

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
- pano binds to `127.0.0.1` only. The control socket `~/.pano/pano.sock` is `0600`.
- pano refuses to run as root.
- The optional Streamable HTTP MCP endpoint (`127.0.0.1:9092/mcp`) is loopback-only with
  DNS-rebinding protection; set `[mcp] expose_http = false` if you only use stdio.
- Captured bodies and headers are stored in `~/.pano/`. Secrets (API keys, cookies,
  bearer tokens, JWTs) are **redacted by default** in every view; revealing them
  requires an explicit flag and is written to `~/.pano/audit.log`.
- Installing the CA is deliberately **not** available over MCP — only from a terminal.
- Toggling the system proxy over MCP requires an explicit confirmation argument.
- pano makes no network requests of its own and has no telemetry.

Remove trust at any time with `pano ca uninstall`.

## Reporting a vulnerability

Please report security issues privately via
[GitHub Security Advisories](https://github.com/orron/pano/security/advisories/new).
Do not open a public issue. You should hear back within 72 hours.
