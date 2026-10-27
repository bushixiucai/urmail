package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/airetrieval"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/aianswer"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// fakeAskIMAP implements the slice of webmail.Client the handler reads.
type fakeAskIMAP struct {
	webmail.Client
	messages map[string]map[uint32]string
	validity map[string]uint32
}

func (f *fakeAskIMAP) ListFolders(context.Context) ([]webmail.Folder, error) {
	names := make([]webmail.Folder, 0, len(f.messages))
	for name := range f.messages {
		names = append(names, webmail.Folder{Name: name})
	}
	return names, nil
}

func (f *fakeAskIMAP) FolderUIDValidity(_ context.Context, folder string) (uint32, error) {
	return f.validity[folder], nil
}

func (f *fakeAskIMAP) ListMessages(_ context.Context, folder string, _ webmail.ListOpts) (webmail.ListResult, error) {
	result := webmail.ListResult{}
	for uid := range f.messages[folder] {
		result.Messages = append(result.Messages, webmail.MessageSummary{UID: uid})
	}
	return result, nil
}

func (f *fakeAskIMAP) GetMessage(_ context.Context, folder string, uid uint32) (*webmail.Message, error) {
	message := &webmail.Message{MessageSummary: webmail.MessageSummary{UID: uid}, UIDValidity: f.validity[folder]}
	if text, ok := f.messages[folder][uid]; ok {
		message.Text = text
	}
	return message, nil
}

func (f *fakeAskIMAP) Close() error { return nil }

type fakeAskDialer struct{ client *fakeAskIMAP }

func (d fakeAskDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return d.client, nil
}

func newAskRouter(t *testing.T, build func(webmail.CredentialVault) *AISearchAsk) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := mail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeMailboxSessions{}
	vault := webmail.NewMemoryVault()
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash}, Sessions: sessions, Vault: vault}
	r := newNativeTestRouter(t, native, sessions)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.POST("/ai/search-ask", build(vault).Handle)
	t.Cleanup(func() { loginAttempts = newLoginLimiter() })
	return r
}

func askOnce(t *testing.T, r *gin.Engine, token, question string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/ai/search-ask", bytes.NewBufferString(`{"question":"`+question+`"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAISearchAskGroundedAnswerCitesRetrievedChunks(t *testing.T) {
	calls := 0
	source := &airetrieval.FakeSource{Mail: map[string]map[uint32]string{"INBOX": {5: "the invoice total is 42 euros"}}, Validity: map[string]uint32{"INBOX": 4}}
	store := airetrieval.NewFakeStore()
	principal := airetrieval.Principal{TenantID: 7, MailboxID: 9}
	if _, err := (airetrieval.Indexer{Store: store, Source: source}).Reindex(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	model := func(context.Context, string, []aianswer.Source) (string, []string, error) {
		calls++
		return "42 euros.", []string{"1"}, nil
	}
	handler := &AISearchAsk{Pool: nil, Store: store, Dialer: fakeAskDialer{client: &fakeAskIMAP{messages: map[string]map[uint32]string{"INBOX": {5: "the invoice total is 42 euros"}}, validity: map[string]uint32{"INBOX": 4}}}, Vault: webmail.NewMemoryVault(), ModelFor: func(context.Context, int64) (aianswer.Model, error) { return model, nil }}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk { handler.Vault = vault; return handler })
	native := loginNative(t, r, "alice@example.test", "correct-horse-battery")

	// Vault the session password exactly like the native login flow does.
	nativeSessions := middleware.ContextMailboxTokenHash
	_ = nativeSessions

	rec := askOnce(t, r, native, "invoice total")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("model calls = %d", calls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "\"grounded\":true") || !strings.Contains(body, "\"citations\":[\"1\"]") {
		t.Fatal(body)
	}
}

func TestAISearchAskEmptyMailboxMakesZeroModelCalls(t *testing.T) {
	calls := 0
	store := airetrieval.NewFakeStore()
	model := func(context.Context, string, []aianswer.Source) (string, []string, error) {
		calls++
		return "invented", nil, nil
	}
	handler := &AISearchAsk{Pool: nil, Store: store, Dialer: fakeAskDialer{client: &fakeAskIMAP{messages: map[string]map[uint32]string{"INBOX": {}}}}, Vault: webmail.NewMemoryVault(), ModelFor: func(context.Context, int64) (aianswer.Model, error) { return model, nil }}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk { handler.Vault = vault; return handler })
	native := loginNative(t, r, "alice@example.test", "correct-horse-battery")
	rec := askOnce(t, r, native, "anything")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if calls != 0 || !strings.Contains(rec.Body.String(), "\"grounded\":false") {
		t.Fatalf("calls=%d %s", calls, rec.Body.String())
	}
}

func TestAISearchAskRequiresMailboxSession(t *testing.T) {
	handler := &AISearchAsk{}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk { handler.Vault = vault; return handler })
	rec := askOnce(t, r, "", "hello")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

var _ = sync.Mutex{}
