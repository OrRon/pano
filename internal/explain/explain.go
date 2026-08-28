package explain

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// DefaultMaxChars is the digest budget used when Options.MaxChars is zero.
const DefaultMaxChars = 4000

// Include names accepted in Options.Include.
const (
	IncludeFinal    = "final"    // reassembled response content blocks
	IncludeUsage    = "usage"    // token usage line
	IncludeTools    = "tools"    // tool names in the request line and tool-call inputs
	IncludeStop     = "stop"     // stop/finish reason
	IncludeSystem   = "system"   // the system prompt (truncated)
	IncludeMessages = "messages" // one line per request message
	IncludeThinking = "thinking" // thinking / reasoning text instead of a placeholder
	IncludeErrors   = "errors"   // errors line
	IncludeRequest  = "request"  // the raw request JSON, pretty-printed (truncated)
)

// DefaultInclude is the include set used when Options.Include is empty.
var DefaultInclude = []string{IncludeFinal, IncludeUsage, IncludeTools, IncludeStop, IncludeErrors}

var allIncludes = []string{
	IncludeFinal, IncludeUsage, IncludeTools, IncludeStop, IncludeSystem,
	IncludeMessages, IncludeThinking, IncludeErrors, IncludeRequest,
}

// ErrNotLLM is returned by Explain when the exchange is not recognised LLM
// traffic and no provider was forced.
var ErrNotLLM = errors.New("explain: not recognised LLM traffic")

// Options controls what Explain renders.
type Options struct {
	// Include selects digest sections; entries may also be comma-separated.
	// Defaults to DefaultInclude.
	Include []string
	// MaxChars caps the digest text. Defaults to DefaultMaxChars.
	MaxChars int
	// Provider forces a provider instead of detecting one.
	Provider string
}

// Result is a digest of one LLM exchange.
type Result struct {
	Provider string
	Model    string
	Stream   bool
	Status   int
	// Text is the rendered digest.
	Text string
	// Final is the response as the non-streaming API would have returned it:
	// the reassembled object for streams, the body itself otherwise. nil when
	// not applicable (errors, empty or non-JSON bodies).
	Final []byte
	// Usage is normalised token usage: input_tokens (all prompt tokens,
	// cached ones included), output_tokens, cache_read_input_tokens,
	// cache_creation_input_tokens, total_tokens, reasoning_tokens (each only
	// when the provider reports it) and the provider's own object under "raw".
	Usage      map[string]any
	StopReason string
	// Partial is true when a stream ended before its terminal event.
	Partial bool
	Errors  []string
}

// streamInfo is what a reassembler learnt about the stream beyond the object.
type streamInfo struct {
	Events   int
	Last     string // name (or type) of the last event
	Complete bool   // the terminal event was seen
	Errors   []string
}

const maxStreamErrors = 20

func (s *streamInfo) errorf(format string, args ...any) {
	if len(s.Errors) < maxStreamErrors {
		s.Errors = append(s.Errors, fmt.Sprintf(format, args...))
	}
}

// parseIncludes validates Options.Include.
func parseIncludes(in []string) (map[string]bool, error) {
	if len(in) == 0 {
		in = DefaultInclude
	}
	set := map[string]bool{}
	for _, raw := range in {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(strings.ToLower(s))
			if s == "" {
				continue
			}
			valid := false
			for _, k := range allIncludes {
				if s == k {
					valid = true
					break
				}
			}
			if !valid {
				return nil, fmt.Errorf("explain: unknown include %q (valid: %s)", s, strings.Join(allIncludes, ", "))
			}
			set[s] = true
		}
	}
	return set, nil
}

