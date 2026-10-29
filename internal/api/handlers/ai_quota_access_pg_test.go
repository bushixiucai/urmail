package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestAIQuotaReadersRecheckTenantPlatformAndMailboxPG(t *testing.T) {
	pool, principal, _, _, actor := aiQuotaLegacyFixture(t)
	if response := aiQuotaPut(t, pool, principal.TenantID, actor, aiQuotaCaps(3)); response.Code != 204 {
		t.Fatal(response.Code, response.Body)
	}
	var foreignUser int64
	if err := pool.QueryRow(t.Context(), "INSERT INTO users(email,password_hash)VALUES('foreign-quota-reader@fixture.test','fixture')RETURNING id").Scan(&foreignUser); err != nil {
		t.Fatal(err)
	}
	read := func(user int64, platform bool) *httptest.ResponseRecorder {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.ContextUserID, user)
			c.Set(middleware.ContextTenantID, principal.TenantID)
			c.Next()
		})
		handler := &AIConfigurationHandler{Pool: pool}
		router.GET("/ai/budget", handler.TenantBudgetGet)
		router.GET("/tenants/:id/ai/budget", handler.PlatformBudgetGet)
		path := "/ai/budget?tenant_id=999999"
		if platform {
			path = fmt.Sprintf("/tenants/%d/ai/budget", principal.TenantID)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return response
	}
	if response := read(foreignUser, false); response.Code != 403 || strings.Contains(response.Body.String(), "configured_nonplatform") {
		t.Fatal("foreign reader saw tenant caps", response.Code, response.Body)
	}
	if _, err := pool.Exec(t.Context(), "DELETE FROM platform_admins WHERE user_id=$1", actor); err != nil {
		t.Fatal(err)
	}
	if response := read(actor, true); response.Code != 403 {
		t.Fatal("revoked platform actor read platform quota", response.Code, response.Body)
	}
	if response := read(actor, false); response.Code != 200 || !strings.Contains(response.Body.String(), "configured_nonplatform") || strings.Contains(response.Body.String(), "encrypted_key") {
		t.Fatal("owner quota read changed or leaked credentials", response.Code, response.Body)
	}
	mailboxRead := func(token string) *httptest.ResponseRecorder {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: principal.TenantID, MailboxID: principal.MailboxID, TokenHash: token})
			c.Next()
		})
		router.GET("/budget", (&AISearchAskAccounting{Pool: pool}).BudgetStatus)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/budget", nil))
		return response
	}
	if response := mailboxRead("forged-or-stale-token"); response.Code != 401 {
		t.Fatal("stale mailbox principal read quota", response.Code, response.Body)
	}
	if response := mailboxRead(principal.SessionHash); response.Code != 200 || !strings.Contains(response.Body.String(), `"mailbox_calls_remaining":3`) {
		t.Fatal("mailbox quota did not use actual principal", response.Code, response.Body)
	}
	if err := store.New(pool).PlatformRevokeMailboxSession(t.Context(), principal.SessionHash); err != nil {
		t.Fatal(err)
	}
	if response := mailboxRead(principal.SessionHash); response.Code != 401 {
		t.Fatal("revoked mailbox session read quota", response.Code, response.Body)
	}
}
