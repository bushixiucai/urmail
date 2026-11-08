package handlers

// v0.6.12 PR-7 conditional requests on the message detail response:
// every 200 carries a strong ETag (sha256 of the payload bytes) and
// X-Render-Cache: hit|miss for observability; a matching If-None-Match
// gets a header-only 304. All response paths honor the contract — cache
// hit, fresh render, and cache disabled — so a repeat open with a
// validator pays neither IMAP FETCH nor render (the DEF-3 follow-up).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/rendercache"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// etagTestHandler builds the native fixture (fake IMAP, no PG) and
// returns the router, the IMAP stub and a bearer token.
func etagTestHandler(t *testing.T, withCache bool) (*gin.Engine, *fakeIMAPClient, string) {
	t.Helper()
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := mail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	msg := &webmail.Message{
		MessageSummary: webmail.MessageSummary{UID: 7, Subject: "original render"},
		UIDValidity:    42,
		HTML:           "<p>original</p>",
	}
	sessions := &fakeMailboxSessions{}
	// The detail handler stores the rendered document back into the
	// message struct it was handed. A real IMAP fetch returns a fresh
	// struct every time; the copy-per-call wrapper keeps the fixture
	// honest, otherwise the second render would wrap an already wrapped
	// document and the ETag of two identical opens would drift.
	imap := &freshCopyIMAPClient{fakeIMAPClient: fakeIMAPClient{message: msg}}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{wantAddress: "alice@example.test", client: imap},
	}
	if withCache {
		h.RenderCache = rendercache.New()
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
	return r, &imap.fakeIMAPClient, token
}

// freshCopyIMAPClient serves every GetMessage a shallow copy, mirroring
// a real IMAP FETCH that hands the caller its own struct.
type freshCopyIMAPClient struct {
	fakeIMAPClient
}

func (f *freshCopyIMAPClient) GetMessage(ctx context.Context, folder string, uid uint32) (*webmail.Message, error) {
	msg, err := f.fakeIMAPClient.GetMessage(ctx, folder, uid)
	if err != nil {
		return nil, err
	}
	copied := *msg
	return &copied, nil
}

func etagRequest(t *testing.T, r *gin.Engine, token, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	r.ServeHTTP(w, req)
	return w
}

// TestGetMessageETagConditionalRequests walks the cache-enabled paths:
// the first open misses (fresh render, miss header), a matching
// validator gets a 304 off the cache hit path, weak/star validators
// compare per RFC 9110, a stale validator serves the cached body, and
// the ETag matches the sha256 of the exact body bytes.
func TestGetMessageETagConditionalRequests(t *testing.T) {
	r, imap, token := etagTestHandler(t, true)
	folder := webmail.EncodeFolderName("INBOX")
	path := "/api/v1/mailbox/folders/" + folder + "/messages/7"

	// First open: fresh render.
	first := etagRequest(t, r, token, path, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first open %d %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("X-Render-Cache"); got != "miss" {
		t.Fatalf("first open X-Render-Cache=%q, want miss", got)
	}
	if got := first.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("first open Cache-Control=%q", got)
	}
	etag := first.Header().Get("ETag")
	sum := sha256.Sum256(first.Body.Bytes())
	if want := `"` + hex.EncodeToString(sum[:]) + `"`; etag != want {
		t.Fatalf("ETag %q, want sha256 of body %q", etag, want)
	}

	// The IMAP fixture now returns a different body: conditional and
	// unconditional repeat opens must not reach it while cached.
	changed := *imap.message
	changed.Subject = "changed render"
	changed.HTML = "<p>changed</p>"
	imap.message = &changed

	// Second open with the validator: 304 served from the cache hit
	// path, no body bytes.
	second := etagRequest(t, r, token, path, etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("conditional open %d %s", second.Code, second.Body.String())
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 carried a body: %d bytes", second.Body.Len())
	}
	if got := second.Header().Get("X-Render-Cache"); got != "hit" {
		t.Fatalf("conditional open X-Render-Cache=%q, want hit", got)
	}
	if got := second.Header().Get("ETag"); got != etag {
		t.Fatalf("304 ETag %q, want %q", got, etag)
	}

	// Star validator matches any existing representation.
	if w := etagRequest(t, r, token, path, "*"); w.Code != http.StatusNotModified {
		t.Fatalf("star validator %d", w.Code)
	}
	// Weak validators compare weakly.
	if w := etagRequest(t, r, token, path, "W/"+etag); w.Code != http.StatusNotModified {
		t.Fatalf("weak validator %d", w.Code)
	}
	// Validator lists: one stale entry and the current tag.
	if w := etagRequest(t, r, token, path, `"deadbeef", `+etag); w.Code != http.StatusNotModified {
		t.Fatalf("list validator %d", w.Code)
	}

	// Stale validator: the cached body replays without another render.
	stale := etagRequest(t, r, token, path, `"deadbeef"`)
	if stale.Code != http.StatusOK {
		t.Fatalf("stale validator %d %s", stale.Code, stale.Body.String())
	}
	if got := stale.Header().Get("X-Render-Cache"); got != "hit" {
		t.Fatalf("stale validator X-Render-Cache=%q, want hit", got)
	}
	var payload struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(stale.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode stale body: %v", err)
	}
	if payload.Subject != "original render" {
		t.Fatalf("stale validator re-rendered: %q", payload.Subject)
	}
	if stale.Header().Get("ETag") != etag {
		t.Fatalf("replayed body ETag drifted: %q", stale.Header().Get("ETag"))
	}
}

// TestGetMessageETagWithoutRenderCache pins the cache-disabled path:
// the ETag contract still works and the observability header stays
// absent.
func TestGetMessageETagWithoutRenderCache(t *testing.T) {
	r, _, token := etagTestHandler(t, false)
	folder := webmail.EncodeFolderName("INBOX")
	path := "/api/v1/mailbox/folders/" + folder + "/messages/7"

	first := etagRequest(t, r, token, path, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first open %d %s", first.Code, first.Body.String())
	}
	if got := first.Header().Get("X-Render-Cache"); got != "" {
		t.Fatalf("cache-disabled open sets X-Render-Cache=%q", got)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("cache-disabled open carries no ETag")
	}

	second := etagRequest(t, r, token, path, etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("conditional open %d %s", second.Code, second.Body.String())
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 carried a body: %d bytes", second.Body.Len())
	}
	if got := second.Header().Get("X-Render-Cache"); got != "" {
		t.Fatalf("cache-disabled 304 sets X-Render-Cache=%q", got)
	}
}
