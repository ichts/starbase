package web

import (
	"log/slog"
	"sync"
	"time"
)

// maxStreamsPerClient is how many render streams one client (edge.ClientKey:
// an IPv4 address, an IPv6 /64) may hold open, so that no one takes all of
// the hub's. A browser closes the stream of a hidden tab, so a person needs
// one per window; 24 leaves room for the people behind a shared address
// (carrier-grade NAT, an office, conference Wi-Fi).
const maxStreamsPerClient = 24

// streamCap counts the open render streams of each client.
type streamCap struct {
	mu   sync.Mutex
	max  int
	open map[string]int
}

func newStreamCap(max int) *streamCap { return &streamCap{max: max, open: map[string]int{}} }

// acquire takes a stream for key, or reports false when key holds max.
func (c *streamCap) acquire(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open[key] >= c.max {
		return false
	}
	c.open[key]++
	return true
}

func (c *streamCap) release(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open[key]--; c.open[key] <= 0 {
		delete(c.open, key)
	}
}

// quietWarn logs a refusal at most once a minute, with how many it left out:
// a refused stream asks again every few seconds.
type quietWarn struct {
	mu      sync.Mutex
	last    time.Time
	skipped int
}

func (q *quietWarn) warn(log *slog.Logger, msg string, args ...any) {
	q.mu.Lock()
	now := time.Now()
	if now.Sub(q.last) < time.Minute {
		q.skipped++
		q.mu.Unlock()
		return
	}
	skipped := q.skipped
	q.last, q.skipped = now, 0
	q.mu.Unlock()
	log.Warn(msg, append(args, "since_last", skipped)...)
}
