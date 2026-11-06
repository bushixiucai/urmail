package middleware_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestPlatformTransactionAuditsCommitRollbackAndCallback(t *testing.T) {
	_, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	r := gin.New()
	r.Use(middleware.AuditPlatform(store.New(p), zap.NewNop()))
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, uid) })
	r.Use(middleware.RequirePlatformTx(p, zap.NewNop()))
	r.POST("/api/v1/platform/tenants/:id/mutation", func(c *gin.Context) {
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		if _, err := q.PlatformUpdateTenantStatus(c, store.PlatformUpdateTenantStatusParams{ID: tid, Status: "suspended"}); err != nil {
			t.Fatal(err)
		}
		c.Status(201)
		switch c.Query("case") {
		case "aborted":
			transaction, _ := c.Get("urmail.transaction")
			_, _ = transaction.(pgx.Tx).Exec(c, "SELECT 1/0")
		case "callback":
			middleware.AfterCommit(c, func() { c.JSON(502, gin.H{"error": gin.H{"code": "PROVIDER_ERROR"}}) })
		}
	})
	request := func(suffix string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/platform/tenants/"+intString(tid)+"/mutation"+suffix, nil)
		r.ServeHTTP(rec, req)
		return rec
	}
	if got := request(""); got.Code != 201 {
		t.Fatalf("commit %d %s", got.Code, got.Body)
	}
	var success int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND detail->>'status'='201' AND detail->>'phase'='transaction'", tid).Scan(&success); err != nil || success != 1 {
		t.Fatalf("success audit missing/duplicated: %d %v", success, err)
	}
	if _, err := p.Exec(ctx, "UPDATE tenants SET status='active' WHERE id=$1", tid); err != nil {
		t.Fatal(err)
	}
	if got := request("?case=aborted"); got.Code != 500 {
		t.Fatalf("aborted commit %d %s", got.Code, got.Body)
	}
	var status string
	if err := p.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", tid).Scan(&status); err != nil || status != "active" {
		t.Fatalf("failed transaction retained mutation: %s %v", status, err)
	}
	var failed int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND detail->>'status'='500'", tid).Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("commit failure lost audit: %d %v", failed, err)
	}
	if got := request("?case=callback"); got.Code != 502 {
		t.Fatalf("callback final status %d %s", got.Code, got.Body)
	}
	var callbacks int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND detail->>'status'='502' AND detail->>'phase'='after_commit'", tid).Scan(&callbacks); err != nil || callbacks != 1 {
		t.Fatalf("callback final audit missing %d %v", callbacks, err)
	}
}

func TestSensitiveArchiveReadRejectionHasNoUntrustedTenantOrSearch(t *testing.T) {
	_, p, _, _ := transactionV3Fixture(t)
	r := gin.New()
	r.Use(middleware.Audit(store.New(p), zap.NewNop()))
	r.GET("/api/v1/archive/search", func(c *gin.Context) { c.AbortWithStatus(403) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/archive/search?tenant_id=999&q=private-body", nil))
	var count int
	if err := p.QueryRow(t.Context(), "SELECT count(*) FROM audit_logs WHERE tenant_id IS NULL AND actor_user_id IS NULL AND action='GET /api/v1/archive/search' AND target_id='/api/v1/archive/search' AND detail->>'status'='403'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("sensitive failure audit %d %v", count, err)
	}
}

func TestPlatformAfterCommitReplacesPreparedJSONAndKeepsFailureAudit(t *testing.T) {
	_, p, tid, uid := transactionV3Fixture(t)
	q := store.New(p)
	r := gin.New()
	r.Use(middleware.AuditPlatform(q, zap.NewNop()), func(c *gin.Context) { c.Set(middleware.ContextUserID, uid) })
	r.POST("/api/v1/platform/tenants/:id/prepared", middleware.RequirePlatformTx(p, zap.NewNop()), func(c *gin.Context) {
		c.JSON(200, gin.H{"prepared": true})
		middleware.AfterCommit(c, func() { middleware.AfterCommit(c, func() { c.JSON(502, gin.H{"error": "provider unavailable"}) }) })
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodPost, fmt.Sprintf("/api/v1/platform/tenants/%d/prepared", tid), nil))
	if w.Code != 502 || strings.Contains(w.Body.String(), "prepared") {
		t.Fatalf("prepared success hid post-commit failure %d %s", w.Code, w.Body)
	}
	var payload map[string]any
	if json.Unmarshal(w.Body.Bytes(), &payload) != nil {
		t.Fatalf("multiple JSON documents were published: %s", w.Body)
	}
	var succeeded, failed int
	if err := p.QueryRow(context.Background(), "SELECT count(*) FILTER(WHERE detail->>'phase'='transaction' AND detail->>'status'='200'),count(*) FILTER(WHERE detail->>'phase'='after_commit' AND detail->>'status'='502') FROM audit_logs WHERE tenant_id=$1", tid).Scan(&succeeded, &failed); err != nil || succeeded != 1 || failed != 1 {
		t.Fatalf("lost phase facts succeeded%d failed%d err%v", succeeded, failed, err)
	}
}
