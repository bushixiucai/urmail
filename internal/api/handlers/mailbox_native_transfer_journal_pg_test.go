package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestMailTransferDurableReplayAndUnknownPG(t *testing.T) {
	fixture := newNativeAIHTTPFixture(t)
	f := &managedFolderFixture{fakeIMAPClient: &fakeIMAPClient{folders: []webmail.Folder{{Name: "INBOX"}, {Name: "Custom"}, {Name: "Trash", Flags: []string{`\Trash`}}}}}
	vault := webmail.NewMemoryVault()
	vault.Put("token", "password")
	h := &MailboxNativeHandler{Enabled: true, Pool: fixture.pool, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{}, Vault: vault, Dialer: &fakeDialer{wantAddress: fixture.principal.Address, client: f}}
	request := func(action, key, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		// Recreate the router and handler wrapper for every retry.
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: fixture.principal.TenantID, MailboxID: fixture.principal.MailboxID, Address: fixture.principal.Address})
			c.Set(middleware.ContextMailboxTokenHash, "token")
		})
		router.POST("/folders/:name/messages/copy", h.CopyMessages)
		router.POST("/folders/:name/messages/trash", h.TrashMessages)
		router.POST("/folders/:name/messages/restore", h.RestoreMessages)
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/folders/INBOX/messages/"+action, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", key, w.Code, w.Body.String())
		}
		return w
	}
	const body = `{"uids":[1],"uid_validity":9,"to":"Custom"}`
	first := request("copy", "copy-once-after-lost-http", body, 200)
	replay := request("copy", "copy-once-after-lost-http", body, 200)
	if first.Body.String() != replay.Body.String() || f.mutations != 1 {
		t.Fatal("COPY repeated after lost HTTP response")
	}
	request("copy", "copy-once-after-lost-http", `{"uids":[2],"uid_validity":9,"to":"Custom"}`, 409)
	f.transferError = true
	request("copy", "copy-unknown-acknowledgement", body, 202)
	f.transferError = false
	request("copy", "copy-unknown-acknowledgement", body, 409)
	if f.mutations != 2 {
		t.Fatal("uncertain COPY retried after restart")
	}
	stale := request("trash", "stale-generation-refused", `{"uids":[1],"uid_validity":8}`, 409)
	if !strings.Contains(stale.Body.String(), "DRAFT_CHANGED") || f.mutations != 2 {
		t.Fatal("stale generation changed mailbox")
	}
}
