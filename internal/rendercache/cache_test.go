package rendercache

import (
	"testing"
	"time"
)

func TestCacheRoundTripAndExpiry(t *testing.T) {
	c := New()
	now := time.Now()
	c.now = func() time.Time { return now }
	key := Key{MailboxID: 9, Folder: "INBOX", UID: 7, UIDValidity: 1, LoadImages: false, UAClass: "desktop", ViewportBucket: "wide"}
	c.Put(key, []byte(`{"html":"<p>hi</p>"}`))
	entry, ok := c.Get(key)
	if !ok || string(entry.Payload) != `{"html":"<p>hi</p>"}` {
		t.Fatalf("get %+v %v", entry, ok)
	}
	missKey := key
	missKey.UIDValidity = 2
	if _, ok := c.Get(missKey); ok {
		t.Fatal("uid_validity change should miss")
	}
	c.now = func() time.Time { return now.Add(DefaultTTL + time.Second) }
	if _, ok := c.Get(key); ok {
		t.Fatal("expired entry returned")
	}
}

func TestCacheMailboxIsolationAndInvalidate(t *testing.T) {
	c := New()
	a := Key{MailboxID: 1, Folder: "INBOX", UID: 1, UIDValidity: 1}
	b := Key{MailboxID: 2, Folder: "INBOX", UID: 1, UIDValidity: 1}
	c.Put(a, []byte("a"))
	c.Put(b, []byte("b"))
	c.InvalidateMailbox(1)
	if _, ok := c.Get(a); ok {
		t.Fatal("mailbox 1 still cached")
	}
	if entry, ok := c.Get(b); !ok || string(entry.Payload) != "b" {
		t.Fatal("mailbox 2 lost")
	}
}

// TestCacheLooseMatchesIgnoringUIDValidity covers the DEF-3 lookup: a
// request without uid_validity matches the entry rendered under any
// validity, while every other dimension still has to agree exactly, an
// exact Get with a zero validity never matches, and expiry applies.
func TestCacheLooseMatchesIgnoringUIDValidity(t *testing.T) {
	c := New()
	now := time.Now()
	c.now = func() time.Time { return now }
	key := Key{MailboxID: 9, Folder: "INBOX", UID: 7, UIDValidity: 42, LoadImages: false, UAClass: "desktop", ViewportBucket: "wide"}
	c.Put(key, []byte("rendered"))

	loose := key
	loose.UIDValidity = 0
	entry, ok := c.GetLoose(loose)
	if !ok || string(entry.Payload) != "rendered" {
		t.Fatalf("loose hit = %v %q", ok, entry.Payload)
	}

	for _, tc := range []struct {
		name string
		mut  func(Key) Key
	}{
		{"folder", func(k Key) Key { k.Folder = "Archive"; return k }},
		{"uid", func(k Key) Key { k.UID = 8; return k }},
		{"mailbox", func(k Key) Key { k.MailboxID = 10; return k }},
		{"load_images", func(k Key) Key { k.LoadImages = true; return k }},
		{"ua_class", func(k Key) Key { k.UAClass = "mobile"; return k }},
		{"viewport", func(k Key) Key { k.ViewportBucket = "narrow"; return k }},
	} {
		if _, ok := c.GetLoose(tc.mut(loose)); ok {
			t.Fatalf("loose match on changed %s must miss", tc.name)
		}
	}
	if _, ok := c.Get(loose); ok {
		t.Fatal("exact Get with zero validity must miss")
	}
	c.now = func() time.Time { return now.Add(DefaultTTL + time.Second) }
	if _, ok := c.GetLoose(loose); ok {
		t.Fatal("expired entry returned loosely")
	}
}

func TestCacheEvictsWhenFull(t *testing.T) {
	c := New()
	c.max = 2
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Put(Key{MailboxID: 1, UID: 1, UIDValidity: 1}, []byte("1"))
	now = now.Add(time.Second)
	c.Put(Key{MailboxID: 1, UID: 2, UIDValidity: 1}, []byte("2"))
	now = now.Add(time.Second)
	c.Put(Key{MailboxID: 1, UID: 3, UIDValidity: 1}, []byte("3"))
	if len(c.entries) > 2 {
		t.Fatalf("size %d", len(c.entries))
	}
	if _, ok := c.Get(Key{MailboxID: 1, UID: 1, UIDValidity: 1}); ok {
		t.Fatal("oldest not evicted")
	}
}
