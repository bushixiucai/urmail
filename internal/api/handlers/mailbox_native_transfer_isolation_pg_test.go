package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestMailTransferReceiptIsTenantScopedPG pins the rejection side of the
// platform-scope binding: the transfer journal queries now run with platform
// scope, so the tenant and mailbox filters must still come from the session.
// A second tenant replaying the same idempotency key must not receive the first
// tenant's stored receipt; it performs its own copy and stores its own row.
func TestMailTransferReceiptIsTenantScopedPG(t *testing.T) {
	fixture := newNativeAIHTTPFixture(t)
	f := &managedFolderFixture{fakeIMAPClient: &fakeIMAPClient{folders: []webmail.Folder{{Name: "INBOX"}, {Name: "Custom"}}}}
	vault := webmail.NewMemoryVault()
	vault.Put("token", "password") //nolint:staticcheck // test adapter seeding, Put exists for this
	h := &MailboxNativeHandler{Enabled: true, Pool: fixture.pool, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{}, Vault: vault, Dialer: &fakeDialer{wantAddress: fixture.principal.Address, client: f}}
	copyAs := func(tenantID, mailboxID int64, key string) *httptest.ResponseRecorder {
		t.Helper()
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: tenantID, MailboxID: mailboxID, Address: fixture.principal.Address})
			c.Set(middleware.ContextMailboxTokenHash, "token")
		})
		router.POST("/folders/:name/messages/copy", h.CopyMessages)
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/folders/INBOX/messages/copy",
			strings.NewReader(`{"uids":[1],"uid_validity":9,"to":"Custom"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	const key = "tenant-scoped-receipt"
	first := copyAs(fixture.principal.TenantID, fixture.principal.MailboxID, key)
	if first.Code != 200 {
		t.Fatalf("tenant A copy: %d %s", first.Code, first.Body.String())
	}
	if f.mutations != 1 {
		t.Fatalf("tenant A mutations=%d, want 1", f.mutations)
	}
	// Same key, different tenant: a tenant-scoped receipt means this must run a
	// real copy (mutations increments) instead of replaying tenant A's answer.
	second := copyAs(fixture.principal.TenantID+1000, fixture.principal.MailboxID+1000, key)
	if second.Code != 200 {
		t.Fatalf("tenant B copy: %d %s", second.Code, second.Body.String())
	}
	if f.mutations != 2 {
		t.Fatalf("tenant B replayed tenant A's receipt: mutations=%d, want 2", f.mutations)
	}
	var rows int
	if err := fixture.pool.QueryRow(t.Context(),
		`SELECT count(DISTINCT tenant_id) FROM mailbox_jobs WHERE kind='mail_transfer' AND idempotency_key=$1`, key).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("mail_transfer receipts span %d tenants, want 2 separate rows", rows)
	}
}
