package handlers

// DEF-3 regression (v0.6.10-hotfix-2): GetMessage used to require
// ?uid_validity for every cache hit, so clients that omit it (the QA
// probes and the webmail list→detail flow) paid the full IMAP FETCH +
// sanitize pass on every open. The loose lookup serves the cached
// rendering when uid_validity is absent, and the payload still echoes the
// uid_validity the entry was rendered with.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/rendercache"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestGetMessageCacheHitsWithoutUIDValidity(t *testing.T) {
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
	imap := &fakeIMAPClient{message: msg}
	h := &MailboxNativeHandler{
		Enabled:     true,
		Auth:        fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions:    sessions,
		Vault:       webmail.NewMemoryVault(),
		Dialer:      &fakeDialer{wantAddress: "alice@example.test", client: imap},
		RenderCache: rendercache.New(),
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
	folder := webmail.EncodeFolderName("INBOX")
	get := func(path string, uaClass string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if uaClass != "" {
			req.Header.Set("X-UA-Class", uaClass)
		}
		r.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) (string, uint32) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("get %d %s", w.Code, w.Body.String())
		}
		var got struct {
			Subject     string `json:"subject"`
			UIDValidity uint32 `json:"uid_validity"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got.Subject, got.UIDValidity
	}
	path := "/api/v1/mailbox/folders/" + folder + "/messages/7"

	// First open renders and caches; the response carries uid_validity.
	subject, validity := decode(get(path, ""))
	if subject != "original render" || validity != 42 {
		t.Fatalf("first open = %q validity %d", subject, validity)
	}

	// The IMAP fixture now returns a different body: a cache hit must
	// keep serving the original render instead of re-fetching.
	changed := *msg
	changed.Subject = "changed render"
	changed.HTML = "<p>changed</p>"
	imap.message = &changed

	// Second open without uid_validity: loose hit, no IMAP round trip.
	subject, validity = decode(get(path, ""))
	if subject != "original render" || validity != 42 {
		t.Fatalf("loose hit = %q validity %d, want the cached render with its uid_validity", subject, validity)
	}

	// Third open WITH uid_validity: exact-key hit behaves as before.
	subject, _ = decode(get(path+"?uid_validity=42", ""))
	if subject != "original render" {
		t.Fatalf("exact hit = %q", subject)
	}

	// A different viewport dimension must miss and re-render.
	subject, _ = decode(get(path, "mobile"))
	if subject != "changed render" {
		t.Fatalf("changed UA class must re-render, got cached %q", subject)
	}
}
