package explain

import (
	"errors"
	"fmt"
	"strings"
)

// anthropicBlock accumulates one content block of a Messages stream.
type anthropicBlock struct {
	block       map[string]any
	text        strings.Builder
	partialJSON strings.Builder
	thinking    strings.Builder
	hasText     bool
	hasJSON     bool
	hasThinking bool
	signature   string
	citations   []any
}

// reassembleAnthropic rebuilds the Message object from a Messages API event
// stream. Verified against the streaming reference: message_start carries the
// Message with empty content; each block is content_block_start, deltas
// (text_delta, input_json_delta with partial_json, thinking_delta,
// signature_delta, citations_delta), content_block_stop; message_delta
// carries top-level changes (stop_reason, stop_sequence) and cumulative usage;
// message_stop ends the stream; ping and error may appear anywhere.
func reassembleAnthropic(events []Event) (map[string]any, streamInfo, error) {
	var (
		info   streamInfo
		msg    map[string]any
		blocks = map[int]*anthropicBlock{}
	)
	for _, ev := range events {
		info.Events++
		info.Last = ev.Name
		v, err := decodeObject([]byte(ev.Data))
		if err != nil {
			info.errorf("event %d (%s): malformed JSON", info.Events, ev.Name)
			continue
		}
		typ := str(v["type"])
		if info.Last == "" {
			info.Last = typ
		}
		switch typ {
		case "message_start":
			if m := obj(v["message"]); m != nil {
				msg = deepCopy(m).(map[string]any)
			}
		case "content_block_start":
			idx := intOr(v["index"], len(blocks))
			cb := obj(v["content_block"])
			if cb == nil {
				cb = map[string]any{}
			}
			blocks[idx] = &anthropicBlock{block: deepCopy(cb).(map[string]any)}
		case "content_block_delta":
			idx := intOr(v["index"], 0)
			d := obj(v["delta"])
			b := blocks[idx]
			if b == nil {
				b = &anthropicBlock{block: map[string]any{"type": anthropicBlockTypeForDelta(str(d["type"]))}}
				blocks[idx] = b
			}
			switch str(d["type"]) {
			case "text_delta":
				b.text.WriteString(str(d["text"]))
				b.hasText = true
			case "input_json_delta":
				b.partialJSON.WriteString(str(d["partial_json"]))
				b.hasJSON = true
			case "thinking_delta":
				b.thinking.WriteString(str(d["thinking"]))
				b.hasThinking = true
			case "signature_delta":
				b.signature = str(d["signature"])
			case "citations_delta":
				if c := d["citation"]; c != nil {
					b.citations = append(b.citations, deepCopy(c))
				}
			}
		case "message_delta":
			if msg == nil {
				continue
			}
			for k, val := range obj(v["delta"]) {
				msg[k] = deepCopy(val)
			}
			if u := obj(v["usage"]); u != nil {
				mu := obj(msg["usage"])
				if mu == nil {
					mu = map[string]any{}
				}
				for k, val := range u {
					mu[k] = deepCopy(val)
				}
				msg["usage"] = mu
			}
		case "message_stop":
			info.Complete = true
		case "error":
			e := obj(v["error"])
			info.errorf("%s", errorFromObject(map[string]any{"error": e}))
		}
	}
	if msg == nil {
		return nil, info, errors.New("anthropic stream: no message_start event")
	}
	content := make([]any, 0, len(blocks))
	for _, i := range sortedKeys(blocks) {
		b := blocks[i]
		cb := b.block
		if b.hasText {
			cb["text"] = str(cb["text"]) + b.text.String()
		}
		if b.hasJSON {
			raw := strings.TrimSpace(b.partialJSON.String())
			if raw == "" {
				cb["input"] = map[string]any{}
			} else if parsed, err := decodeJSON([]byte(raw)); err == nil {
				cb["input"] = parsed
			} else {
				cb["input"] = map[string]any{}
				info.errorf("content[%d] %s: tool input JSON incomplete: %s", i, str(cb["name"]), truncRunes(raw, 120))
			}
		}
		if b.hasThinking {
			cb["thinking"] = str(cb["thinking"]) + b.thinking.String()
		}
		if b.signature != "" {
			cb["signature"] = b.signature
		}
		if len(b.citations) > 0 {
			cb["citations"] = append(arr(cb["citations"]), b.citations...)
		}
		content = append(content, cb)
	}
	msg["content"] = content
	return msg, info, nil
}

