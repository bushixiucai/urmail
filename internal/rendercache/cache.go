// Package rendercache stores short-lived, per-mailbox renderings of a
// single mail body so a second open of the same message (same viewport
// and load-images flag) does not pay for another IMAP FETCH + sanitize
// pass. Entries are process-local and never shared across mailboxes; the
// HTTP response still carries Cache-Control: no-store so browsers and
// shared caches never keep the body.
package rendercache

import (
	"sync"
	"time"
)

const (
	// DefaultTTL is how long a rendered body stays reusable.
	DefaultTTL = 5 * time.Minute
	// DefaultMaxEntries bounds memory; the oldest entry is dropped when
	// the map is full.
	DefaultMaxEntries = 256
)

// Key identifies one rendering. uid_validity is part of the key so a
// folder renumber invalidates every cached body for that folder.
type Key struct {
	MailboxID      int64
	Folder         string
	UID            uint32
	UIDValidity    uint32
	LoadImages     bool
	UAClass        string
	ViewportBucket string
}

// Entry is one cached rendering. Payload is the full JSON body of the
// GetMessage response so a cache hit skips the IMAP FETCH entirely.
type Entry struct {
	Payload []byte
	At      time.Time
}

// Cache is a process-local LRU-ish map of rendered bodies.
type Cache struct {
	mu      sync.Mutex
	entries map[Key]Entry
	ttl     time.Duration
	max     int
	now     func() time.Time
}

// New returns a Cache with the default TTL and size.
func New() *Cache {
	return &Cache{
		entries: make(map[Key]Entry),
		ttl:     DefaultTTL,
		max:     DefaultMaxEntries,
		now:     time.Now,
	}
}

// Get returns a fresh entry for key, or false when missing/expired.
func (c *Cache) Get(key Key) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return Entry{}, false
	}
	entry, ok = c.freshLocked(key, entry)
	if !ok {
		return Entry{}, false
	}
	return entry, true
}

// GetLoose returns a fresh entry matching key on every dimension EXCEPT
// uid_validity. GetMessage uses it when the request did not carry
// uid_validity: the cached payload echoes the validity the message had
// when it was rendered, so the client can still detect a renumber, and
// the TTL bounds how long such a renumber could go unnoticed server-side.
func (c *Cache) GetLoose(key Key) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, entry := range c.entries {
		if k.MailboxID != key.MailboxID || k.Folder != key.Folder ||
			k.UID != key.UID || k.LoadImages != key.LoadImages ||
			k.UAClass != key.UAClass || k.ViewportBucket != key.ViewportBucket {
			continue
		}
		entry, ok := c.freshLocked(k, entry)
		if !ok {
			return Entry{}, false
		}
		return entry, true
	}
	return Entry{}, false
}

// freshLocked touches one entry whose TTL is checked under the held lock.
// An expired entry is dropped and reported as missing.
func (c *Cache) freshLocked(key Key, entry Entry) (Entry, bool) {
	if c.now().After(entry.At.Add(c.ttl)) {
		delete(c.entries, key)
		return Entry{}, false
	}
	// Touch for a cheap LRU approximation.
	entry.At = c.now()
	c.entries[key] = entry
	return entry, true
}

// Put stores a rendering. When the map is full the oldest entry is
// dropped so memory stays bounded.
func (c *Cache) Put(key Key, payload []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		if _, ok := c.entries[key]; !ok {
			c.evictOldestLocked()
		}
	}
	c.entries[key] = Entry{Payload: append([]byte(nil), payload...), At: c.now()}
}

// InvalidateMailbox drops every entry for one mailbox (e.g. on logout).
func (c *Cache) InvalidateMailbox(mailboxID int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if key.MailboxID == mailboxID {
			delete(c.entries, key)
		}
	}
}

// InvalidateMessage drops every rendering of one message, across the
// load_images/UA/viewport dimensions — used when server-side state that
// the rendering embeds (e.g. the persisted phishing verdict) changes
// under an existing cache key.
func (c *Cache) InvalidateMessage(mailboxID int64, folder string, uid, uidValidity uint32) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if key.MailboxID == mailboxID && key.Folder == folder && key.UID == uid && key.UIDValidity == uidValidity {
			delete(c.entries, key)
		}
	}
}

func (c *Cache) evictOldestLocked() {
	var oldest Key
	var oldestAt time.Time
	first := true
	for key, entry := range c.entries {
		if first || entry.At.Before(oldestAt) {
			oldest = key
			oldestAt = entry.At
			first = false
		}
	}
	if !first {
		delete(c.entries, oldest)
	}
}
