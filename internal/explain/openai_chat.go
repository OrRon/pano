package explain

import (
	"errors"
	"fmt"
	"strings"
)

type chatToolCall struct {
	id, typ, name string
	args          strings.Builder
}

type chatChoice struct {
	role         string
	content      strings.Builder
	hasContent   bool
	refusal      strings.Builder
	hasRefusal   bool
	reasoning    strings.Builder
	reasoningKey string // "reasoning_content" (DeepSeek) or "reasoning" (OpenRouter)
	toolCalls    map[int]*chatToolCall
	funcCall     *chatToolCall // legacy function_call
	finish       any
}

// reassembleOpenAIChat rebuilds a chat.completion object from
// chat.completion.chunk events. Verified against the Chat Completions
// streaming reference: choices[].delta carries role (once), content, refusal
// and tool_calls[] merged by index (id/type/function.name once,
// function.arguments appended); finish_reason arrives on a choice's last
// chunk; with stream_options.include_usage a final chunk with empty choices
// carries usage; the stream ends with "data: [DONE]".
func reassembleOpenAIChat(events []Event) (map[string]any, streamInfo, error) {
	var (
		info    streamInfo
		base    map[string]any
		choices = map[int]*chatChoice{}
		usage   map[string]any
	)
	for _, ev := range events {
		info.Events++
		data := strings.TrimSpace(ev.Data)
		if data == "[DONE]" {
			info.Complete = true
			info.Last = "[DONE]"
			continue
		}
		info.Last = "chunk"
		v, err := decodeObject([]byte(data))
		if err != nil {
			info.errorf("event %d: malformed JSON", info.Events)
			continue
		}
		if e := errorFromObject(v); e != "" {
			info.Last = "error"
			info.errorf("%s", e)
			continue
		}
		if base == nil {
			base = map[string]any{}
		}
		for k, val := range v {
			switch k {
			case "choices", "usage", "obfuscation":
				continue
			}
			if _, seen := base[k]; !seen || base[k] == nil {
				base[k] = deepCopy(val)
			}
		}
		if u := obj(v["usage"]); u != nil {
			usage = deepCopy(u).(map[string]any)
		}
		for _, c := range arr(v["choices"]) {
			cm := obj(c)
			if cm == nil {
				continue
			}
			idx := intOr(cm["index"], 0)
			ch := choices[idx]
			if ch == nil {
				ch = &chatChoice{toolCalls: map[int]*chatToolCall{}}
				choices[idx] = ch
			}
			d := obj(cm["delta"])
			if role := str(d["role"]); role != "" {
				ch.role = role
			}
			if s, ok := d["content"].(string); ok {
				ch.content.WriteString(s)
				ch.hasContent = true
			}
			if s, ok := d["refusal"].(string); ok {
				ch.refusal.WriteString(s)
				ch.hasRefusal = true
			}
			for _, key := range []string{"reasoning_content", "reasoning"} {
				if s, ok := d[key].(string); ok {
					ch.reasoning.WriteString(s)
					if ch.reasoningKey == "" {
						ch.reasoningKey = key
					}
				}
			}
			for n, tc := range arr(d["tool_calls"]) {
				tm := obj(tc)
				if tm == nil {
					continue
				}
				ti := intOr(tm["index"], n)
				t := ch.toolCalls[ti]
				if t == nil {
					t = &chatToolCall{}
					ch.toolCalls[ti] = t
				}
				mergeToolCall(t, tm)
			}
			if fc := obj(d["function_call"]); fc != nil {
				if ch.funcCall == nil {
					ch.funcCall = &chatToolCall{}
				}
				mergeToolCall(ch.funcCall, map[string]any{"function": fc})
			}
			if fr := cm["finish_reason"]; fr != nil {
				ch.finish = fr
			}
		}
	}
	if base == nil {
		return nil, info, errors.New("openai chat stream: no chunks")
	}
	base["object"] = "chat.completion"
	out := make([]any, 0, len(choices))
	for _, idx := range sortedKeys(choices) {
		ch := choices[idx]
		role := ch.role
		if role == "" {
			role = "assistant"
		}
		msg := map[string]any{"role": role, "content": nil, "refusal": nil}
		if ch.hasContent {
			msg["content"] = ch.content.String()
		}
		if ch.hasRefusal {
			msg["refusal"] = ch.refusal.String()
		}
		if ch.reasoningKey != "" {
			msg[ch.reasoningKey] = ch.reasoning.String()
		}
		if len(ch.toolCalls) > 0 {
			calls := make([]any, 0, len(ch.toolCalls))
			for _, ti := range sortedKeys(ch.toolCalls) {
				t := ch.toolCalls[ti]
				typ := t.typ
				if typ == "" {
					typ = "function"
				}
				calls = append(calls, map[string]any{
					"id":       t.id,
					"type":     typ,
					"function": map[string]any{"name": t.name, "arguments": t.args.String()},
				})
			}
			msg["tool_calls"] = calls
		}
		if ch.funcCall != nil {
			msg["function_call"] = map[string]any{"name": ch.funcCall.name, "arguments": ch.funcCall.args.String()}
		}
		out = append(out, map[string]any{
			"index":         idx,
			"message":       msg,
			"finish_reason": ch.finish,
			"logprobs":      nil,
		})
	}
	base["choices"] = out
	if usage != nil {
		base["usage"] = usage
	}
	return base, info, nil
}

func mergeToolCall(t *chatToolCall, tm map[string]any) {
	if id := str(tm["id"]); id != "" {
		t.id = id
	}
	if typ := str(tm["type"]); typ != "" {
		t.typ = typ
	}
	if f := obj(tm["function"]); f != nil {
		if name := str(f["name"]); name != "" {
			t.name = name
		}
		t.args.WriteString(str(f["arguments"]))
	}
}

