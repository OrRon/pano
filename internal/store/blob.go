package store

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Blobs is a content-addressed byte store.
type Blobs interface {
	Put(b []byte) string
	Get(hash string) ([]byte, bool)
}

// Hash returns the sha256 hex of b.
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// MemBlobs is an LRU byte-budgeted in-memory blob store. Bodies of the
// least recently used flows are dropped once the budget is exceeded; a flow
// whose body was dropped still lists, it just has no body to show.
type MemBlobs struct {
	mu     sync.Mutex
	budget int64
	used   int64
	lru    *list.List
	index  map[string]*list.Element
}

type blobEntry struct {
	hash string
	b    []byte
}

// NewMemBlobs creates a store with the given byte budget (0 = 256 MiB).
func NewMemBlobs(budget int64) *MemBlobs {
	if budget <= 0 {
		budget = 256 << 20
	}
	return &MemBlobs{budget: budget, lru: list.New(), index: make(map[string]*list.Element)}
}

// Put stores b and returns its hash.
func (m *MemBlobs) Put(b []byte) string {
	h := Hash(b)
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.index[h]; ok {
		m.lru.MoveToFront(el)
		return h
	}
	m.index[h] = m.lru.PushFront(&blobEntry{hash: h, b: b})
	m.used += int64(len(b))
	for m.used > m.budget && m.lru.Len() > 1 {
		last := m.lru.Back()
		e := last.Value.(*blobEntry)
		m.used -= int64(len(e.b))
		delete(m.index, e.hash)
		m.lru.Remove(last)
	}
	return h
}

// Get fetches by hash.
func (m *MemBlobs) Get(hash string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.index[hash]
	if !ok {
		return nil, false
	}
	m.lru.MoveToFront(el)
	return el.Value.(*blobEntry).b, true
}

// Clear drops every blob.
func (m *MemBlobs) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lru.Init()
	m.index = make(map[string]*list.Element)
	m.used = 0
}

// Len is the number of blobs held.
func (m *MemBlobs) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lru.Len()
}

// Bytes is the total size of the blobs held.
func (m *MemBlobs) Bytes() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used
}
