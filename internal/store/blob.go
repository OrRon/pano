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

// MemBlobs is an LRU byte-budgeted blob cache. A Persister, if set, is
// consulted on miss and told about every Put.
type MemBlobs struct {
	mu        sync.Mutex
	budget    int64
	used      int64
	lru       *list.List
	index     map[string]*list.Element
	Persister BlobPersister
}

// BlobPersister is a durable backing store for blobs.
type BlobPersister interface {
	PutBlob(hash string, b []byte)
	GetBlob(hash string) ([]byte, bool)
}

type blobEntry struct {
	hash string
	b    []byte
}

// NewMemBlobs creates a cache with the given byte budget.
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
	if el, ok := m.index[h]; ok {
		m.lru.MoveToFront(el)
		m.mu.Unlock()
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
	p := m.Persister
	m.mu.Unlock()
	if p != nil {
		p.PutBlob(h, b)
	}
	return h
}

// Get fetches by hash.
func (m *MemBlobs) Get(hash string) ([]byte, bool) {
	m.mu.Lock()
	if el, ok := m.index[hash]; ok {
		m.lru.MoveToFront(el)
		b := el.Value.(*blobEntry).b
		m.mu.Unlock()
		return b, true
	}
	p := m.Persister
	m.mu.Unlock()
	if p == nil {
		return nil, false
	}
	b, ok := p.GetBlob(hash)
	if !ok {
		return nil, false
	}
	m.mu.Lock()
	if _, exists := m.index[hash]; !exists {
		m.index[hash] = m.lru.PushFront(&blobEntry{hash: hash, b: b})
		m.used += int64(len(b))
	}
	m.mu.Unlock()
	return b, true
}
