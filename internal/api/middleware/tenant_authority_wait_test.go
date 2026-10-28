package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
)

// A queued request must not retain authority read before a tenant lock wait.
func TestTenantWriterRechecksAuthorityAfterLockWait(t *testing.T) {
	for _, change := range []string{"revoke", "disable"} {
		t.Run(change, func(t *testing.T) {
			app, platform, tenant, actor := transactionV3Fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			blocker, err := platform.PlatformTx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if _, err = blocker.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", tenant); err != nil {
				t.Fatal(err)
			}
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Set(middleware.ContextTenantID, tenant)
				c.Set(middleware.ContextUserID, actor)
			})
			engine.Use(middleware.RequireTenantTx(app, zap.NewNop()))
			called := false
			engine.POST("/protected", middleware.RequireRole("owner", "admin"), func(c *gin.Context) { called = true; c.Status(http.StatusNoContent) })
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/protected", nil))
			}()
			waiting := false
			for ctx.Err() == nil {
				if err = platform.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=0 AND objid=$1::oid)`, tenant).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("protected request never reached the held tenant lock")
			}
			if change == "revoke" {
				_, err = platform.Exec(ctx, "DELETE FROM user_tenant_roles WHERE tenant_id=$1 AND user_id=$2", tenant, actor)
			} else {
				_, err = platform.Exec(ctx, "UPDATE users SET status='disabled' WHERE id=$1", actor)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("request did not finish after tenant lock release")
			}
			if called || response.Code != http.StatusForbidden {
				t.Fatalf("revoked request reached handler=%v HTTP=%d body=%s", called, response.Code, response.Body.String())
			}
		})
	}
}
