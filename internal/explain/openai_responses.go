package explain

import (
	"errors"
	"fmt"
	"strings"
)

type partKey struct{ item, part int }

// reassembleOpenAIResponses rebuilds a Response object from a Responses API
// event stream. Verified against the SDK event types: response.created /
// response.in_progress / response.completed / response.failed /
// response.incomplete carry the full response (the terminal ones with usage);
// response.output_item.added/done carry the item at output_index;
// response.content_part.added/done carry the part at content_index;
// response.output_text.delta, response.refusal.delta,
// response.function_call_arguments.delta and
// response.reasoning_summary_text.delta carry a delta string keyed by
// output_index (and content_index / summary_index); the matching .done events
// carry the full text/arguments; error carries code and message.
//
// The terminal event's response is authoritative. Without one, the object is
// assembled from the item and delta events and marked partial.
func reassembleOpenAIResponses(events []Event) (map[string]any, streamInfo, error) {
	var (
		info     streamInfo
		resp     map[string]any
		terminal bool
		items    = map[int]map[string]any{}
		done     = map[int]bool{}
		textBuf  = map[partKey]*strings.Builder{}
		textDone = map[partKey]string{}
		refBuf   = map[partKey]*strings.Builder{}
		refDone  = map[partKey]string{}
		argsBuf  = map[int]*strings.Builder{}
		argsDone = map[int]string{}
		sumBuf   = map[partKey]*strings.Builder{}
		sumDone  = map[partKey]string{}
	)
	add := func(m map[partKey]*strings.Builder, k partKey, s string) {
		b := m[k]
		if b == nil {
			b = &strings.Builder{}
			m[k] = b
		}
		b.WriteString(s)
	}
	for _, ev := range events {
		info.Events++
		info.Last = ev.Name
		v, err := decodeObject([]byte(ev.Data))
		if err != nil {
			info.errorf("event %d (%s): malformed JSON", info.Events, ev.Name)
			continue
		}
		typ := str(v["type"])
		if typ != "" {
			info.Last = typ
		}
		idx := intOr(v["output_index"], 0)
		ci := intOr(v["content_index"], 0)
		si := intOr(v["summary_index"], 0)
		switch typ {
		case "response.created", "response.in_progress", "response.queued":
			if r := obj(v["response"]); r != nil {
				resp = deepCopy(r).(map[string]any)
			}
		case "response.completed", "response.failed", "response.incomplete":
			if r := obj(v["response"]); r != nil {
				resp = deepCopy(r).(map[string]any)
				terminal = true
				info.Complete = true
				switch typ {
				case "response.failed":
					info.errorf("%s", errorFromObject(resp))
				case "response.incomplete":
					info.errorf("incomplete: %s", str(obj(resp["incomplete_details"])["reason"]))
				}
			}
		case "response.output_item.added":
			if it := obj(v["item"]); it != nil {
				items[idx] = deepCopy(it).(map[string]any)
			}
		case "response.output_item.done":
			if it := obj(v["item"]); it != nil {
				items[idx] = deepCopy(it).(map[string]any)
				done[idx] = true
			}
		case "response.content_part.added", "response.content_part.done":
			it := items[idx]
			if it == nil {
				it = map[string]any{"type": "message", "role": "assistant", "content": []any{}}
				items[idx] = it
			}
			content := arr(it["content"])
			for len(content) <= ci {
				content = append(content, nil)
			}
			if p := obj(v["part"]); p != nil {
				content[ci] = deepCopy(p)
			}
			it["content"] = content
		case "response.output_text.delta", "response.reasoning_text.delta":
			add(textBuf, partKey{idx, ci}, str(v["delta"]))
		case "response.output_text.done", "response.reasoning_text.done":
			textDone[partKey{idx, ci}] = str(v["text"])
		case "response.refusal.delta":
			add(refBuf, partKey{idx, ci}, str(v["delta"]))
		case "response.refusal.done":
			refDone[partKey{idx, ci}] = str(v["refusal"])
		case "response.function_call_arguments.delta":
			b := argsBuf[idx]
			if b == nil {
				b = &strings.Builder{}
				argsBuf[idx] = b
			}
			b.WriteString(str(v["delta"]))
		case "response.function_call_arguments.done":
			argsDone[idx] = str(v["arguments"])
		case "response.reasoning_summary_text.delta":
			add(sumBuf, partKey{idx, si}, str(v["delta"]))
		case "response.reasoning_summary_text.done":
			sumDone[partKey{idx, si}] = str(v["text"])
		case "error":
			info.errorf("%s", errorFromObject(map[string]any{"error": v}))
		}
	}
	if terminal && resp != nil {
		return resp, info, nil
	}
	if resp == nil && len(items) == 0 {
		return nil, info, errors.New("openai responses stream: no response events")
	}
	if resp == nil {
		resp = map[string]any{"object": "response"}
	}
	if str(resp["status"]) == "" {
		resp["status"] = "in_progress"
	}
	output := make([]any, 0, len(items))
	for _, idx := range sortedKeys(items) {
		it := items[idx]
		if !done[idx] {
			applyResponseDeltas(it, idx, textBuf, textDone, refBuf, refDone, argsBuf, argsDone, sumBuf, sumDone)
		}
		output = append(output, it)
	}
	resp["output"] = output
	return resp, info, nil
}