// Explain builds the digest of one exchange. reqBody and respBody are decoded
// bytes (any Content-Encoding already removed). status is the HTTP status of
// the response; bodies of error responses (≥ 400) are summarised into
// Result.Errors rather than reassembled.
func Explain(host, path string, status int, reqHeaders http.Header, reqBody []byte, respHeaders http.Header, respBody []byte, opts Options) (*Result, error) {
	_ = reqHeaders // request headers carry nothing the digest needs today
	inc, err := parseIncludes(opts.Include)
	if err != nil {
		return nil, err
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	respMIME := ""
	if respHeaders != nil {
		respMIME = respHeaders.Get("Content-Type")
	}
	provider := opts.Provider
	if provider == "" {
		p, ok := Detect(host, path, reqBody, respMIME)
		if !ok {
			return nil, ErrNotLLM
		}
		provider = p
	} else if !validProvider(provider) {
		return nil, fmt.Errorf("explain: unknown provider %q (valid: %s)", provider, strings.Join(Providers, ", "))
	}

	r := &Result{Provider: provider, Status: status}
	reqObj, reqErr := decodeObject(reqBody)
	if reqErr != nil {
		reqObj = nil
	}
	r.Model = str(reqObj["model"])
	if provider == Gemini {
		if m := geminiModelFromPath(path); m != "" {
			r.Model = m
		}
	}
	req := summariseRequest(provider, path, reqObj)
	req.Bytes = len(reqBody)

	wantStream := reqObj["stream"] == true
	if provider == Gemini && strings.Contains(strings.ToLower(path), ":streamgeneratecontent") {
		wantStream = true
	}
	trimmed := bytes.TrimSpace(respBody)
	isSSE := strings.Contains(strings.ToLower(respMIME), "text/event-stream") || (len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '[' && looksLikeSSE(trimmed))
	isGeminiArray := provider == Gemini && len(trimmed) > 0 && trimmed[0] == '['

	var (
		final map[string]any
		info  streamInfo
	)
	switch {
	case status >= 400:
		r.Stream = isSSE
		r.Errors = append(r.Errors, extractError(respBody, status))
	case len(trimmed) == 0:
		r.Stream = wantStream
		if wantStream {
			r.Partial = true
		} else {
			r.Errors = append(r.Errors, "empty response body")
		}
	case isSSE || isGeminiArray:
		r.Stream = true
		var rerr error
		final, info, rerr = reassemble(provider, respBody)
		r.Partial = !info.Complete
		r.Errors = append(r.Errors, info.Errors...)
		if rerr != nil {
			r.Errors = append(r.Errors, rerr.Error())
		}
	default:
		o, derr := decodeObject(respBody)
		if derr != nil {
			r.Errors = append(r.Errors, "response body is not a JSON object: "+derr.Error())
		} else {
			final = o
			if e := errorFromObject(o); e != "" {
				r.Errors = append(r.Errors, e)
			}
		}
	}

	var groups []group
	if final != nil {
		r.Final = marshalJSON(final, "")
		if m := modelOf(provider, final); m != "" {
			r.Model = m
		}
		r.Usage = usageOf(provider, final)
		r.StopReason = stopOf(provider, final)
		groups = itemsOf(provider, final)
	}
	r.Text = render(r, req, groups, info, inc, maxChars, reqBody)
	return r, nil
}

// Reassemble turns a provider's streamed response body (SSE, or a JSON array
// of chunks for Gemini) into the equivalent non-streaming JSON object.
// partial is true when the stream ended before its terminal event; the
// object returned then reflects everything received so far.
func Reassemble(provider string, sse []byte) (final []byte, partial bool, err error) {
	obj, info, err := reassemble(provider, sse)
	if err != nil {
		return nil, !info.Complete, err
	}
	return marshalJSON(obj, ""), !info.Complete, nil
}

func reassemble(provider string, body []byte) (map[string]any, streamInfo, error) {
	switch provider {
	case Anthropic:
		return reassembleAnthropic(ParseSSE(body))
	case OpenAIChat:
		return reassembleOpenAIChat(ParseSSE(body))
	case OpenAIResponses:
		return reassembleOpenAIResponses(ParseSSE(body))
	case Gemini:
		chunks, err := geminiChunks(body)
		if err != nil {
			return nil, streamInfo{}, err
		}
		return reassembleGemini(chunks)
	}
	return nil, streamInfo{}, fmt.Errorf("explain: unknown provider %q", provider)
}

func summariseRequest(provider, path string, req map[string]any) reqSummary {
	if req == nil {
		return reqSummary{}
	}
	switch provider {
	case Anthropic:
		return anthropicRequest(req)
	case OpenAIChat:
		return openAIChatRequest(req)
	case OpenAIResponses:
		return openAIResponsesRequest(req)
	case Gemini:
		return geminiRequest(req, path)
	}
	return reqSummary{OK: true}
}

func modelOf(provider string, final map[string]any) string {
	if provider == Gemini {
		return str(final["modelVersion"])
	}
	return str(final["model"])
}

func usageOf(provider string, final map[string]any) map[string]any {
	switch provider {
	case Anthropic:
		return anthropicUsage(final)
	case OpenAIChat:
		return openAIChatUsage(final)
	case OpenAIResponses:
		return openAIResponsesUsage(final)
	case Gemini:
		return geminiUsage(final)
	}
	return nil
}

func stopOf(provider string, final map[string]any) string {
	switch provider {
	case Anthropic:
		return str(final["stop_reason"])
	case OpenAIChat:
		return openAIChatStop(final)
	case OpenAIResponses:
		return openAIResponsesStop(final)
	case Gemini:
		return geminiStop(final)
	}
	return ""
}

func itemsOf(provider string, final map[string]any) []group {
	switch provider {
	case Anthropic:
		return anthropicItems(final)
	case OpenAIChat:
		return openAIChatItems(final)
	case OpenAIResponses:
		return openAIResponsesItems(final)
	case Gemini:
		return geminiItems(final)
	}
	return nil
}

// extractError summarises an error response body of any provider.
func extractError(body []byte, status int) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fmt.Sprintf("HTTP %d %s", status, http.StatusText(status))
	}
	if looksLikeSSE(trimmed) && trimmed[0] != '{' {
		for _, ev := range ParseSSE(trimmed) {
			if m, err := decodeObject([]byte(ev.Data)); err == nil {
				if e := errorFromObject(m); e != "" {
					return fmt.Sprintf("HTTP %d: %s", status, e)
				}
			}
		}
	}
	if m, err := decodeObject(trimmed); err == nil {
		if e := errorFromObject(m); e != "" {
			return fmt.Sprintf("HTTP %d: %s", status, e)
		}
		if msg := str(m["message"]); msg != "" {
			return fmt.Sprintf("HTTP %d: %s", status, msg)
		}
	}
	return fmt.Sprintf("HTTP %d: %s", status, truncRunes(string(trimmed), 200))
}

// errorFromObject formats the "error" member of a provider response, or ""
// when there is none. Anthropic: {"type":"error","error":{"type","message"}};
// OpenAI: {"error":{"message","type","code"}}; Gemini: {"error":{"code",
// "message","status"}}.
func errorFromObject(m map[string]any) string {
	e := obj(m["error"])
	if e == nil {
		if s := str(m["error"]); s != "" {
			return s
		}
		return ""
	}
	kind := str(e["type"])
	if kind == "" {
		kind = str(e["status"])
	}
	if kind == "" && e["code"] != nil {
		kind = fmt.Sprint(e["code"])
	}
	msg := str(e["message"])
	switch {
	case kind != "" && msg != "":
		return kind + ": " + msg
	case msg != "":
		return msg
	case kind != "":
		return kind
	}
	return string(marshalJSON(e, ""))
}

// sortedKeys returns the keys of an int-keyed map in ascending order.
func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
