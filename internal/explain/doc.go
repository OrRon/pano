// Package explain turns captured LLM API traffic into compact digests.
//
// The proxy stores raw request and response bodies. LLM responses are very
// often server-sent event streams made of hundreds of tiny events, which an
// agent should never have to read. This package:
//
//   - detects LLM traffic from the host, path, request body and response
//     content type ([Detect]);
//   - reassembles a streamed response into the JSON object the same call would
//     have returned without streaming ([Reassemble]);
//   - renders a short, deterministic text digest of the exchange together with
//     normalised token usage and the stop reason ([Explain]).
//
// Supported providers are the Anthropic Messages API, OpenAI Chat Completions
// (and the many compatible APIs that share its wire format), the OpenAI
// Responses API, and, best effort, the Gemini generateContent API.
//
// [ParseSSE] is a small, tolerant event-stream parser that the reassemblers
// share; it is exported because other parts of pano render raw streams too.
package explain
