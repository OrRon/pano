package explain

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// geminiChunks splits a Gemini response body into GenerateContentResponse
// objects. streamGenerateContent returns SSE "data:" events with alt=sse and
// a JSON array of objects otherwise; generateContent returns one object.
func geminiChunks(body []byte) ([]map[string]any, error) {
	t := bytes.TrimSpace(body)
	if len(t) == 0 {
		return nil, errors.New("gemini: empty body")
	}
	switch t[0] {
	case '[':
		v, err := decodeJSON(t)
		if err != nil {
			return nil, fmt.Errorf("gemini: %w", err)
		}
		var chunks []map[string]any
		for _, c := range arr(v) {
			if m := obj(c); m != nil {
				chunks = append(chunks, m)
			}
		}
		return chunks, nil
	case '{':
		m, err := decodeObject(t)
		if err != nil {
			return nil, fmt.Errorf("gemini: %w", err)
		}
		return []map[string]any{m}, nil
	}
	var chunks []map[string]any
	for _, ev := range ParseSSE(t) {
		if m, err := decodeObject([]byte(ev.Data)); err == nil {
			chunks = append(chunks, m)
		}
	}
	if len(chunks) == 0 {
		return nil, errors.New("gemini: no JSON chunks in body")
	}
	return chunks, nil
}

type geminiCandidate struct {
	cand  map[string]any
	role  string
	parts []map[string]any
}

// reassembleGemini merges streamed GenerateContentResponse chunks: text parts
// are concatenated per candidate (thought parts separately), other parts are
// appended, and finishReason, usageMetadata, modelVersion and responseId are
// taken from the last chunk that carries them.
func reassembleGemini(chunks []map[string]any) (map[string]any, streamInfo, error) {
	var (
		info  streamInfo
		base  = map[string]any{}
		cands = map[int]*geminiCandidate{}
	)
	for _, chunk := range chunks {
		info.Events++
		info.Last = "chunk"
		if e := errorFromObject(chunk); e != "" {
			info.Last = "error"
			info.errorf("%s", e)
			continue
		}
		for k, val := range chunk {
			if k == "candidates" {
				continue
			}
			base[k] = deepCopy(val)
		}
		for n, c := range arr(chunk["candidates"]) {
			cm := obj(c)
			if cm == nil {
				continue
			}
			idx := intOr(cm["index"], n)
			gc := cands[idx]
			if gc == nil {
				gc = &geminiCandidate{cand: map[string]any{}}
				cands[idx] = gc
			}
			for k, val := range cm {
				if k == "content" {
					continue
				}
				gc.cand[k] = deepCopy(val)
			}
			if fr := str(cm["finishReason"]); fr != "" {
				info.Complete = true
			}
			content := obj(cm["content"])
			if role := str(content["role"]); role != "" {
				gc.role = role
			}
			for _, p := range arr(content["parts"]) {
				pm := obj(p)
				if pm == nil {
					continue
				}
				txt, isText := pm["text"].(string)
				if isText && len(gc.parts) > 0 {
					last := gc.parts[len(gc.parts)-1]
					if _, lastText := last["text"].(string); lastText && last["thought"] == pm["thought"] {
						last["text"] = str(last["text"]) + txt
						continue
					}
				}
				gc.parts = append(gc.parts, deepCopy(pm).(map[string]any))
			}
		}
	}
	if len(cands) == 0 && len(base) == 0 {
		return nil, info, errors.New("gemini stream: no candidates")
	}
	out := make([]any, 0, len(cands))
	for _, idx := range sortedKeys(cands) {
		gc := cands[idx]
		parts := make([]any, 0, len(gc.parts))
		for _, p := range gc.parts {
			parts = append(parts, p)
		}
		role := gc.role
		if role == "" {
			role = "model"
		}
		gc.cand["content"] = map[string]any{"parts": parts, "role": role}
		if _, ok := gc.cand["index"]; !ok && len(cands) > 1 {
			gc.cand["index"] = idx
		}
		out = append(out, gc.cand)
	}
	base["candidates"] = out
	return base, info, nil
}

// geminiUsage normalises usageMetadata (promptTokenCount,
// candidatesTokenCount, totalTokenCount, cachedContentTokenCount,
// thoughtsTokenCount).
func geminiUsage(final map[string]any) map[string]any {
	u := obj(final["usageMetadata"])
	if u == nil {
		return nil
	}
	in := intOr(u["promptTokenCount"], 0)
	out := intOr(u["candidatesTokenCount"], 0)
	res := map[string]any{
		"input_tokens":  in,
		"output_tokens": out,
		"total_tokens":  intOr(u["totalTokenCount"], in+out),
		"raw":           u,
	}
	if cr, ok := toInt(u["cachedContentTokenCount"]); ok {
		res["cache_read_input_tokens"] = cr
	}
	if rt, ok := toInt(u["thoughtsTokenCount"]); ok {
		res["reasoning_tokens"] = rt
	}
	return res
}