func anthropicBlockTypeForDelta(deltaType string) string {
	switch deltaType {
	case "input_json_delta":
		return "tool_use"
	case "thinking_delta", "signature_delta":
		return "thinking"
	}
	return "text"
}

// anthropicUsage normalises a Message's usage. input_tokens in the wire
// format excludes cached tokens, so the normalised input is the sum of the
// uncached, cache-read and cache-creation counts.
func anthropicUsage(final map[string]any) map[string]any {
	u := obj(final["usage"])
	if u == nil {
		return nil
	}
	in := intOr(u["input_tokens"], 0)
	out := intOr(u["output_tokens"], 0)
	cr, hasCR := toInt(u["cache_read_input_tokens"])
	cc, hasCC := toInt(u["cache_creation_input_tokens"])
	res := map[string]any{
		"input_tokens":  in + cr + cc,
		"output_tokens": out,
		"total_tokens":  in + cr + cc + out,
		"raw":           u,
	}
	if hasCR {
		res["cache_read_input_tokens"] = cr
	}
	if hasCC {
		res["cache_creation_input_tokens"] = cc
	}
	return res
}

func anthropicItems(final map[string]any) []group {
	var items []item
	for _, c := range arr(final["content"]) {
		cb := obj(c)
		if cb == nil {
			continue
		}
		typ := str(cb["type"])
		switch typ {
		case "text":
			items = append(items, item{Kind: "text", Text: str(cb["text"])})
		case "tool_use", "server_tool_use", "mcp_tool_use":
			items = append(items, item{Kind: typ, Name: str(cb["name"]), JSON: compactJSONString(cb["input"])})
		case "thinking":
			items = append(items, item{Kind: "thinking", Text: str(cb["thinking"])})
		case "redacted_thinking":
			items = append(items, item{Kind: typ, Note: "(redacted)"})
		default:
			it := item{Kind: typ}
			if res := arr(cb["content"]); res != nil {
				it.Note = fmt.Sprintf("(%d results)", len(res))
			} else if s := str(cb["content"]); s != "" {
				it.Text = s
				it.Kind = typ
			}
			items = append(items, it)
		}
	}
	return []group{{Items: items}}
}

func anthropicRequest(req map[string]any) reqSummary {
	rs := reqSummary{OK: true, Roles: map[string]int{}}
	switch s := req["system"].(type) {
	case string:
		rs.System = s
	case []any:
		var parts []string
		for _, b := range s {
			if t := str(obj(b)["text"]); t != "" {
				parts = append(parts, t)
			}
		}
		rs.System = strings.Join(parts, "\n")
	}
	for _, m := range arr(req["messages"]) {
		mm := obj(m)
		if mm == nil {
			continue
		}
		role := str(mm["role"])
		rs.addMessage(role, anthropicContentText(mm["content"]))
	}
	for _, t := range arr(req["tools"]) {
		tm := obj(t)
		name := str(tm["name"])
		if name == "" {
			name = str(tm["type"])
		}
		rs.addTool(name)
	}
	rs.param("max_tokens", req["max_tokens"])
	rs.param("temperature", req["temperature"])
	rs.streamParam(req["stream"])
	if th := obj(req["thinking"]); th != nil {
		if b, ok := toInt(th["budget_tokens"]); ok {
			rs.Params = append(rs.Params, "thinking budget "+commas(b))
		} else if t := str(th["type"]); t != "" {
			rs.Params = append(rs.Params, "thinking "+t)
		}
	}
	return rs
}

// anthropicContentText flattens a message's content for a one-line summary.
func anthropicContentText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	var parts []string
	for _, b := range arr(content) {
		bm := obj(b)
		switch typ := str(bm["type"]); typ {
		case "text":
			parts = append(parts, str(bm["text"]))
		case "tool_use", "server_tool_use":
			parts = append(parts, "["+typ+" "+str(bm["name"])+"]")
		case "tool_result":
			parts = append(parts, "[tool_result "+truncRunes(anthropicContentText(bm["content"]), 60)+"]")
		case "":
			continue
		default:
			parts = append(parts, "["+typ+"]")
		}
	}
	return strings.Join(parts, " ")
}
