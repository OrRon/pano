package bus

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/orron/pano/internal/flow"
)

// Bus publishes flow events to subscribers.
type Bus struct {
	mu   sync.RWMutex
	subs map[*Subscriber]struct{}
	seq  atomic.Uint64
}

// Subscriber receives events on C. If its queue fills, older events are
// dropped and a single EvDropped event is coalesced in front.
type Subscriber struct {
	C       chan flow.Event
	bus     *Bus
	mu      sync.Mutex
	dropped int
	closed  bool
	filter  func(flow.Event) bool
}

// New creates a bus.
func New() *Bus { return &Bus{subs: make(map[*Subscriber]struct{})} }

// Subscribe registers a subscriber with the given queue size. filter may be nil.
func (b *Bus) Subscribe(size int, filter func(flow.Event) bool) *Subscriber {
	if size <= 0 {
		size = 256
	}
	s := &Subscriber{C: make(chan flow.Event, size), bus: b, filter: filter}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Close unsubscribes. C is closed; pending events are discarded.
func (s *Subscriber) Close() {
	s.bus.mu.Lock()
	delete(s.bus.subs, s)
	s.bus.mu.Unlock()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.C)
	}
	s.mu.Unlock()
}

// Publish stamps and fans out an event. Never blocks.
func (b *Bus) Publish(ev flow.Event) {
	ev.Seq = b.seq.Add(1)
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		s.push(ev)
	}
}

// Seq returns the last published sequence number.
func (b *Bus) Seq() uint64 { return b.seq.Load() }

func (s *Subscriber) push(ev flow.Event) {
	if s.filter != nil && !s.filter(ev) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.dropped > 0 {
		select {
		case s.C <- flow.Event{Type: flow.EvDropped, Dropped: s.dropped, TS: ev.TS, Seq: ev.Seq}:
			s.dropped = 0
		default:
			s.dropped++
			return
		}
	}
	select {
	case s.C <- ev:
	default:
		// Queue full: drop the oldest to make room, remember the loss.
		select {
		case <-s.C:
		default:
		}
		s.dropped++
		select {
		case s.C <- ev:
		default:
		}
	}
}
