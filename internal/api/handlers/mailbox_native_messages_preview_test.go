package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestMailboxNativeListMessagesPreview checks that the list endpoint requests
// snippets by default (ListOpts.Preview), keeps them in the JSON payload, and
// drops both the fetch request and the field when preview=0 is passed.
func TestMailboxNativeListMessagesPreview(t *testing.T) {
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := mail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	imap := &fakeIMAPClient{
		folders: []webmail.Folder{{Name: "INBOX", Delimiter: "/"}},
		listed: webmail.ListResult{
			Messages: []webmail.MessageSummary{{UID: 5, Subject: "weekly", Preview: "Hello snippet line"}},
			Total:    1,
		},
	}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{wantAddress: "alice@example.test", client: imap},
	}
	r := newNativeTestRouter(t, h, sessions)
	token := loginNative(t, r, "alice@example.test", password)
	folder := webmail.EncodeFolderName("INBOX")

	list := func(url string) []byte {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("list %s -> %d %s", url, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}

	body := list("/api/v1/mailbox/folders/" + folder + "/messages?limit=10")
	if !imap.lastListOpts.Preview {
		t.Fatal("preview must be requested by default")
	}
	if !bytes.Contains(body, []byte(`"preview":"Hello snippet line"`)) {
		t.Fatalf("body missing preview: %s", body)
	}

	// The fake would otherwise replay the same pre-filled preview; clear it to
	// model a backend that answered without snippets.
	imap.listed.Messages[0].Preview = ""
	body = list("/api/v1/mailbox/folders/" + folder + "/messages?limit=10&preview=0")
	if imap.lastListOpts.Preview {
		t.Fatal("preview=0 must disable the snippet fetch")
	}
	if bytes.Contains(body, []byte(`"preview"`)) {
		t.Fatalf("preview=0 must omit the field: %s", body)
	}
}
