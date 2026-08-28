package explain

import (
	"bytes"
	"strings"
)

// Event is one server-sent event as split out of a text/event-stream body.
type Event struct {
	// Name is the value of the "event:" field, or "" when the event has none
	// (OpenAI streams, for example, only send "data:" lines).
	Name string
	// Data is the payload: every "data:" line of the event joined with "\n".
	Data string
	// ID is the value of the "id:" field, if any.
	ID string
}

// ParseSSE splits an event-stream body into events. It follows the WHATWG
// event-stream grammar loosely: lines end in CRLF, LF or CR; a line starting
// with ':' is a comment; multiple "data:" lines are joined with a newline; the
// single optional space after the field colon is dropped; a blank line
// dispatches the pending event. A pending event at end of input is dispatched
// too, so a stream that was cut off without its trailing blank line still
// yields its last event. Events without any field are dropped.
func ParseSSE(b []byte) []Event {
	var (
		events []Event
		cur    Event
		data   []string
		seen   bool // any field seen for cur
	)
	flush := func() {
		if seen {
			cur.Data = strings.Join(data, "\n")
			events = append(events, cur)
		}
		cur = Event{}
		data = data[:0]
		seen = false
	}
	for len(b) > 0 {
		var line []byte
		if i := bytes.IndexAny(b, "\r\n"); i < 0 {
			line, b = b, nil
		} else {
			line = b[:i]
			if b[i] == '\r' && i+1 < len(b) && b[i+1] == '\n' {
				b = b[i+2:]
			} else {
				b = b[i+1:]
			}
		}
		if len(line) == 0 {
			flush()
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value := string(line), ""
		if j := bytes.IndexByte(line, ':'); j >= 0 {
			field = string(line[:j])
			value = string(line[j+1:])
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			cur.Name = value
			seen = true
		case "data":
			data = append(data, value)
			seen = true
		case "id":
			cur.ID = value
			seen = true
		}
	}
	flush()
	return events
}

// looksLikeSSE reports whether a body starts like an event stream. It is used
// when the response carried no usable Content-Type.
func looksLikeSSE(b []byte) bool {
	t := bytes.TrimLeft(b, " \t\r\n\uFEFF")
	return bytes.HasPrefix(t, []byte("event:")) || bytes.HasPrefix(t, []byte("data:")) || bytes.HasPrefix(t, []byte(":"))
}
