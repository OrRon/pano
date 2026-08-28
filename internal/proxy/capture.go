package proxy

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
)

var bufPool = sync.Pool{New: func() any { b := make([]byte, 32<<10); return &b }}

// budget bounds total in-flight capture bytes across all flows.
type budget struct {
	limit int64
	used  atomic.Int64
}

func (b *budget) take(n int64) bool {
	if b == nil || b.limit <= 0 {
		return true
	}
	if b.used.Add(n) > b.limit {
		b.used.Add(-n)
		return false
	}
	return true
}

func (b *budget) release(n int64) {
	if b != nil && b.limit > 0 {
		b.used.Add(-n)
	}
}

// capture accumulates up to limit bytes of a body while counting all of them.
type capture struct {
	buf       bytes.Buffer
	limit     int64
	size      int64
	truncated bool
	budget    *budget
	reserved  int64
	off       bool
}

func newCapture(limit int64, b *budget) *capture {
	return &capture{limit: limit, budget: b}
}

func (c *capture) Write(p []byte) (int, error) {
	n := len(p)
	c.size += int64(n)
	if c.off || c.truncated {
		return n, nil
	}
	room := c.limit - int64(c.buf.Len())
	if room <= 0 {
		c.truncated = true
		return n, nil
	}
	take := p
	if int64(len(take)) > room {
		take = take[:room]
		c.truncated = true
	}
	if !c.budget.take(int64(len(take))) {
		c.truncated = true
		return n, nil
	}
	c.reserved += int64(len(take))
	c.buf.Write(take)
	return n, nil
}

// bytesAndRelease returns captured bytes and releases the budget reservation.
func (c *capture) bytesAndRelease() []byte {
	b := c.buf.Bytes()
	out := make([]byte, len(b))
	copy(out, b)
	c.budget.release(c.reserved)
	c.reserved = 0
	return out
}

// teeReader copies what is read into a capture.
type teeReader struct {
	r   io.ReadCloser
	cap *capture
}

func (t *teeReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		_, _ = t.cap.Write(p[:n])
	}
	return n, err
}

func (t *teeReader) Close() error { return t.r.Close() }
