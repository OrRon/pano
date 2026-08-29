# ADR 0007: Decrypt policy — three modes, two lists, suggestions never auto-applied

**Status:** accepted (2026-08-29)

## Context

pano decrypted every CONNECT except hosts on a single `bypass` glob list.
That covered pinned Apple daemons but left three needs unmet:

- "decrypt only my app's API and leave the rest of the machine alone";
- "observe without decrypting" (hosts, bytes, timing — no plaintext);
- knowing *which* app is pinning. A pinned client fails the TLS handshake
  dozens of times (WhatsApp desktop did exactly that on the author's Mac),
  and the only trace was an error string on each tunnel flow. No front end
  surfaced the hosts, and an agent could not act on them.

The list was also editable only from the CLI — not over MCP, not in the TUI.

## Decision

1. **`[decrypt]` has a mode and two lists.** `mode = all | only | off`;
   `only` is consulted in mode `only`; `never` wins in every mode. One pure
   function, `proxy.DecryptPolicy.Decide(host)`, returns *decrypt or not* plus
   the reason tag recorded on the tunnel flow (`never` / `unlisted` / `off`).
   Three modes rather than two because "decrypt a short allow-list" is the
   safe default for a shared machine and the natural setting for a focused
   debugging session, while `off` is what you want when the CA is not
   trusted yet or you only need to see who is talking. Charles, Proxyman and
   mitmproxy all converged on include+exclude lists; pano adds the explicit
   mode so the state is one word, not an inference from two lists.
2. **A bare domain covers its subdomains.** `whatsapp.net` matches
   `mmg.whatsapp.net`; globs (`*`, `?`) still work. This lives in
   `proxy.HostMatch`, *not* in `internal/glob`, whose exact-match semantics
   rules depend on. Rationale: agents and humans kept writing `apple.com`
   and wondering why `gateway.apple.com` was still decrypted.
3. **Rejected hosts are suggested, never auto-added.** The proxy remembers
   hosts whose client refused pano's certificate in the last hour
   (`proxy.Server.Rejected`, in-memory, bounded). Every front end shows them
   with a one-step "never decrypt" action. pano does not add them itself:
   a MITM proxy silently ceasing to decrypt a host is exactly the surprise
   this tool exists to avoid, and the host you are debugging is often the
   one that pins.
4. **MCP may change the policy freely, audited.** `pano_decrypt` can set
   the mode and edit both lists without a confirmation gate. Narrowing is
   always safe; widening only matters once the user has already trusted the
   CA in a terminal (which MCP cannot do — ADR 0005 invariant). Every change
   is written to `~/.pano/audit.log` with its source (`cli`/`mcp`/`tui`),
   carried in the request body because the control socket cannot tell
   clients apart.
5. **Every list is always printed in full.** `api.Status` carries the whole
   `Decrypt` object, not counts; `pano status`, `pano doctor`, `pano_status`
   and the TUI print every entry and wrap rather than truncate. A host that
   is or is not being decrypted must be visible at a glance, not one
   sub-command away.
6. **The default `never` list is minimal.** Five globs — `*.push.apple.com`,
   `*.icloud.com`, `*.icloud-content.com`, `*.apple-cloudkit.com`,
   `*.ls.apple.com` — the macOS daemons that pin and visibly break (push,
   iCloud, CloudKit, Maps). The earlier blanket `*.apple.com` plus CDN and
   Crashlytics entries were folklore that also hid traffic which decrypts
   fine; with rejected-host suggestions in place, everything else is
   discovered rather than pre-excluded.

## Consequences

- `pano bypass` is gone; `[proxy] bypass` in an existing `config.toml` is
  read as `[decrypt] never` with a warning and rewritten on the next save.
- Tunnel flows are tagged with the reason instead of `bypass`; filters and
  docs use `never`, `unlisted`, `off`.
- `PATCH /v1/decrypt` is a partial update (add/remove per list) so three
  front ends never race on a whole-list replace.
- The TUI drawer gained a third tab; the header names the mode, the `only`
  hosts and the first rejected host.
