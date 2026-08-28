package view

// Realistic bodies shared by the tests.

const anthropicBody = `{"id":"msg_01XFDUDYJgAACzvnptvVoYEL","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"Hello! The quick brown fox jumps over the lazy dog. This is a long enough sentence to be elided in a summary view because it exceeds the configured string truncation length, which we set fairly low in the tests so that the elision path is exercised."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":112,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`

const openaiBody = `{"id":"chatcmpl-9x1AbCdEfGhIjKlMnOpQrStUv","object":"chat.completion","created":1719000000,"model":"gpt-4o-2024-05-13","choices":[{"index":0,"message":{"role":"assistant","content":"Sure — here is a haiku.\nOld pond, frog jumps in\nsplash."},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":14,"completion_tokens":21,"total_tokens":35},"system_fingerprint":"fp_1234abcd"}`

const anthropicRequest = `{"model":"claude-opus-5","max_tokens":1024,"temperature":0.7,"system":"You are terse.","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello."},{"role":"user","content":[{"type":"text","text":"Tell me a joke"}]}],"metadata":{"user_id":"user_7f3e"},"created_at":"2026-08-27T10:00:00Z","stream":false}`

const anthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01\",\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":10}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n" +
	": keep-alive\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const htmlBody = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Pano &amp; Friends</title><style>body{color:red}</style><script>var x = "<b>ignored</b>";</script></head><body><h1>Welcome</h1><p>Some <b>bold</b> text here.</p></body></html>`
