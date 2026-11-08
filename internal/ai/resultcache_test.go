package ai

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func cachePayload(t *testing.T, s string) json.RawMessage {
	t.Helper()
	return json.RawMessage(s)
}

// TestResultCacheHitMiss pins the happy path: put then get replays the
// payload and the producing meta, unknown keys miss.
func TestResultCacheHitMiss(t *testing.T) {
	c := NewResultCache()
	c.Put("summarize", 2, "digest-a", cachePayload(t, `{"bullets":["x"]}`), ResultMeta{TemplateVersion: 2})
	got, ok := c.Get("summarize", 2, "digest-a")
	if !ok || string(got.Payload) != `{"bullets":["x"]}` || got.Meta.TemplateVersion != 2 {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
	if _, ok := c.Get("summarize", 2, "digest-b"); ok {
		t.Fatalf("unknown digest hit")
	}
	if _, ok := c.Get("polish", 2, "digest-a"); ok {
		t.Fatalf("unknown operation hit")
	}
	if _, ok := c.Get("summarize", 1, "digest-a"); ok {
		t.Fatalf("unknown version hit")
	}
}

// TestResultCacheExpiry pins the TTL: an expired entry misses and is
// dropped from the eviction order.
func TestResultCacheExpiry(t *testing.T) {
	c := NewResultCache()
	base := time.Unix(1700000000, 0)
	c.now = func() time.Time { return base }
	c.Put("summarize", 2, "d", cachePayload(t, `{}`), ResultMeta{TemplateVersion: 2})
	c.now = func() time.Time { return base.Add(ResultCacheTTL + time.Second) }
	if _, ok := c.Get("summarize", 2, "d"); ok {
		t.Fatalf("expired entry served")
	}
	// Refilling after expiry must not resurrect the dead order entry;
	// the fresh entry expires one TTL after the refill, not after the
	// original put.
	c.Put("summarize", 2, "d", cachePayload(t, `{"b":1}`), ResultMeta{TemplateVersion: 2})
	c.now = func() time.Time { return base.Add(2*ResultCacheTTL + 2*time.Second) }
	if _, ok := c.Get("summarize", 2, "d"); ok {
		t.Fatalf("re-filled entry served past TTL")
	}
}

// TestResultCacheEviction pins the LRU bound: capacity+1 puts evict the
// oldest key.
func TestResultCacheEviction(t *testing.T) {
	c := NewResultCache()
	c.Put("op", 1, "k0", cachePayload(t, `{"n":0}`), ResultMeta{})
	for i := 1; i <= ResultCacheCapacity; i++ {
		c.Put("op", 1, "k"+strconv.Itoa(i), cachePayload(t, `{"n":1}`), ResultMeta{})
	}
	if _, ok := c.Get("op", 1, "k0"); ok {
		t.Fatalf("oldest entry survived eviction")
	}
	if _, ok := c.Get("op", 1, "k"+strconv.Itoa(ResultCacheCapacity)); !ok {
		t.Fatalf("newest entry evicted")
	}
	if len(c.entries) != ResultCacheCapacity {
		t.Fatalf("cache size %d, want %d", len(c.entries), ResultCacheCapacity)
	}
}

// TestResultCacheNeverCachesDegraded pins the degraded guard: fallback
// answers are refused at Put and never served.
func TestResultCacheNeverCachesDegraded(t *testing.T) {
	c := NewResultCache()
	c.Put("summarize", 2, "d", cachePayload(t, `{"text":"x"}`), ResultMeta{TemplateVersion: 1, Degraded: true, DegradeReason: DegradeSchemaRetryExhausted})
	c.Put("summarize", 2, "empty", nil, ResultMeta{TemplateVersion: 2})
	if _, ok := c.Get("summarize", 2, "d"); ok {
		t.Fatalf("degraded result cached")
	}
	if _, ok := c.Get("summarize", 2, "empty"); ok {
		t.Fatalf("empty payload cached")
	}
}

// TestResultCacheNilReceiver pins the disabled-cache contract: a nil
// cache misses and swallows puts, so wiring can stay unconditional.
func TestResultCacheNilReceiver(t *testing.T) {
	var c *ResultCache
	if _, ok := c.Get("op", 1, "d"); ok {
		t.Fatalf("nil cache hit")
	}
	c.Put("op", 1, "d", cachePayload(t, `{}`), ResultMeta{})
}
