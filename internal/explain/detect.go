package explain

import (
	"regexp"
	"strings"
)

// Provider identifiers.
const (
	// Anthropic is the Messages API: POST /v1/messages, streamed or not.
	Anthropic = "anthropic"
	// OpenAIChat is Chat Completions: POST /v1/chat/completions, including the
	// compatible APIs exposed by Azure, OpenRouter, Groq, Together, DeepSeek,
	// xAI, Mistral and others.
	OpenAIChat = "openai-chat"
	// OpenAIResponses is the Responses API: POST /v1/responses.
	OpenAIResponses = "openai-responses"
	// Gemini is generativelanguage.googleapis.com :generateContent and
	// :streamGenerateContent (best effort).
	Gemini = "gemini"
)

// Providers lists every provider identifier.
var Providers = []string{Anthropic, OpenAIChat, OpenAIResponses, Gemini}

func validProvider(p string) bool {
	for _, q := range Providers {
		if p == q {
			return true
		}
	}
	return false
}

// Detect identifies the provider from the host, request path, request body
// shape and response content type. ok is false for traffic that is not a
// recognised LLM completion call.
//
// Path suffixes are checked first so that gateways and proxies hosted on any
// domain are recognised; the request body shape is the fallback for providers
// reached through non-standard paths (Bedrock, Vertex, self-hosted gateways).
func Detect(host, path string, reqBody []byte, respMIME string) (provider string, ok bool) {
	h := strings.ToLower(host)
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	p := path
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	lp := strings.ToLower(strings.TrimSuffix(p, "/"))

	// Sibling endpoints whose bodies look like completions but are not.
	for _, suffix := range []string{"/count_tokens", "/batches", "/embeddings", "/models", "/files"} {
		if strings.HasSuffix(lp, suffix) {
			return "", false
		}
	}

	switch {
	case strings.HasSuffix(lp, "/chat/completions"):
		return OpenAIChat, true
	case strings.Contains(lp, ":generatecontent") || strings.Contains(lp, ":streamgeneratecontent"):
		return Gemini, true
	case strings.HasSuffix(lp, "/v1/messages"),
		strings.HasSuffix(lp, "/messages") && strings.Contains(h, "anthropic"):
		return Anthropic, true
	case strings.HasSuffix(lp, "/responses") && (strings.Contains(h, "openai") || strings.Contains(h, "azure")):
		return OpenAIResponses, true
	}

	if !mimeMayBeLLM(respMIME) {
		return "", false
	}
	m, err := decodeObject(reqBody)
	if err != nil {
		return "", false
	}
	_, hasMessages := m["messages"]
	_, hasModel := m["model"]
	switch {
	case m["contents"] != nil:
		return Gemini, true
	case hasMessages && m["max_tokens"] != nil &&
		(m["system"] != nil || m["anthropic_version"] != nil || strings.Contains(h, "anthropic")):
		return Anthropic, true
	case hasMessages && m["anthropic_version"] != nil:
		return Anthropic, true
	case hasMessages && hasModel:
		return OpenAIChat, true
	case (m["input"] != nil || m["instructions"] != nil) && (hasModel || strings.HasSuffix(lp, "/responses")):
		return OpenAIResponses, true
	}
	return "", false
}

// mimeMayBeLLM reports whether a response content type is compatible with an
// LLM API response. An empty value (unknown, or request-only) is accepted.
func mimeMayBeLLM(ct string) bool {
	ct = strings.ToLower(ct)
	return ct == "" || strings.Contains(ct, "json") || strings.Contains(ct, "event-stream") || strings.Contains(ct, "text/plain")
}

var geminiModelRe = regexp.MustCompile(`/models/([^/:?]+):`)

// geminiModelFromPath extracts the model from a Gemini request path such as
// /v1beta/models/gemini-2.5-flash:streamGenerateContent.
func geminiModelFromPath(path string) string {
	if m := geminiModelRe.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}