// openAIChatUsage normalises a chat.completion usage object
// (prompt_tokens, completion_tokens, total_tokens,
// prompt_tokens_details.cached_tokens, completion_tokens_details.reasoning_tokens).
func openAIChatUsage(final map[string]any) map[string]any {
	u := obj(final["usage"])
	if u == nil {
		return nil
	}
	in := intOr(u["prompt_tokens"], 0)
	out := intOr(u["completion_tokens"], 0)
	res := map[string]any{
		"input_tokens":  in,
		"output_tokens": out,
		"total_tokens":  intOr(u["total_tokens"], in+out),
		"raw":           u,
	}
	if cr, ok := toInt(obj(u["prompt_tokens_details"])["cached_tokens"]); ok {
		res["cache_read_input_tokens"] = cr
	}
	if rt, ok := toInt(obj(u["completion_tokens_details"])["reasoning_tokens"]); ok {
		res["reasoning_tokens"] = rt
	}
	return res
}

func openAIChatStop(final map[string]any) string {
	choices := arr(final["choices"])
	if len(choices) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var reasons []string
	for _, c := range choices {
		fr := str(obj(c)["finish_reason"])
		if fr == "" || seen[fr] {
			continue
		}
		seen[fr] = true
		reasons = append(reasons, fr)
	}
	return strings.Join(reasons, ",")
}

func openAIChatItems(final map[string]any) []group {
	choices := arr(final["choices"])
	var groups []group
	for n, c := range choices {
		cm := obj(c)
		msg := obj(cm["message"])
		var items []item
		if s, ok := msg["reasoning_content"].(string); ok && s != "" {
			items = append(items, item{Kind: "reasoning", Text: s})
		} else if s, ok := msg["reasoning"].(string); ok && s != "" {
			items = append(items, item{Kind: "reasoning", Text: s})
		}
		if s, ok := msg["content"].(string); ok && s != "" {
			items = append(items, item{Kind: "text", Text: s})
		} else if parts := arr(msg["content"]); parts != nil {
			for _, p := range parts {
				pm := obj(p)
				if t := str(pm["text"]); t != "" {
					items = append(items, item{Kind: "text", Text: t})
				}
			}
		}
		if s, ok := msg["refusal"].(string); ok && s != "" {
			items = append(items, item{Kind: "refusal", Text: s})
		}
		for _, tc := range arr(msg["tool_calls"]) {
			tm := obj(tc)
			f := obj(tm["function"])
			it := item{Kind: "tool_call", Name: str(f["name"]), JSON: compactJSONString(f["arguments"])}
			if typ := str(tm["type"]); typ != "" && typ != "function" {
				it.Kind = typ
				if it.Name == "" {
					it.Name = str(obj(tm[typ])["name"])
				}
			}
			items = append(items, it)
		}
		if fc := obj(msg["function_call"]); fc != nil {
			items = append(items, item{Kind: "function_call", Name: str(fc["name"]), JSON: compactJSONString(fc["arguments"])})
		}
		g := group{Items: items}
		if len(choices) > 1 {
			fr := str(cm["finish_reason"])
			if fr == "" {
				fr = "-"
			}
			g.Label = fmt.Sprintf("choice %d (finish: %s)", intOr(cm["index"], n), fr)
		}
		groups = append(groups, g)
	}
	return groups
}

func openAIChatRequest(req map[string]any) reqSummary {
	rs := reqSummary{OK: true, Roles: map[string]int{}}
	var system []string
	for _, m := range arr(req["messages"]) {
		mm := obj(m)
		if mm == nil {
			continue
		}
		role := str(mm["role"])
		text := openAIContentText(mm["content"])
		if role == "system" || role == "developer" {
			system = append(system, text)
		}
		if calls := arr(mm["tool_calls"]); len(calls) > 0 {
			var names []string
			for _, tc := range calls {
				names = append(names, "[tool_call "+str(obj(obj(tc)["function"])["name"])+"]")
			}
			text = strings.TrimSpace(text + " " + strings.Join(names, " "))
		}
		rs.addMessage(role, text)
	}
	rs.System = strings.Join(system, "\n")
	for _, t := range arr(req["tools"]) {
		tm := obj(t)
		name := str(obj(tm["function"])["name"])
		if name == "" {
			name = str(tm["type"])
		}
		rs.addTool(name)
	}
	if req["max_completion_tokens"] != nil {
		rs.param("max_completion_tokens", req["max_completion_tokens"])
	} else {
		rs.param("max_tokens", req["max_tokens"])
	}
	rs.param("temperature", req["temperature"])
	rs.streamParam(req["stream"])
	if req["stream"] == true {
		if obj(req["stream_options"])["include_usage"] == true {
			rs.Params = append(rs.Params, "stream_options include_usage")
		} else {
			rs.Params = append(rs.Params, "stream_options -")
		}
	}
	if n, ok := toInt(req["n"]); ok && n > 1 {
		rs.Params = append(rs.Params, "n "+commas(n))
	}
	if e := str(req["reasoning_effort"]); e != "" {
		rs.Params = append(rs.Params, "reasoning_effort "+e)
	}
	return rs
}

// openAIContentText flattens a message content that is either a string or an
// array of typed parts.
func openAIContentText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	var parts []string
	for _, p := range arr(content) {
		pm := obj(p)
		switch typ := str(pm["type"]); typ {
		case "text", "input_text", "output_text":
			parts = append(parts, str(pm["text"]))
		case "":
			continue
		default:
			parts = append(parts, "["+typ+"]")
		}
	}
	return strings.Join(parts, " ")
}
