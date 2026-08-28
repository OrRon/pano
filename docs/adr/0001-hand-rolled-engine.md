# ADR 0001: Hand-rolled MITM engine instead of goproxy or martian

Status: accepted (2026-08)

## Context

pano needs a TLS-terminating HTTP proxy that speaks HTTP/1.1 **and HTTP/2**
on the decrypted side (browsers and LLM SDKs negotiate h2), passes
server-sent events through with per-read flushing, splices WebSockets, and
lets a rules engine and a capture sink observe and mutate both phases of an
exchange on the request goroutine.

The two established Go libraries did not fit at the time of writing:

- `elazarl/goproxy` shipped HTTP/2 MITM only in v1.9.0 on 2026-08-06, days
  before this decision, with an API shaped around its own handler chain and
  no control over flushing or body teeing.
- `google/martian` is archived and has no HTTP/2 interception.

The standard library already provides every primitive: `net/http` for the
front listener and the upstream `Transport`, `crypto/tls` with
`GetCertificate` for on-demand leafs, `http.ResponseController.Hijack` for
CONNECT, and `golang.org/x/net/http2.Server.ServeConn` to serve h2 on an
already-established `tls.Conn`.

## Decision

Write the engine in `internal/proxy` directly on the standard library:

- CONNECT → hijack → `tls.Server(conn, ca.TLSConfig())` with ALPN
  `h2, http/1.1` → branch on `NegotiatedProtocol`: `http2.Server.ServeConn`
  or `http.Server.Serve(oneConnListener)`. Both feed one protocol-agnostic
  `handleExchange`.
- One shared upstream `http.Transport` (`Proxy: nil`,
  `DisableCompression: true`, `ResponseHeaderTimeout: 0`).
- Rules and capture plug in through two small interfaces (`proxy.Hooks`,
  `proxy.Sink`); the engine knows nothing about SQLite or rules.
- A hand-written 40-line glob for host matching instead of a glob library.

## Consequences

- We own CONNECT handling, hop-by-hop stripping, flush heuristics, WebSocket
  framing and loop guards, and must test them ourselves (`e2e_test.go`
  covers h1, h2, SSE, WS, rules, bypass, truncation).
- No third-party proxy dependency to track; the only engine dependency is
  `golang.org/x/net/http2`, which is in maintenance mode. The h1 fallback
  path (`DisableH2`) exists so pano keeps working if `ServeConn` ever goes
  away.
- Reviewers can read the whole decryption path in `connect.go` and
  `handler.go` (~600 lines) instead of a library's plugin model.
- We do not get goproxy's ecosystem (existing handlers, examples). Given the
  agent-oriented surface is pano's actual product, that trade was cheap.
