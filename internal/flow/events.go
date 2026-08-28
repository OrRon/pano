package flow

import "time"

// EventType enumerates bus events.
type EventType string

// Event types.
const (
	EvStarted EventType = "started" // Flow set (request headers known)
	EvHeaders EventType = "headers" // Flow set (response headers known)
	EvDone    EventType = "done"    // Flow set (final)
	EvHeld    EventType = "held"    // Flow set; Phase set
	EvWS      EventType = "ws"      // WS set
	EvDropped EventType = "dropped" // Dropped set
)

// WSMessage is one captured WebSocket frame/message.
type WSMessage struct {
	FlowID  ID        `json:"flow_id"`
	Seq     int       `json:"seq"`
	TS      time.Time `json:"ts"`
	Dir     string    `json:"dir"` // "c2s" | "s2c"
	Opcode  int       `json:"opcode"`
	Len     int64     `json:"len"`
	Payload []byte    `json:"payload,omitempty"`
	Masked  bool      `json:"masked,omitempty"`
}

// Event is what the engine publishes on the bus.
type Event struct {
	Type    EventType  `json:"type"`
	Seq     uint64     `json:"seq"`
	TS      time.Time  `json:"ts"`
	Flow    *Flow      `json:"flow,omitempty"`
	Phase   string     `json:"phase,omitempty"`
	WS      *WSMessage `json:"ws,omitempty"`
	Dropped int        `json:"dropped,omitempty"`
}
