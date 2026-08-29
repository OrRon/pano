package store

import (
	"container/list"
	"sync"

	"github.com/orron/pano/internal/flow"
)

// WSLog keeps captured WebSocket messages per flow in memory. Each flow keeps
// at most perFlow messages (the newest; older ones are dropped and counted)
// and the whole log stays under a byte budget by forgetting the flows that
// started logging earliest. Drop releases a flow's messages when it leaves
// the ring.
type WSLog struct {
	mu      sync.Mutex
	perFlow int
	budget  int64
	used    int64
	logs    map[flow.ID]*wsEntry
	order   *list.List // *wsEntry, oldest first
}

type wsEntry struct {
	id      flow.ID
	msgs    []flow.WSMessage
	bytes   int64
	dropped int
	el      *list.Element
}

// NewWSLog creates a log keeping at most perFlow messages per flow (0 = 1000)
// under a total budget of budget bytes of payload (0 = 64 MiB).
func NewWSLog(perFlow int, budget int64) *WSLog {
	if perFlow <= 0 {
		perFlow = 1000
	}
	if budget <= 0 {
		budget = 64 << 20
	}
	return &WSLog{perFlow: perFlow, budget: budget, logs: make(map[flow.ID]*wsEntry), order: list.New()}
}

// Add records a message.
func (w *WSLog) Add(m *flow.WSMessage) {
	if m == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.logs[m.FlowID]
	if !ok {
		e = &wsEntry{id: m.FlowID}
		e.el = w.order.PushBack(e)
		w.logs[m.FlowID] = e
	}
	size := wsSize(m)
	e.msgs = append(e.msgs, *m)
	e.bytes += size
	w.used += size
	if len(e.msgs) > w.perFlow {
		old := wsSize(&e.msgs[0])
		e.msgs[0] = flow.WSMessage{}
		e.msgs = e.msgs[1:]
		e.bytes -= old
		w.used -= old
		e.dropped++
	}
	for w.used > w.budget && w.order.Len() > 1 {
		oldest := w.order.Front().Value.(*wsEntry)
		if oldest == e {
			break
		}
		w.remove(oldest)
	}
}

// Messages returns up to limit messages of a flow in capture order (0 = all).
func (w *WSLog) Messages(id flow.ID, limit int) []flow.WSMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.logs[id]
	if !ok {
		return nil
	}
	n := len(e.msgs)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]flow.WSMessage, n)
	copy(out, e.msgs[:n])
	return out
}

// Dropped reports how many messages of a flow were forgotten to stay within
// the per-flow cap.
func (w *WSLog) Dropped(id flow.ID) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.logs[id]; ok {
		return e.dropped
	}
	return 0
}

// Drop forgets a flow's messages.
func (w *WSLog) Drop(id flow.ID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.logs[id]; ok {
		w.remove(e)
	}
}

// Clear forgets everything.
func (w *WSLog) Clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logs = make(map[flow.ID]*wsEntry)
	w.order.Init()
	w.used = 0
}

// Stats reports the number of flows with messages, the number of messages
// and their payload bytes.
func (w *WSLog) Stats() (flows, messages int, bytes int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range w.logs {
		messages += len(e.msgs)
	}
	return len(w.logs), messages, w.used
}

func (w *WSLog) remove(e *wsEntry) {
	w.order.Remove(e.el)
	delete(w.logs, e.id)
	w.used -= e.bytes
}

func wsSize(m *flow.WSMessage) int64 { return int64(len(m.Payload)) + 64 }
