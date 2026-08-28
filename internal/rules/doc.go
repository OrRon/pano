// Package rules implements pano's live traffic rules and breakpoints. An
// Engine satisfies proxy.Hooks: the proxy calls Request before contacting the
// origin and Response once response headers are known, and the engine matches
// the exchange against its rule set and applies the matching rules' actions.
//
// # Rules
//
// A rule (api.Rule) is a Match plus an ordered list of Actions. Rules are
// evaluated in priority order (higher first, then oldest first). Every field
// set on the Match must hold for the rule to fire:
//
//   - Host is a glob (internal/glob) matched against the flow host; a pattern
//     containing ':' is matched against host:port instead.
//   - Path is a prefix ("/v1/"), a glob ("/v1/*/models"), or a regexp when
//     wrapped in slashes and the inner text uses regexp syntax ("/^\/v[12]\//").
//   - Method is a case-insensitive list, Scheme is "http" or "https".
//   - Header maps a header name to a glob matched against the joined request
//     header value; an empty glob only requires the header to be present.
//   - Status is a response-phase spec: "500", "4xx", "400-499", "!2xx", "200|204".
//   - Phase is request, response or both. When empty it is derived from the
//     actions: a rule whose actions all run on the response side is a
//     response rule, otherwise it is a request rule (or a response rule when
//     Status is set).
//
// Actions run in order. Each applied action appends a flow.RuleHit to the flow.
// mock, block and breakpoint (drop) end the evaluation. Action types:
//
//	delay          sleep ms (+ random jitter_ms), cancelled with the request
//	set_header     set name: value on the request or response
//	remove_header  delete name
//	set_query      set name=value on the request URL
//	rewrite_body   json_patch (dotted paths -> values), regex + replace, or a
//	               text/template with .Host .Path .Method .Status .Body and
//	               .Header "Name"; gzip bodies are decoded and served plain
//	mock           answer with status/headers/body without contacting the origin
//	mock_every_n   like mock but only on every nth hit (value = n)
//	block          mode reset (drop the connection), timeout (hang for ms),
//	               or status (default: answer with status, default 502)
//	redirect       send the request to upstream ("http://localhost:3000")
//	throttle       limit the response body to kbps kilobytes per second
//	breakpoint     park the exchange until Resume (alias: hold)
//	tag            add tags to the flow
//
// mock, mock_every_n, block, redirect and set_query default to the request
// side; throttle is response only; the rest apply in whichever phase the rule
// is evaluated unless On pins them. Probability gates the whole rule, MaxHits
// disables it once reached and TTLSeconds/Expires remove it lazily.
//
// # Breakpoints
//
// A breakpoint sets the flow to flow.StateHeld, publishes flow.EvHeld and
// blocks the exchange until Resume is called, the client goes away, or
// Options.HoldTimeout elapses. Resume may edit the parked request (URL,
// method, headers, body) or response (status, headers, body) before it
// continues, or drop it.
//
// # Concurrency and persistence
//
// The compiled rule set lives behind an atomic pointer and is replaced on
// every change, so the hot path is lock-free and allocates nothing for rules
// that do not match. Changes are written atomically to Options.PersistPath as
// a JSON array of api.Rule.
package rules
