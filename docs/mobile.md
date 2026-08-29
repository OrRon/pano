# Phones and tablets

`pano mobile` puts an iPhone, iPad or Android device on the same Wi-Fi behind
pano. One command on the Mac, one QR code, one page on the phone that hands
out the proxy settings and the certificate and ticks each step off as the
phone gets there.

```sh
pano mobile
```

```
╭─────╮
│ ◉ ◉ │  ✓ proxy open to your network at 192.168.1.23:9091  en0 · Home
╰─────╯    the Mac's own proxy setting is unchanged

  █████████████████████████████████
  ████ ▄▄▄▄▄ █▀▄ █  ▄█ █ ▄▄▄▄▄ ████   On the phone (same Wi-Fi)
  ████ █   █ ██ ▀█▄█ ▀ █ █   █ ████
  ████ █▄▄▄█ █▄▄█ ▄▄▀▄ █ █▄▄▄█ ████   Scan → the setup page opens.
  ████▄▄▄▄▄▄▄█▄█ ▀▄▀ █ █▄▄▄▄▄▄▄████   It gives you the proxy settings, the
  ████▄▀ █▄▀▄▀█▀▀ █▄▀ ▄▀██ ██▄▀████   certificate, and ticks each step off
  ████ ██▀▀ ▄ █▀▀   ▄ ▀▀▄█ █▄ ▄████   as the phone gets there.
  ████▄▄█▄▄▀▄█  █▀  █ ▀▀ ▀▄ █▄ ████
  ████▄▀▀▀ ▄▄██▀▄▄ ▀▀▄▄▀▄▀▄▀▄ ▄████   No camera?   http://192.168.1.23:9091
  ████ ▄▄▄▄▄ ██▀█▀███  █▄█ ▀█▄ ████   Proxied already? http://pano.internal
  ████ █   █ █▄█ ▀   █   ▄▄█▀ ▄████
  █████████████████████████████████

  ● iPhone · iOS 17.5  192.168.1.40   proxy ✓  https ✓   42 requests
  ctrl-c leaves this open · close with pano mobile off
```

