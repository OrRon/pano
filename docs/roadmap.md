# Roadmap

Things that are deliberately not done yet, in rough priority order. Open an
issue if one of these matters to you.

## Distribution

- [x] **Homebrew tap.** `brew install orron/tap/pano` — live since v0.1.0.
- [x] First tagged release: v0.1.0, 2026-08-30, via goreleaser.
- [ ] `pano update` self-replace (today the update check is notify-only).
- [ ] Code signing / notarization (would replace the cask's quarantine-bit
      hook).

## Enterprise features

- [ ] **PAC-aware upstream chaining.** On managed Macs a corporate agent
      (Zscaler, Netskope, Koi Security, …) pushes an Automatic Proxy
      Configuration (PAC) URL; `pano on` snapshots it and turns it off so
      capture works (a PAC overrides manual proxies in Chrome and most apps),
      which also takes the machine out of the corporate filter for the
      session. The better answer — what Proxyman's External Proxy does since
      3.2.0 — is to keep the filter in the path: evaluate the PAC per request
      (run `FindProxyForURL` with a small embedded JS engine, e.g. goja) and
      forward upstream to the proxy it names, `DIRECT` as fallback. Config
      sketch: `[upstream] pac_url = "…"` or `pac = "auto"` to reuse the PAC
      pano just snapshotted; plus a plain `[upstream] proxy = host:port` for
      fixed corporate proxies. Needs: PAC fetch + cache, evaluator, per-request
      dial-through-upstream in the proxy engine (CONNECT-over-CONNECT for
      https), auth passthrough, `pano status` line naming the upstream.
- [ ] Fixed upstream proxy without PAC (`[upstream] proxy`, with bypass
      list) — the simpler half of the above, useful on its own.

## Platforms

- [ ] Linux: native `pano on/off` and `pano ca install` (system proxy and
      trust-store integration); today they print manual instructions.
- [ ] Windows: unsupported.
