package store

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/orron/pano/internal/api"
)

// DefaultSessionName is the name of the session every daemon starts in.
const DefaultSessionName = "default"

// ErrNotFound is returned by lookups for ids that are not in the store.
var ErrNotFound = errors.New("store: not found")

// Sessions is the in-memory session registry: named groups of flows, exactly
// one of which is current. Flows record the id of the session they were
// captured under; List computes per-session counts with the callback it is
// given. The registry starts with DefaultSessionName current and is gone
// with the daemon.
type Sessions struct {
	mu   sync.Mutex
	list []api.Session // oldest first
	cur  string
}

// NewSessions returns a registry whose current session is DefaultSessionName.
func NewSessions() *Sessions {
	s := &Sessions{}
	s.Start(DefaultSessionName)
	return s
}

// CurrentID returns the id of the current session.
func (s *Sessions) CurrentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Start ends the current session and makes a new one with a short random id
// current. An empty name becomes DefaultSessionName.
func (s *Sessions) Start(name string) api.Session {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultSessionName
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].Current {
			s.list[i].Current = false
			s.list[i].EndedAt = now
		}
	}
	id := newSessionID()
	for s.has(id) {
		id = newSessionID()
	}
	ss := api.Session{ID: id, Name: name, StartedAt: now, Current: true}
	s.list = append(s.list, ss)
	s.cur = id
	return ss
}

// Delete forgets a session. Its flows are the caller's to remove (see
// Mem.Delete). Deleting the current session is refused with ErrCurrent.
func (s *Sessions) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.cur {
		return ErrCurrent
	}
	for i := range s.list {
		if s.list[i].ID == id {
			s.list = append(s.list[:i], s.list[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// ErrCurrent is returned by Delete for the current session.
var ErrCurrent = errors.New("store: cannot delete the current session")

// List returns every session newest first; flows(id) supplies the count.
func (s *Sessions) List(flows func(id string) int) []api.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.Session, 0, len(s.list))
	for i := len(s.list) - 1; i >= 0; i-- {
		ss := s.list[i]
		if flows != nil {
			ss.Flows = flows(ss.ID)
		}
		out = append(out, ss)
	}
	return out
}

func (s *Sessions) has(id string) bool {
	for _, ss := range s.list {
		if ss.ID == id {
			return true
		}
	}
	return false
}

// newSessionID returns 8 hex characters of randomness.
func newSessionID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to the
		// clock so we still return something unique enough.
		binary.BigEndian.PutUint32(b[:], uint32(time.Now().UnixNano()&0xffffffff)) //nolint:gosec // masked
	}
	return hex.EncodeToString(b[:])
}