func geminiStop(final map[string]any) string {
	cands := arr(final["candidates"])
	if len(cands) == 0 {
		return ""
	}
	return str(obj(cands[0])["finishReason"])
}

func geminiItems(final map[string]any) []group {
	cands := arr(final["candidates"])
	var groups []group
	for n, c := range cands {
		cm := obj(c)
		var items []item
		for _, p := range arr(obj(cm["content"])["parts"]) {
			pm := obj(p)
			switch {
			case pm["text"] != nil:
				kind := "text"
				if pm["thought"] == true {
					kind = "thinking"
				}
				items = append(items, item{Kind: kind, Text: str(pm["text"])})
			case pm["functionCall"] != nil:
				fc := obj(pm["functionCall"])
				items = append(items, item{Kind: "function_call", Name: str(fc["name"]), JSON: compactJSONString(fc["args"])})
			case pm["inlineData"] != nil:
				items = append(items, item{Kind: "inline_data", Note: str(obj(pm["inlineData"])["mimeType"])})
			case pm["executableCode"] != nil:
				items = append(items, item{Kind: "executable_code", Text: str(obj(pm["executableCode"])["code"])})
			case pm["codeExecutionResult"] != nil:
				items = append(items, item{Kind: "code_execution_result", Text: str(obj(pm["codeExecutionResult"])["output"])})
			default:
				items = append(items, item{Kind: "part"})
			}
		}
		g := group{Items: items}
		if len(cands) > 1 {
			fr := str(cm["finishReason"])
			if fr == "" {
				fr = "-"
			}
			g.Label = fmt.Sprintf("candidate %d (finish: %s)", intOr(cm["index"], n), fr)
		}
		groups = append(groups, g)
	}
	return groups
}

func geminiRequest(req map[string]any, path string) reqSummary {
	rs := reqSummary{OK: true, Roles: map[string]int{}}
	rs.System = geminiPartsText(obj(req["systemInstruction"])["parts"])
	if rs.System == "" {
		rs.System = geminiPartsText(obj(req["system_instruction"])["parts"])
	}
	for _, c := range arr(req["contents"]) {
		cm := obj(c)
		if cm == nil {
			continue
		}
		role := str(cm["role"])
		if role == "" {
			role = "user"
		}
		rs.addMessage(role, geminiPartsText(cm["parts"]))
	}
	for _, t := range arr(req["tools"]) {
		tm := obj(t)
		decls := arr(tm["functionDeclarations"])
		if decls == nil {
			decls = arr(tm["function_declarations"])
		}
		if len(decls) == 0 {
			for k := range tm {
				rs.addTool(k)
			}
			continue
		}
		for _, d := range decls {
			rs.addTool(str(obj(d)["name"]))
		}
	}
	gc := obj(req["generationConfig"])
	if gc == nil {
		gc = obj(req["generation_config"])
	}
	rs.param("max_output_tokens", firstNonNil(gc["maxOutputTokens"], gc["max_output_tokens"]))
	rs.param("temperature", gc["temperature"])
	stream := strings.Contains(strings.ToLower(path), ":streamgeneratecontent")
	rs.streamParam(stream)
	if tc := obj(gc["thinkingConfig"]); tc != nil {
		if b, ok := toInt(tc["thinkingBudget"]); ok {
			rs.Params = append(rs.Params, "thinking budget "+commas(b))
		}
	}
	return rs
}

func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func geminiPartsText(parts any) string {
	var out []string
	for _, p := range arr(parts) {
		pm := obj(p)
		switch {
		case pm["text"] != nil:
			out = append(out, str(pm["text"]))
		case pm["functionCall"] != nil:
			out = append(out, "[function_call "+str(obj(pm["functionCall"])["name"])+"]")
		case pm["functionResponse"] != nil:
			out = append(out, "[function_response "+str(obj(pm["functionResponse"])["name"])+"]")
		case pm["inlineData"] != nil:
			out = append(out, "[inline_data]")
		case pm["fileData"] != nil:
			out = append(out, "[file_data]")
		}
	}
	return strings.Join(out, " ")
}
