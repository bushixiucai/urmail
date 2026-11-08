package ai

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

// Operation result cache (TECH §4): the same mail summarized, translated
// or classified twice must not pay twice — the second caller is served
// from an in-process LRU keyed by the request digest, the prompt template
// version and the operation, with a one-hour TTL. Only validated,
// non-degraded payloads are cached; degraded fallback answers stay
// uncached so a bad provider round can never pin itself for an hour.
// The cache holds no tenant dimension beyond the digest: callers fold
// tenant/mailbox/input into the digest they pass.

const (
	// ResultCacheCapacity bounds the LRU at 256 operations.
	ResultCacheCapacity = 256
	// ResultCacheTTL bounds how long one answer serves repeat calls.
	ResultCacheTTL = time.Hour
)

// CachedResult carries the stored payload next to the meta that produced
// it, so a hit replays the exact envelope the first caller saw.
type CachedResult struct {
	Payload json.RawMessage
	Meta    ResultMeta
}

type resultCacheEntry struct {
	result  CachedResult
	expires time.Time
}

// ResultCache is a process-local LRU with TTL. Safe for concurrent use.
type ResultCache struct {
	mu      sync.Mutex
	entries map[string]resultCacheEntry
	order   []string // FIFO of keys, oldest first, for eviction
	cap     int
	ttl     time.Duration
	now     func() time.Time
}

// NewResultCache returns a cache with the production capacity and TTL.
func NewResultCache() *ResultCache {
	return &ResultCache{
		entries: make(map[string]resultCacheEntry, ResultCacheCapacity),
		cap:     ResultCacheCapacity,
		ttl:     ResultCacheTTL,
		now:     time.Now,
	}
}

// resultCacheKey names one cached operation result.
func resultCacheKey(operation string, version int, digest string) string {
	return operation + ":v" + strconv.Itoa(version) + ":" + digest
}

// Get returns the cached result for one operation/digest pair and moves
// the key to the fresh end of the eviction order.
func (c *ResultCache) Get(operation string, version int, digest string) (CachedResult, bool) {
	if c == nil {
		return CachedResult{}, false
	}
	key := resultCacheKey(operation, version, digest)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return CachedResult{}, false
	}
	if !c.now().Before(entry.expires) {
		c.removeLocked(key)
		return CachedResult{}, false
	}
	return entry.result, true
}

// Put stores a validated, non-degraded result; degraded metas are
// ignored so fallback answers can never be served from cache.
func (c *ResultCache) Put(operation string, version int, digest string, payload json.RawMessage, meta ResultMeta) {
	if c == nil || meta.Degraded || len(payload) == 0 {
		return
	}
	key := resultCacheKey(operation, version, digest)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		c.order = append(c.order, key)
	}
	c.entries[key] = resultCacheEntry{
		result:  CachedResult{Payload: payload, Meta: meta},
		expires: c.now().Add(c.ttl),
	}
	for len(c.entries) > c.cap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

// removeLocked drops one key from the map and the eviction order.
func (c *ResultCache) removeLocked(key string) {
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}
