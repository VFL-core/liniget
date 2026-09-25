package main

import (
	"io"
	"sync"
	"time"
)

// RateLimiter is a simple shared token-bucket limiter. It's safe to use
// from multiple goroutines at once, which lets several download threads
// share a single overall speed cap.
type RateLimiter struct {
	mu           sync.Mutex
	bytesPerSec  int64 // 0 = unlimited
	windowStart  time.Time
	usedInWindow int64
}

func NewRateLimiter(bytesPerSec int64) *RateLimiter {
	return &RateLimiter{
		bytesPerSec: bytesPerSec,
		windowStart: time.Now(),
	}
}

// Wait blocks until it is "allowed" to send n more bytes, given the
// configured cap. It divides time into rolling 1-second windows and
// sleeps out the remainder of a window once the cap is used up.
func (r *RateLimiter) Wait(n int) {
	if r == nil || r.bytesPerSec <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if elapsed := now.Sub(r.windowStart); elapsed >= time.Second {
		r.windowStart = now
		r.usedInWindow = 0
	}

	r.usedInWindow += int64(n)
	if r.usedInWindow > r.bytesPerSec {
		// We've exceeded the cap for this window; sleep until the
		// window resets, then start a fresh window.
		sleepFor := time.Second - time.Since(r.windowStart)
		if sleepFor > 0 {
			// Unlock while sleeping so other threads' Wait() calls
			// (which is unlocked already since we hold the lock)... instead
			// we sleep while holding the lock to keep accounting simple;
			// this trades a bit of concurrency for correctness.
			time.Sleep(sleepFor)
		}
		r.windowStart = time.Now()
		r.usedInWindow = 0
	}
}

// throttledReader wraps any io.Reader so every Read() is throttled
// through a shared RateLimiter. Several throttledReaders can share one
// RateLimiter so that multiple download threads together respect a
// single overall speed cap.
type throttledReader struct {
	src     io.Reader
	limiter *RateLimiter
}

func newThrottledReader(src io.Reader, limiter *RateLimiter) *throttledReader {
	return &throttledReader{src: src, limiter: limiter}
}

func (t *throttledReader) Read(p []byte) (int, error) {
	// Cap each individual read to a reasonably small chunk so the
	// limiter can react promptly instead of bursting a huge buffer
	// through in one shot.
	if len(p) > 32*1024 {
		p = p[:32*1024]
	}
	n, err := t.src.Read(p)
	if n > 0 {
		t.limiter.Wait(n)
	}
	return n, err
}
