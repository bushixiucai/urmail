package handlers

// Slice-1.5 acceptance (QA TC-13a fix): in SQLite single-engine mode the
// router wires ReadOnlyStore = nil, so folder/message listings fall through
// to the live dialIMAP path and serve Dovecot's authoritative data — real
// UIDVALIDITY, real rows — instead of the (writerless, always-empty) SQLite
// index tables. This test pins the handler contract that makes that true:
// ReadOnlyStore nil ⇒ the IMAP dial happens and its data comes back verbatim
// through the authenticated HTTP surface.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestSQLiteModeListingsServeIMAPNotReadOnlyStore(t *testing.T) {
	loginAttempts = newLoginLimiter()
	password := "slice15-mailbox-password"
	hash, hashErr := domainMail.HashPassword(password)
	if hashErr != nil {
		t.Fatalf("hash: %v", hashErr)
	}
	const (
		tenantID    = int64(7)
		mailboxID   = int64(11)
		mailAddress = "alice@a.example.test"
		uidValidity = uint32(1662778624) // real Dovecot-style UIDVALIDITY, not the placeholder 1
	)
	sessions := &fakeMailboxSessions{}
	imap := &fakeIMAPClient{
		folders: []webmail.Folder{{
			Name:      "INBOX",
			NameEnc:   webmail.EncodeFolderName("INBOX"),
			Delimiter: "/",
			Flags:     []string{`\HasNoChildren`},
		}},
		listed: webmail.ListResult{
			Messages: []webmail.MessageSummary{{
				UID:         101,
				Subject:     "Real Dovecot mail",
				UIDValidity: uidValidity,
			}},
			Total:       1,
			UIDValidity: uidValidity,
		},
	}
	h := &MailboxNativeHandler{
		Enabled: true,
		// The slice-1.5 router wiring: ReadOnlyStore deliberately nil so the
		// handlers take the dialIMAP branch. If this field is non-nil the
		// regression TC-13a returns (silently empty lists from the writerless
		// SQLite tables), so the dial assertion below is the real guard.
		ReadOnlyStore: nil,
		Auth:          fakeMailboxAuth{tenantID: tenantID, mailboxID: mailboxID, address: mailAddress, hash: hash},
		Sessions:      sessions,
		Vault:         webmail.NewMemoryVault(),
		Dialer:        &fakeDialer{wantAddress: mailAddress, client: imap},
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.POST("/api/v1/mailbox/session", h.CreateSession)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/folders", h.ListFolders)
	auth.GET("/folders/:name/messages", h.ListMessages)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	login := func() string {
		t.Helper()
		req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodPost,
			srv.URL+"/api/v1/mailbox/session", strings.NewReader(`{"email":"`+mailAddress+`","password":"`+password+`"}`))
		if reqErr != nil {
			t.Fatalf("login request: %v", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("login = %d", res.StatusCode)
		}
		var payload struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil || payload.Token == "" {
			t.Fatalf("login payload decode: %v", err)
		}
		return payload.Token
	}
	get := func(token, path string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = res.Body.Close() }()
		bodyBytes, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return res.StatusCode, string(bodyBytes)
	}

	token := login()
	dialer := h.Dialer.(*fakeDialer)

	// Folder listing: the IMAP dial must happen and its folders must come
	// back through the authenticated surface.
	code, body := get(token, "/api/v1/mailbox/folders")
	if code != http.StatusOK {
		t.Fatalf("GET /folders = %d %s, want 200", code, body)
	}
	if !dialer.dialed {
		t.Fatalf("folders listing never dialed IMAP: ReadOnlyStore fallback returned instead")
	}
	var folderPayload struct {
		Folders []webmail.Folder `json:"folders"`
	}
	if err := json.Unmarshal([]byte(body), &folderPayload); err != nil {
		t.Fatalf("folders payload: %v (%s)", err, body)
	}
	if len(folderPayload.Folders) != 1 || folderPayload.Folders[0].Name != "INBOX" {
		t.Fatalf("folders = %+v, want the IMAP-provided INBOX", folderPayload.Folders)
	}

	// Message listing: UIDVALIDITY comes from IMAP (1662778624), never the
	// writerless-store placeholder (1).
	code, body = get(token, "/api/v1/mailbox/folders/INBOX/messages")
	if code != http.StatusOK {
		t.Fatalf("GET /folders/INBOX/messages = %d %s, want 200", code, body)
	}
	var listPayload struct {
		Messages    []webmail.MessageSummary `json:"messages"`
		UIDValidity uint32                   `json:"uid_validity"`
		Total       int                      `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &listPayload); err != nil {
		t.Fatalf("list payload: %v (%s)", err, body)
	}
	if listPayload.UIDValidity != uidValidity {
		t.Fatalf("uid_validity = %d, want IMAP's %d (placeholder would mean the SQLite ReadOnlyStore answered)", listPayload.UIDValidity, uidValidity)
	}
	if listPayload.Total != 1 || len(listPayload.Messages) != 1 || listPayload.Messages[0].Subject != "Real Dovecot mail" {
		t.Fatalf("messages = %+v, want the IMAP-provided row", listPayload)
	}
}
