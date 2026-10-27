package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type fakeAIDrafts struct{ row store.PlatformGetAIDraftRow }

func (f *fakeAIDrafts) PlatformInsertAIDraft(_ context.Context, arg store.PlatformInsertAIDraftParams) (store.PlatformInsertAIDraftRow, error) {
	f.row = store.PlatformGetAIDraftRow{ID: 4, Subject: arg.Subject, Body: arg.Body, Version: 1}
	return store.PlatformInsertAIDraftRow{ID: 4, Version: 1}, nil
}
func (f *fakeAIDrafts) PlatformGetAIDraft(context.Context, store.PlatformGetAIDraftParams) (store.PlatformGetAIDraftRow, error) {
	return f.row, nil
}
func (f *fakeAIDrafts) PlatformUpdateAIDraft(_ context.Context, arg store.PlatformUpdateAIDraftParams) (int64, error) {
	if arg.Version != f.row.Version {
		return 0, pgx.ErrNoRows
	}
	f.row.Subject, f.row.Body, f.row.Version = arg.Subject, arg.Body, arg.Version+1
	return f.row.Version, nil
}

func TestAIDraftUpdateRejectsStaleVersion(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeMailboxSessions{}
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash}, Sessions: sessions, Vault: webmail.NewMemoryVault()}
	drafts := AIDrafts{Q: &fakeAIDrafts{}}
	r := newNativeTestRouter(t, native, sessions)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.POST("/ai/drafts", drafts.Create)
	auth.PUT("/ai/drafts/:id", drafts.Update)
	token := loginNative(t, r, "alice@example.test", password)
	create := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/ai/drafts", bytes.NewBufferString("{\"subject\":\"Hello\",\"body\":\"first\"}"))
	create.Header.Set("Authorization", "Bearer "+token)
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	r.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body.String())
	}
	update := func(version, body string) *httptest.ResponseRecorder {
		payload := "{\"subject\":\"Hello\",\"body\":\"" + body + "\",\"version\":" + version + "}"
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/api/v1/mailbox/ai/drafts/4", bytes.NewBufferString(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if got := update("1", "user"); got.Code != http.StatusOK {
		t.Fatal(got.Body.String())
	}
	if got := update("1", "late"); got.Code != http.StatusConflict {
		t.Fatalf("%d %s", got.Code, got.Body.String())
	}
}
