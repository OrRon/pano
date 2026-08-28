package explain

import (
	"reflect"
	"testing"
)

func TestParseSSE(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Event
	}{
		{"empty", "", nil},
		{"single", "event: ping\ndata: {}\n\n", []Event{{Name: "ping", Data: "{}"}}},
		{"crlf", "event: a\r\ndata: 1\r\n\r\nevent: b\r\ndata: 2\r\n\r\n", []Event{{Name: "a", Data: "1"}, {Name: "b", Data: "2"}}},
		{"cr only", "data: 1\r\rdata: 2\r\r", []Event{{Data: "1"}, {Data: "2"}}},
		{"multi-line data", "data: line one\ndata: line two\ndata:\n\n", []Event{{Data: "line one\nline two\n"}}},
		{"comments", ": keep-alive\n:another\ndata: x\n\n: trailing\n", []Event{{Data: "x"}}},
		{"no trailing blank line", "event: message_stop\ndata: {\"type\":\"message_stop\"}", []Event{{Name: "message_stop", Data: `{"type":"message_stop"}`}}},
		{"no trailing newline at all", "data: [DONE]", []Event{{Data: "[DONE]"}}},
		{"no space after colon", "data:{\"a\":1}\n\n", []Event{{Data: `{"a":1}`}}},
		{"only one space stripped", "data:  two spaces\n\n", []Event{{Data: " two spaces"}}},
		{"id field", "id: 42\ndata: x\n\n", []Event{{ID: "42", Data: "x"}}},
		{"field without colon", "data\n\n", []Event{{Data: ""}}},
		{"unknown fields ignored", "retry: 1000\nfoo: bar\ndata: x\n\n", []Event{{Data: "x"}}},
		{"blank lines between events", "data: 1\n\n\n\ndata: 2\n\n", []Event{{Data: "1"}, {Data: "2"}}},
		{"event name without data", "event: ping\n\n", []Event{{Name: "ping"}}},
		{"data with colon in value", "data: a:b:c\n\n", []Event{{Data: "a:b:c"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseSSE([]byte(c.in))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseSSE(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestParseSSEFixtures(t *testing.T) {
	for name, want := range map[string]int{
		"anthropic_stream.sse":        19,
		"anthropic_truncated.sse":     14,
		"openai_chat_stream.sse":      10,
		"openai_responses_stream.sse": 24,
		"gemini_stream.sse":           3,
	} {
		if got := len(ParseSSE(readFixture(t, name))); got != want {
			t.Errorf("%s: %d events, want %d", name, got, want)
		}
	}
}