> **Beta.** This works end to end on iPhone; Android, iPad, simulators and
> unusual networks (guest Wi-Fi, client isolation, VPNs) have had less
> exercise. If a step didn't turn green, the page said the wrong thing, or
> your setup needed something this document doesn't cover, please
> [open an issue](https://github.com/OrRon/pano/issues) — a screenshot of
> the page and the output of `pano mobile status` is the ideal report.

The rest of this page is what happens underneath, what the phone sees, the
platform specifics, and how to undo it.

## What `pano mobile` does

1. **Finds the Mac's LAN address** — the Wi-Fi interface first (`en0`), then
   wired — and the network name when macOS will say.
2. **Opens one more proxy listener** on that address, same port as the
   loopback proxy (`192.168.1.23:9091`). Only the proxy is exposed: the
   control socket and the MCP endpoint stay on loopback. The listener admits
   private and link-local source addresses only; anything routed in from
   elsewhere is closed before a byte is read. The daemon logs and audits the
   change (`~/.pano/audit.log`: `mobile enabled=true addr=…`).
3. **Prints a QR code** for `http://192.168.1.23:9091` — the setup page,
   served directly by that listener, so it works *before* the phone proxies
   anything.
4. **Waits and watches.** Every device that connects appears with its state:
   `proxy ✓` once it has routed a request through pano, `https ✓` once it has
   completed a TLS handshake against pano's certificate, or `https ✕ ×n` if it
   keeps refusing the certificate. `ctrl-c` leaves the listener open;
   `pano mobile off` closes it — and stops the daemon unless `pano on` is
   active, so `pano mobile` → `pano mobile off` leaves nothing running
   (`--keep-daemon` to keep it). `pano off` closes it too.

The Mac's own system proxy is untouched: you can put a phone behind pano
without routing the Mac through it, or do both.

`pano mobile --no-wait` prints and returns; `pano mobile status` shows the
listener and every device seen; `pano status` includes the same block;
`--json` on any of them gives the `api.Mobile` structure.

## What the phone sees

Scanning the code opens the setup page. It detects the platform from the
browser (with tabs to switch), and it polls pano every 1.5 s to tick steps off:

**1 · Point Wi-Fi at pano.** The Mac's address and port as two tap-to-copy
cards, with the exact path for the platform (iOS: *Settings › Wi-Fi › ⓘ ›
Configure Proxy › Manual*). The moment the phone's first request arrives
*through* the proxy — including the page's own polling, which now travels as
a proxy request — the step turns green.

**2 · Trust pano's certificate.** A download button for the right format:

| Platform | File | What the OS does with it |
|---|---|---|
| iOS / iPadOS | `pano-ca.mobileconfig` — a configuration profile named **pano CA** with a description that says which Mac made it and how to remove it | Safari asks to allow the download; *Settings › Profile Downloaded › Install*; then *Settings › General › About › Certificate Trust Settings › pano CA* |
| Android | `pano-ca.crt` (DER) | Android 11+: *Settings › Security & privacy › More security › Encryption & credentials › Install a certificate › CA certificate*, pick the file. Android 10 and older install it straight from the download. |
| Other | `pano-ca.pem` | Whatever the OS or app uses |

The page then probes `https://pano.internal/_pano/ok` through the proxy. That
request only succeeds when the device has completed a TLS handshake against a
certificate pano minted, so a 204 is proof of trust — the step turns green and
the page says *All set*. If instead the device keeps *rejecting* the
handshake (the classic iOS state after installing the profile but before
enabling full trust), the page says so and points at the Certificate Trust
Settings screen.

**When you're done.** The page ends with the removal path for the platform.
The profile's own description repeats it, so someone finding it in Settings a
month later knows what it is.

## `pano.internal`

Every device whose traffic already goes through pano can open
**http://pano.internal** (or https://) from any network and get the same page.
The name deliberately does not exist in DNS — `.internal` is ICANN-reserved
for private use — because through a proxy the browser never resolves it; pano
recognises the host and answers itself. Nothing about it is recorded as a
flow.

`http://pano.internal/ssl` returns the certificate in the format the requesting
platform installs from a browser (profile / DER / PEM), the same convention
as `proxy.man/ssl` and `chls.pro/ssl`. `/_pano/pano-ca.mobileconfig`,
`/_pano/pano-ca.crt` and `/_pano/pano-ca.pem` are the explicit forms.

The Mac can use it too: with `pano on`, `curl http://pano.internal/_pano/setup.json`
shows the same status the phone sees.

## Reading a device's traffic

Every flow records its client address. In lists:

- `pano flows --client 192.168.1.40` / `pano tail --client 192.168.1.40` — one
  device; `--client remote` — every device that is not this Mac.
- `pano ui`: `M` opens the Mobile drawer — `⏎` opens or closes the listener,
  the QR code is right there to scan, and every device is listed with its
  state. `/` then `client=192.168.1.40` (or `device=`) filters the list; rows
  from other devices carry a `▯` flag; the header shows a `▯ mobile
  192.168.1.23:9091 · iPhone` chip while the listener is open.
- MCP: `pano_flows client="192.168.1.40"`; `pano_status` lists every device
  with its proxy/https state and request count.

Decryption follows the same policy as for the Mac (`pano decrypt`). Apps that
pin their certificate refuse the handshake exactly as they do on macOS; they
show up under **rejected** and `pano decrypt never add <host>` leaves them
alone. The default `never` list already covers the Apple daemons that pin
(push, iCloud, CloudKit, Maps), which matters more on iOS than on a Mac.

## Platform notes

### iOS / iPadOS

- A profile-installed root is **not trusted until you enable it** under
  *General › About › Certificate Trust Settings*. Until then every HTTPS
  connection fails with a certificate error and pano counts a rejection.
- The proxy setting is **per Wi-Fi network**. Joining another network drops
  it; the certificate stays.
- Apps that use `NSURLSession` / `URLSession` honour the Wi-Fi proxy
  automatically. Some cross-platform stacks do not (Flutter's Dart
  `HttpClient`, Go binaries, some game engines): they need the proxy set in
  code or an app-level setting.
- VPN apps and "Private Relay" (Settings › Apple ID › iCloud › Private Relay)
  route around a Wi-Fi proxy. Turn them off for the session.
- **iOS Simulator**: it shares the Mac's network stack, so `pano on` already
  proxies it. It has its own trust store:
  `xcrun simctl keychain booted add-root-cert ~/.pano/ca.pem`, then in the
  simulator *Settings › General › About › Certificate Trust Settings*.

### Android

- Since Android 7 (API 24) an app trusts **user-installed CAs only if it opts
  in**. The system browser and WebViews do; your own app needs a
  `network_security_config.xml` with `<certificates src="user"/>` inside
  `<debug-overrides>` (the setup page shows the exact XML) — debug overrides
  never apply to release builds. Third-party apps you do not build will
  usually not trust it at all.
- Chrome on Android may try `https://192.168.1.23:9091` first (HTTPS-First
  Mode) and fall back; if it shows an error instead, type the `http://` URL
  explicitly.
- **Android Emulator**: `emulator -avd <name> -http-proxy 127.0.0.1:9091`
  (or the emulator's Settings › Proxy) uses the Mac's loopback proxy
  directly — no `pano mobile` needed. The certificate installs like on a
  device; for the *system* store you need a writable system image
  (`-writable-system`) and `adb root`.

### Something else on the LAN

Any client that can take an HTTP proxy — another Mac, a Raspberry Pi, a
container on another host, a smart TV in developer mode — works the same way:
proxy `192.168.1.23:9091`, certificate from `/_pano/pano-ca.pem`. Only
private-network sources are admitted.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `pano mobile` says no LAN address found | Wi-Fi or Ethernet is not connected, or the only addresses are on tunnels/VPNs. `pano mobile --ip <addr>` picks an interface by hand; the error lists the candidates. |
| The QR opens but the page never loads | Phone and Mac are on different networks (guest Wi-Fi, client isolation, a VPN on either side), or a firewall on the Mac blocks the port. Try `http://<ip>:<port>` typed by hand; check *System Settings › Network › Firewall*. |
| Step 1 never turns green | The proxy is set on a different Wi-Fi network than the one the phone is on, or a VPN/Private Relay bypasses it. The phone's requests are not reaching pano at all. |
| Step 2 stays on "checking" with `https ✕` in the terminal | The certificate is installed but not trusted (iOS: enable full trust; Android: installed as a *VPN and apps* cert but the app does not opt in). |
| Everything green, but one app shows network errors | That app pins. It appears under *rejected*; `pano decrypt never add <host>`. |
| The Mac's address changed (new network, DHCP) | `pano mobile status` warns; run `pano mobile` again to move the listener. |
| The page says the LAN listener is off | You opened it on the Mac (`http://127.0.0.1:9091`) without `pano mobile`. Fine for a look; phones need the listener. |

## Undoing it

On the phone: turn the proxy off in the Wi-Fi network's settings and remove
the certificate (iOS: *General › VPN & Device Management › pano CA › Remove*;
Android: *Encryption & credentials › Trusted credentials › User*). On the Mac:
`pano mobile off`, or just `pano off`. Do the phone first, or expect it to
have no internet in between: a Wi-Fi proxy pointing at a closed port fails
every request, and pano cannot reach out to fix that. `pano mobile off`,
`pano status` and `pano_status` keep reminding you while a device was seen
and the listener is closed.

The certificate a phone installs is the same 2-year root the Mac trusts (see
[SECURITY.md](../SECURITY.md)); `pano ca reset` invalidates it everywhere at
once.

## Compared with Proxyman / Charles

Both show you an IP and port in a window, have you type it into the phone,
then have you browse to `proxy.man/ssl` or `chls.pro/ssl` — which only works
once the proxy is set — install, trust, and hope. pano's differences:

- **One command, no window.** The QR code is in the terminal, and it encodes a
  page that works *before* the proxy is configured, so the phone gets the
  settings from the page instead of from your screen.
- **The page knows where you are.** Each step turns green from real signals —
  first proxied request, first successful TLS handshake — and the common
  failure (installed but not trusted) is named on the page and in the
  terminal.
- **The certificate is a proper profile**, named, described, with a stable
  identity so re-downloading replaces it, and a removal path written into it.
- **Scoped exposure.** Only the proxy, only to private addresses, only while
  you say so; the control API and MCP never leave loopback, and the action is
  terminal-only — an agent cannot open your proxy to the network over MCP.
- **Devices are first-class**: named from their User-Agent, listed
  everywhere with their state, filterable with `client=`.
