package policy

import (
	"sync"
	"time"
)

// seenCache is a fixed-capacity, TTL-bounded set of keys implemented with the
// standard library only. It answers "was this key recorded within the TTL?"
// and evicts oldest-inserted entries once capacity is reached.
type seenCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	entries map[string]time.Time
	order   []string // insertion order, for FIFO eviction
}

func newSeenCache(max int, ttl time.Duration) *seenCache {
	if max <= 0 {
		max = 1
	}
	return &seenCache{
		max:     max,
		ttl:     ttl,
		entries: make(map[string]time.Time, max),
		order:   make([]string, 0, max),
	}
}

// Seen reports whether key was recorded within the TTL. If not, it records
// key now and returns false.
func (c *seenCache) Seen(key string) bool {
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if ts, ok := c.entries[key]; ok {
		if now.Sub(ts) < c.ttl {
			return true
		}
		// Expired: refresh the timestamp below.
		c.record(key, now)
		return false
	}

	c.record(key, now)
	return false
}

// Len returns the number of tracked keys.
func (c *seenCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// SeenSet is the minimal exported view of the dedup cache for tests.
type SeenSet interface {
	Seen(key string) bool
}

// NewSeenCacheForTest exposes the dedup cache for package-external tests.
func NewSeenCacheForTest(max int, ttl time.Duration) SeenSet {
	return newSeenCache(max, ttl)
}

// Forget removes a key so the next recipient of the same message is
// re-evaluated after a rejection or deferral.
func (c *seenCache) Forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *seenCache) record(key string, now time.Time) {
	if _, ok := c.entries[key]; !ok {
		c.order = append(c.order, key)
	}
	c.entries[key] = now

	for len(c.order) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}
