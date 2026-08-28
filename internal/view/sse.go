package view

import (
	"strings"
)

// sseEvent is one parsed text/event-stream event.
type sseEvent struct {
	name string // "event:" field, or "message" when absent
	data string // "data:" lines joined with "\n"
	id   string
}

// maxSSEEvents bounds how many events the summary and schema views parse.
const maxSSEEvents = 10000

// parseSSE splits a text/event-stream body into events. It tolerates CRLF
// line endings and a missing terminating blank line.
func parseSSE(b []byte) []sseEvent {
	var (
		events []sseEvent
		cur    sseEvent
		data   []string
		have   bool
	)
	flush := func() {
		if !have {
			return
		}
		if cur.name == "" {
			cur.name = "message"
		}
		cur.data = strings.Join(data, "\n")
		events = append(events, cur)
		cur, data, have = sseEvent{}, nil, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			if len(events) >= maxSSEEvents {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keep-alive
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			cur.name, have = value, true
		case "data":
			data, have = append(data, value), true
		case "id":
			cur.id, have = value, true
		case "retry":
			have = true
		}
	}
	flush()
	return events
}

// looksLikeSSE sniffs whether an untyped text body is an event stream.
func looksLikeSSE(b []byte) bool {
	s := strings.TrimLeft(string(cutHead(b, 512)), " \t\r\n")
	return strings.HasPrefix(s, "data:") || strings.HasPrefix(s, "event:")
}

// llmEventNames are event names emitted by streaming LLM APIs.
var llmEventNames = map[string]bool{
	"message_start":        true,
	"message_delta":        true,
	"message_stop":         true,
	"content_block_start":  true,
	"content_block_delta":  true,
	"content_block_stop":   true,
	"response.created":     true,
	"response.completed":   true,
	"response.output_text": true,
}

// isLLMStream guesses whether an event stream carries LLM completions.
func isLLMStream(events []sseEvent) bool {
	for i, ev := range events {
		if llmEventNames[ev.name] || strings.HasPrefix(ev.name, "response.") {
			return true
		}
		if strings.Contains(ev.data, `"chat.completion.chunk"`) ||
			strings.Contains(ev.data, `"candidates"`) ||
			strings.TrimSpace(ev.data) == "[DONE]" {
			return true
		}
		if i > 20 {
			break
		}
	}
	return false
}

// eventNameCounts returns distinct event names in first-seen order with
// their counts.
func eventNameCounts(events []sseEvent) ([]string, map[string]int) {
	counts := map[string]int{}
	var order []string
	for _, ev := range events {
		if counts[ev.name] == 0 {
			order = append(order, ev.name)
		}
		counts[ev.name]++
	}
	return order, counts
}