// applyResponseDeltas fills an output item that never received its
// output_item.done event from the accumulated deltas.
func applyResponseDeltas(it map[string]any, idx int,
	textBuf map[partKey]*strings.Builder, textDone map[partKey]string,
	refBuf map[partKey]*strings.Builder, refDone map[partKey]string,
	argsBuf map[int]*strings.Builder, argsDone map[int]string,
	sumBuf map[partKey]*strings.Builder, sumDone map[partKey]string,
) {
	partType := "output_text"
	if str(it["type"]) == "reasoning" {
		partType = "reasoning_text"
	}
	content := arr(it["content"])
	maxPart := len(content) - 1
	for k := range textBuf {
		if k.item == idx && k.part > maxPart {
			maxPart = k.part
		}
	}
	for k := range refBuf {
		if k.item == idx && k.part > maxPart {
			maxPart = k.part
		}
	}
	for ci := 0; ci <= maxPart; ci++ {
		for len(content) <= ci {
			content = append(content, nil)
		}
		p := obj(content[ci])
		if p == nil {
			p = map[string]any{"type": partType, "text": "", "annotations": []any{}}
			if _, isRef := refBuf[partKey{idx, ci}]; isRef {
				p = map[string]any{"type": "refusal", "refusal": ""}
			}
			content[ci] = p
		}
		k := partKey{idx, ci}
		if s, ok := textDone[k]; ok {
			p["text"] = s
		} else if b := textBuf[k]; b != nil {
			p["text"] = str(p["text"]) + b.String()
		}
		if s, ok := refDone[k]; ok {
			p["refusal"] = s
		} else if b := refBuf[k]; b != nil {
			p["refusal"] = str(p["refusal"]) + b.String()
		}
	}
	if len(content) > 0 {
		it["content"] = content
	}
	if s, ok := argsDone[idx]; ok {
		it["arguments"] = s
	} else if b := argsBuf[idx]; b != nil {
		it["arguments"] = str(it["arguments"]) + b.String()
	}
	summary := arr(it["summary"])
	maxSum := len(summary) - 1
	for k := range sumBuf {
		if k.item == idx && k.part > maxSum {
			maxSum = k.part
		}
	}
	for k := range sumDone {
		if k.item == idx && k.part > maxSum {
			maxSum = k.part
		}
	}
	for si := 0; si <= maxSum; si++ {
		for len(summary) <= si {
			summary = append(summary, map[string]any{"type": "summary_text", "text": ""})
		}
		p := obj(summary[si])
		if p == nil {
			p = map[string]any{"type": "summary_text", "text": ""}
			summary[si] = p
		}
		k := partKey{idx, si}
		if s, ok := sumDone[k]; ok {
			p["text"] = s
		} else if b := sumBuf[k]; b != nil {
			p["text"] = str(p["text"]) + b.String()
		}
	}
	if len(summary) > 0 {
		it["summary"] = summary
	}
}

