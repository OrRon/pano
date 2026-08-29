# ADR 0008: Phones via an explicit, proxy-only LAN listener and a self-served setup page

**Status:** accepted (2026-08-29)

## Context

pano bound to loopback only (an invariant since day one), so a phone could
not use it at all. Every comparable tool (Proxyman, Charles, mitmproxy)
supports phones the same way: the app listens on all interfaces, shows the
Mac's IP and port in a window, the user types them into the phone's Wi-Fi
settings, browses to a magic URL (`proxy.man/ssl`, `chls.pro/ssl`,
`mitm.it`) that only works once the proxy is set, installs the certificate,
and then finds the trust toggle on their own. The common failure — profile
installed, full trust not enabled — is invisible to the tool.

Constraints that shaped the design:

- The loopback-only invariant exists for a reason: an open proxy on a
  hostile network records other people's traffic into your store and lets
  them use your machine as an egress. Opening it must be deliberate, narrow
  and visible.
- pano has no window. Whatever the user needs to read must fit in a
  terminal or on the phone.
- Agents drive pano over MCP; opening the proxy to the network is not
  something an agent should be able to do on its own.

## Decision

1. **`pano mobile` adds one more listener for the proxy only**, bound to
   the Mac's LAN address (never `0.0.0.0`; Wi-Fi preferred), wrapped so that
   only private and link-local source addresses are accepted. The control
   socket and the MCP HTTP endpoint stay on loopback. Enabling and disabling
   are audited; `pano mobile off` and `pano off` close it. There is no MCP
   tool for it — `pano_status` only reports its state — like CA install.
2. **The proxy answers requests addressed to itself** instead of forwarding
   them: plain requests on a proxy port, absolute-URI requests for one of its
   own addresses, and anything for **`pano.internal`** over http or https.
   `pano.internal` never resolves in DNS (`.internal` is ICANN-reserved);
   through a proxy that does not matter, and it is why the same URL works on
   every device and network. `pano.internal` is TLS-terminated regardless of
   decrypt mode. None of this becomes a flow.
3. **The setup page is served by the LAN listener itself**, so the QR code
   printed in the terminal encodes `http://<lan-ip>:<port>` and works
   *before* the phone proxies anything. The page carries the proxy settings
   (tap to copy), the certificate in the platform's native form (an Apple
   configuration profile with a name, description, stable UUIDs and a removal
   path written in; DER for Android; PEM otherwise), and it polls pano to
   turn steps green from real signals: the first request that arrives through
   the proxy, then a `https://pano.internal/_pano/ok` probe that can only
   succeed once the device trusts the certificate. Refused handshakes from
   the device are named as "installed but not trusted yet".
4. **Remote clients are first-class**: the proxy keeps a small table (IP →
   name derived from User-Agent, requests, accepted and refused handshakes)
   that every status surface lists in full, and flows gain a `client` filter
   (`<ip>` or `remote`).

## Consequences

- The "loopback binds only" invariant becomes "loopback by default; the
  proxy may additionally listen on the LAN IP while the user says so".
  `CLAUDE.md` records the exception.
- `isSelf` grew from "loopback + my port" to "any address I listen on";
  self-addressed requests are served rather than refused with 403 (a CONNECT
  to self still is).
- One dependency added (`skip2/go-qrcode`, no transitive deps) for the
  terminal QR code.
- The proxy setting on iOS is per-network and cannot be pushed without an
  MDM-style Wi-Fi payload that would also carry the network password; that
  was judged not worth the risk of clobbering a known network, so step 1
  stays manual — but copied from the page, not from a window.
- Apps that opt out of user CAs (Android 7+, pinned iOS apps) are not
  solvable here; the page and `docs/mobile.md` say so instead of pretending.
