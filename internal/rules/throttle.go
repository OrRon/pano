package rules

import (
	"context"
	"io"
	"sync"
	"time"
)

// throttleChunk is the largest read a throttled body serves at once and the
// burst a fresh limiter allows.
const throttleChunk = 32 << 10

// bucket is a token bucket measured in bytes. It is shared by every body a
// throttle action wraps, so the limit is per rule rather than per response.
type bucket struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(bytesPerSec, burst float64) *bucket {
	return &bucket{rate: bytesPerSec, burst: burst, tokens: burst, last: time.Now()}
}

// reserve takes n tokens and returns how long the caller must wait for them.
func (b *bucket) reserve(n int) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+b.rate*now.Sub(b.last).Seconds())
	b.last = now
	b.tokens -= float64(n)
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}

// throttledReader paces reads through a bucket.
type throttledReader struct {
	ctx context.Context
	rc  io.ReadCloser
	b   *bucket
}

func newThrottledReader(ctx context.Context, rc io.ReadCloser, b *bucket) io.ReadCloser {
	return &throttledReader{ctx: ctx, rc: rc, b: b}
}

func (t *throttledReader) Read(p []byte) (int, error) {
	if len(p) > throttleChunk {
		p = p[:throttleChunk]
	}
	n, err := t.rc.Read(p)
	if n > 0 {
		if d := t.b.reserve(n); d > 0 {
			timer := time.NewTimer(d)
			select {
			case <-timer.C:
			case <-t.ctx.Done():
				timer.Stop()
				return n, t.ctx.Err()
			}
		}
	}
	return n, err
}

func (t *throttledReader) Close() error { return t.rc.Close() }
