package handlers

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
)

func TestLoginLimiterBlocksAfterMaxFailures(t *testing.T) {
	l := newLoginLimiter()
	key := "1.2.3.4|user@example.com"

	for i := 0; i < loginMaxFailures; i++ {
		if !l.allow(key) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		l.fail(key)
	}
	if l.allow(key) {
		t.Fatalf("attempt %d must be blocked", loginMaxFailures+1)
	}
	// A different identity is unaffected.
	if !l.allow("5.6.7.8|other@example.com") {
		t.Fatalf("unrelated identity must not be blocked")
	}
}

func TestLoginLimiterResetAfterSuccess(t *testing.T) {
	l := newLoginLimiter()
	key := "1.2.3.4|user@example.com"
	for i := 0; i < loginMaxFailures; i++ {
		l.fail(key)
	}
	l.reset(key)
	if !l.allow(key) {
		t.Fatalf("after reset the identity must be allowed again")
	}
}

func TestHashSessionTokenIsStableAndIrreversible(t *testing.T) {
	raw := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	h1 := middleware.HashSessionToken(raw)
	h2 := middleware.HashSessionToken(raw)
	if h1 != h2 {
		t.Fatalf("hash must be deterministic")
	}
	if h1 == raw {
		t.Fatalf("hash must not equal the raw token")
	}
	if len(h1) != 64 {
		t.Fatalf("sha256 hex must be 64 chars, got %d", len(h1))
	}
	if middleware.HashSessionToken("other") == h1 {
		t.Fatalf("different tokens must hash differently")
	}
}

func TestDummyPasswordHashIsUsable(t *testing.T) {
	if dummyPasswordHash == "" {
		t.Fatalf("dummy hash must be generated at init")
	}
	if !panelpassword.Verify("urmail-dummy-password-for-timing", dummyPasswordHash) {
		t.Fatalf("dummy hash must verify against its own password")
	}
}
