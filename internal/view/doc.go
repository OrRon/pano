// Package view renders captured HTTP bodies and headers into the compact,
// token-efficient text that pano shows to LLM agents and humans.
//
// The package has four responsibilities:
//
//   - Render turns a raw wire body into one of five views (summary, schema,
//     truncated, pretty, raw), after removing any Content-Encoding and
//     optionally selecting a sub-value with a gjson/JSONPath expression.
//     Binary bodies are never inlined.
//   - Decode strips gzip, deflate, brotli and zstd encodings with an output
//     bound so a small compressed body cannot expand without limit.
//   - RedactHeaders, RedactText and Mask hide credentials (API keys, bearer
//     tokens, cookies, JWTs, password fields, …) while keeping a short stable
//     fingerprint so two occurrences of the same secret can still be matched.
//   - DiffJSON, DiffText and DiffHeaders produce short structural diffs
//     between two bodies or header sets.
//
// The package depends only on internal/mimeclass and third-party helpers; it
// must not import internal/store.
package view
