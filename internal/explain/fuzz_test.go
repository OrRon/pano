package explain

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func fuzzSeeds(f *testing.F) [][]byte {
	f.Helper()
	names, _ := filepath.Glob(filepath.Join("testdata", "*"))
	var seeds [][]byte
	for _, n := range names {
		if filepath.Ext(n) == ".txt" {
			continue
		}
		if b, err := os.ReadFile(n); err == nil {
			seeds = append(seeds, b)
		}
	}
	return seeds
}

func FuzzParseSSE(f *testing.F) {
	for _, s := range fuzzSeeds(f) {
		f.Add(s)
	}
	f.Add([]byte("data: a\r\ndata: b\r\r\n\n:c\nevent:\nid:1\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, ev := range ParseSSE(b) {
			_ = ev.Name + ev.Data + ev.ID
		}
	})
}

func FuzzReassemble(f *testing.F) {
	for _, s := range fuzzSeeds(f) {
		for i := range Providers {
			f.Add(uint8(i), s)
		}
	}
	f.Add(uint8(0), []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"content\":[]}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":7,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\"}}\n\n"))
	f.Add(uint8(1), []byte("data: {\"choices\":[{\"index\":-1,\"delta\":{\"tool_calls\":[{\"index\":99}]}}]}\n\ndata: [DONE]\n"))
	f.Add(uint8(2), []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":3,\"content_index\":2,\"delta\":\"x\"}\n\n"))
	f.Add(uint8(3), []byte("[{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"a\"},{\"text\":\"b\",\"thought\":true}]}}]},{}]"))
	f.Fuzz(func(t *testing.T, which uint8, body []byte) {
		provider := Providers[int(which)%len(Providers)]
		final, _, err := Reassemble(provider, body)
		if err == nil && !json.Valid(final) {
			t.Fatalf("%s: Reassemble returned invalid JSON: %s", provider, final)
		}
		// Explain must never panic either, whatever the body looks like.
		for _, status := range []int{200, 500} {
			r, err := Explain("h", "/p", status, nil, body, http.Header{"Content-Type": {"text/event-stream"}}, body, Options{Provider: provider, MaxChars: 300})
			if err != nil {
				t.Fatalf("%s: Explain: %v", provider, err)
			}
			if len([]rune(r.Text)) > 300 {
				t.Fatalf("%s: digest over budget: %d", provider, len([]rune(r.Text)))
			}
		}
	})
}
