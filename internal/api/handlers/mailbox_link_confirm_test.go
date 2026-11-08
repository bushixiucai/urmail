package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/linkconfirm"
)

func linkConfirmRouter(t *testing.T, handler *MailboxNativeHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/mailbox/links/confirm", handler.LinkConfirm)
	r.POST("/api/v1/mailbox/links/confirm", handler.LinkConfirm)
	return r
}

func TestLinkConfirmPageRendersHostPerCharacter(t *testing.T) {
	signer := linkconfirm.NewSigner([]byte("render-key"))
	token, err := signer.Sign(linkconfirm.Payload{
		MailboxID: 9, UID: 7, Expires: time.Now().Add(time.Hour).Unix(),
		URL: "https://аррӏе.com/login", From: "Eve <eve@evil.test>", Subject: "Reset",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := &MailboxNativeHandler{RenderKey: []byte("render-key")}
	r := linkConfirmRouter(t, handler)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/links/confirm?t="+url.QueryEscape(token), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="ch"`) {
		t.Fatalf("host not per-character: %s", body)
	}
	if !strings.Contains(body, "https://аррӏе.com/login") {
		t.Fatalf("full url missing: %s", body)
	}
	if !strings.Contains(body, "Eve") || !strings.Contains(body, "Reset") {
		t.Fatalf("from/subject missing: %s", body)
	}
	if !strings.Contains(body, "look-alike") {
		t.Fatalf("first-visit warning missing: %s", body)
	}
}

func TestLinkConfirmPOSTRedirects(t *testing.T) {
	signer := linkconfirm.NewSigner([]byte("render-key"))
	token, err := signer.Sign(linkconfirm.Payload{
		MailboxID: 9, UID: 7, Expires: time.Now().Add(time.Hour).Unix(),
		URL: "https://example.com/path",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := &MailboxNativeHandler{RenderKey: []byte("render-key")}
	r := linkConfirmRouter(t, handler)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/links/confirm", strings.NewReader("t="+url.QueryEscape(token)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "https://example.com/path" {
		t.Fatalf("location %q", got)
	}
}

func TestLinkConfirmRejectsBadToken(t *testing.T) {
	handler := &MailboxNativeHandler{RenderKey: []byte("render-key")}
	r := linkConfirmRouter(t, handler)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/links/confirm?t=bad", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
}
