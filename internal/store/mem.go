package store

import (
	"sync"

	"github.com/orron/pano/internal/flow"
)

// Mem is a fixed-size ring of flow snapshots, newest last. Flows are
// immutable snapshots; Upsert replaces by ID. When the ring is full the
// oldest flow is evicted to make room.
type Mem struct {
	mu    sync.RWMutex
	ring  []*flow.Flow
	head  int // index of the next write
	count int
	byID  map[flow.ID]int // id -> ring index
	total int64

	// OnEvict, if set, is called (without the lock held) for every flow that
	// leaves the ring — capacity eviction, Delete and Clear — so dependent
	// per-flow state (WebSocket messages) can be released with it.
	OnEvict func(*flow.Flow)
}

// NewMem creates a ring holding at most size flows.
func NewMem(size int) *Mem {
	if size <= 0 {
		size = 10000
	}
	return &Mem{ring: make([]*flow.Flow, size), byID: make(map[flow.ID]int, size)}
}

// Upsert stores a snapshot.
func (m *Mem) Upsert(f *flow.Flow) {
	m.mu.Lock()
	if idx, ok := m.byID[f.ID]; ok {
		m.ring[idx] = f
		m.mu.Unlock()
		return
	}
	var evicted *flow.Flow
	if m.count == len(m.ring) {
		evicted = m.ring[m.head]
		if evicted != nil {
			delete(m.byID, evicted.ID)
		}
	} else {
		m.count++
	}
	m.ring[m.head] = f
	m.byID[f.ID] = m.head
	m.head = (m.head + 1) % len(m.ring)
	m.total++
	fn := m.OnEvict
	m.mu.Unlock()
	if evicted != nil && fn != nil {
		fn(evicted)
	}
}

// Get returns a flow by ID.
func (m *Mem) Get(id flow.ID) (*flow.Flow, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	idx, ok := m.byID[id]
	if !ok {
		return nil, false
	}
	return m.ring[idx], true
}

// Len is the number of flows held.
func (m *Mem) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.count
}

// Cap is the ring size.
func (m *Mem) Cap() int { return len(m.ring) }

// Total is the number of distinct flows ever inserted.
func (m *Mem) Total() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.total
}

// Clear empties the ring.
func (m *Mem) Clear() {
	m.Delete(func(*flow.Flow) bool { return true })
}

// Delete removes every flow for which match returns true and reports how many
// were removed. Order and IDs of the remaining flows are preserved.
func (m *Mem) Delete(match func(*flow.Flow) bool) int {
	m.mu.Lock()
	n := len(m.ring)
	var keep, gone []*flow.Flow
	for i := m.count; i >= 1; i-- { // oldest first
		f := m.ring[((m.head-i)%n+n)%n]
		if f == nil {
			continue
		}
		if match(f) {
			gone = append(gone, f)
		} else {
			keep = append(keep, f)
		}
	}
	if len(gone) > 0 {
		for i := range m.ring {
			m.ring[i] = nil
		}
		m.byID = make(map[flow.ID]int, n)
		m.head, m.count = 0, 0
		for _, f := range keep {
			m.ring[m.head] = f
			m.byID[f.ID] = m.head
			m.head = (m.head + 1) % n
			m.count++
		}
	}
	fn := m.OnEvict
	m.mu.Unlock()
	if fn != nil {
		for _, f := range gone {
			fn(f)
		}
	}
	return len(gone)
}

// Count returns how many flows satisfy match.
func (m *Mem) Count(match func(*flow.Flow) bool) int {
	n := 0
	m.Each(func(f *flow.Flow) bool {
		if match(f) {
			n++
		}
		return true
	})
	return n
}

// Each visits flows newest first until fn returns false.
func (m *Mem) Each(fn func(*flow.Flow) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := len(m.ring)
	for i := 1; i <= m.count; i++ {
		idx := ((m.head-i)%n + n) % n
		f := m.ring[idx]
		if f == nil {
			continue
		}
		if !fn(f) {
			return
		}
	}
}

// Newest returns the highest ID present.
func (m *Mem) Newest() flow.ID {
	var id flow.ID
	m.Each(func(f *flow.Flow) bool { id = f.ID; return false })
	return id
}