// openAIResponsesUsage normalises a Response usage object (input_tokens,
// output_tokens, total_tokens, input_tokens_details.cached_tokens,
// input_tokens_details.cache_write_tokens, output_tokens_details.reasoning_tokens).
func openAIResponsesUsage(final map[string]any) map[string]any {
	u := obj(final["usage"])
	if u == nil {
		return nil
	}
	in := intOr(u["input_tokens"], 0)
	out := intOr(u["output_tokens"], 0)
	res := map[string]any{
		"input_tokens":  in,
		"output_tokens": out,
		"total_tokens":  intOr(u["total_tokens"], in+out),
		"raw":           u,
	}
	details := obj(u["input_tokens_details"])
	if cr, ok := toInt(details["cached_tokens"]); ok {
		res["cache_read_input_tokens"] = cr
	}
	if cw, ok := toInt(details["cache_write_tokens"]); ok {
		res["cache_creation_input_tokens"] = cw
	}
	if rt, ok := toInt(obj(u["output_tokens_details"])["reasoning_tokens"]); ok {
		res["reasoning_tokens"] = rt
	}
	return res
}

func openAIResponsesStop(final map[string]any) string {
	status := str(final["status"])
	if reason := str(obj(final["incomplete_details"])["reason"]); reason != "" {
		return status + " (" + reason + ")"
	}
	return status
}

func openAIResponsesItems(final map[string]any) []group {
	var items []item
	for _, o := range arr(final["output"]) {
		om := obj(o)
		if om == nil {
			continue
		}
		typ := str(om["type"])
		switch typ {
		case "message":
			for _, p := range arr(om["content"]) {
				pm := obj(p)
				switch pt := str(pm["type"]); pt {
				case "output_text":
					items = append(items, item{Kind: "text", Text: str(pm["text"])})
				case "refusal":
					items = append(items, item{Kind: "refusal", Text: str(pm["refusal"])})
				default:
					items = append(items, item{Kind: pt})
				}
			}
		case "function_call", "custom_tool_call", "mcp_call":
			name := str(om["name"])
			args := om["arguments"]
			if args == nil {
				args = om["input"]
			}
			items = append(items, item{Kind: typ, Name: name, JSON: compactJSONString(args)})
		case "reasoning":
			var parts []string
			for _, s := range arr(om["summary"]) {
				if t := str(obj(s)["text"]); t != "" {
					parts = append(parts, t)
				}
			}
			for _, c := range arr(om["content"]) {
				if t := str(obj(c)["text"]); t != "" {
					parts = append(parts, t)
				}
			}
			it := item{Kind: "reasoning", Text: strings.Join(parts, "\n")}
			if it.Text == "" {
				it.Note = "(no summary)"
			}
			items = append(items, it)
		default:
			it := item{Kind: typ}
			if s := str(om["status"]); s != "" {
				it.Note = s
			}
			items = append(items, it)
		}
	}
	return []group{{Items: items}}
}

func openAIResponsesRequest(req map[string]any) reqSummary {
	rs := reqSummary{OK: true, Roles: map[string]int{}}
	rs.System = str(req["instructions"])
	switch in := req["input"].(type) {
	case string:
		rs.addMessage("user", in)
	case []any:
		for _, m := range in {
			mm := obj(m)
			if mm == nil {
				continue
			}
			role := str(mm["role"])
			switch typ := str(mm["type"]); {
			case role != "":
				rs.addMessage(role, openAIContentText(mm["content"]))
			case typ == "function_call":
				rs.addMessage("assistant", "[function_call "+str(mm["name"])+"]")
			case typ == "function_call_output":
				rs.addMessage("tool", truncRunes(compactJSONString(mm["output"]), 120))
			default:
				rs.addMessage(typ, "")
			}
		}
	}
	for _, t := range arr(req["tools"]) {
		tm := obj(t)
		name := str(tm["name"])
		if name == "" {
			name = str(tm["type"])
		}
		rs.addTool(name)
	}
	rs.param("max_output_tokens", req["max_output_tokens"])
	rs.param("temperature", req["temperature"])
	rs.streamParam(req["stream"])
	if e := str(obj(req["reasoning"])["effort"]); e != "" {
		rs.Params = append(rs.Params, "reasoning_effort "+e)
	}
	if p := str(req["previous_response_id"]); p != "" {
		rs.Params = append(rs.Params, fmt.Sprintf("previous_response_id %s", truncRunes(p, 24)))
	}
	return rs
}
