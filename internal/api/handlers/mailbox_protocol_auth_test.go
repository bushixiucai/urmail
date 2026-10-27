package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestMailboxProtocolBasicAuthUsesVerifiedMailboxAndForgetsCredential(t *testing.T) {
	hash, err := domainMail.HashPassword("protocol-client-secret")
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeMailboxSessions{}
	vault := webmail.NewMemoryVault()
	h := &MailboxNativeHandler{Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "protocol@example.com", hash: hash}, Sessions: sessions, Vault: vault}
	r := gin.New()
	var usedHash string
	r.Handle("PROPFIND", "/dav/", h.ProtocolAuth(), func(c *gin.Context) {
		session, ok := middleware.GetMailboxSession(c)
		if !ok || session.MailboxID != 9 || session.TenantID != 7 {
			t.Fatal("missing verified mailbox principal")
		}
		usedHash = session.TokenHash
		if password, ok := vault.Get(usedHash); !ok || password != "protocol-client-secret" {
			t.Fatal("request credential unavailable")
		}
		c.Status(http.StatusMultiStatus)
	})
	req := httptest.NewRequestWithContext(context.Background(), "PROPFIND", "/dav/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.SetBasicAuth("protocol@example.com", "protocol-client-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(sessions.byHash) != 0 {
		t.Fatal("request session was retained")
	}
	if _, ok := vault.Get(usedHash); ok {
		t.Fatal("request credential retained")
	}
}

func TestMailboxProtocolRejectsWrongPasswordAndDirectCleartext(t *testing.T) {
	hash, err := domainMail.HashPassword("correct-protocol-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, peer, password string
		status               int
	}{
		{"wrong password", "127.0.0.1:12345", "incorrect", http.StatusUnauthorized},
		{"untrusted plaintext", "192.0.2.2:12345", "correct-protocol-secret", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &fakeMailboxSessions{}
			h := &MailboxNativeHandler{Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "reject-protocol@example.com", hash: hash}, Sessions: sessions, Vault: webmail.NewMemoryVault()}
			r := gin.New()
			r.POST("/eas", h.ProtocolAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/eas", nil)
			req.RemoteAddr = tc.peer
			req.SetBasicAuth("reject-protocol@example.com", tc.password)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			if len(sessions.byHash) != 0 {
				t.Fatal("rejected credential established session")
			}
		})
	}
}

func TestMailboxProtocolBearerCannotBypassTLS(t *testing.T) {
	h := &MailboxNativeHandler{}
	r := gin.New()
	r.POST("/eas", h.ProtocolAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/eas", nil)
	req.RemoteAddr = "192.0.2.2:12345"
	req.Header.Set("Authorization", "Bearer opaque-token")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("remote plaintext bearer status=%d", w.Code)
	}
}
