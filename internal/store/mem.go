package store

import (
	"sync"

	"github.com/orron/pano/internal/flow"
)

// Mem is a fixed-size ring of flow snapshots, newest last. Flows are
// immutable snapshots; Upsert replaces by ID.
type Mem struct {
	mu    sync.RWMutex
	ring  []*flow.Flow
	head  int // index of the next write
	count int
	byID  map[flow.ID]int // id -> ring index
	total int64
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
	defer m.mu.Unlock()
	if idx, ok := m.byID[f.ID]; ok {
		m.ring[idx] = f
		return
	}
	if m.count == len(m.ring) {
		old := m.ring[m.head]
		if old != nil {
			delete(m.byID, old.ID)
		}
	} else {
		m.count++
	}
	m.ring[m.head] = f
	m.byID[f.ID] = m.head
	m.head = (m.head + 1) % len(m.ring)
	m.total++
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

// Total is the number of distinct flows ever inserted.
func (m *Mem) Total() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.total
}

// Clear empties the ring.
func (m *Mem) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.ring {
		m.ring[i] = nil
	}
	m.byID = make(map[flow.ID]int, len(m.ring))
	m.head, m.count = 0, 0
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
