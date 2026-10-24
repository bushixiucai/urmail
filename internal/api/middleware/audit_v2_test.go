package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestTenantAuditSurvivesSQLAndDeferredCommitFailure(t *testing.T) {
	app, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	_, err := p.Exec(ctx, `CREATE FUNCTION audit_fixture_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key='theme' THEN RAISE EXCEPTION 'fixture deferred failure'; END IF; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER audit_fixture_commit_failure AFTER INSERT ON settings DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION audit_fixture_commit_failure();`)
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, uid) })
	engine.Use(middleware.Audit(store.New(p.Pool), zap.NewNop()), middleware.RequireTenantTx(app, zap.NewNop()))
	engine.POST("/sql-error", func(c *gin.Context) {
		raw, _ := middleware.QueriesFromCtx(c)
		_, sqlErr := raw.(*store.Queries).UpsertTenantSetting(c, store.UpsertTenantSettingParams{ScopeID: tid, Key: "locale", Value: []byte("not-json")})
		if sqlErr == nil {
			t.Error("expected SQL failure")
		}
		c.JSON(500, gin.H{"error": gin.H{"code": "DB_ERROR"}})
	})
	engine.POST("/commit-error", func(c *gin.Context) {
		raw, _ := middleware.QueriesFromCtx(c)
		if _, insertErr := raw.(*store.Queries).UpsertTenantSetting(c, store.UpsertTenantSettingParams{ScopeID: tid, Key: "theme", Value: []byte(`{"primary":"#123456"}`)}); insertErr != nil {
			t.Error(insertErr)
		}
		c.JSON(201, gin.H{"ok": true})
	})
	for _, path := range []string{"/sql-error", "/commit-error"} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, path, nil))
		if response.Code != 500 {
			t.Fatalf("%s status %d: %s", path, response.Code, response.Body)
		}
		var count int
		if err = p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action=$2 AND detail->>'status'='500' AND detail->>'outcome'='failed'", tid, "POST "+path).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s audit count=%d err=%v", path, count, err)
		}
	}
	var count int
	if err = p.QueryRow(ctx, "SELECT count(*) FROM settings WHERE scope='tenant' AND scope_id=$1 AND key='theme'", tid).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed commit leaked row: %d %v", count, err)
	}
}

func TestTenantAuditIncludesCSRFFailureAndNeverTrustsUnauthenticatedTenant(t *testing.T) {
	app, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	engine := gin.New()
	engine.Use(middleware.Audit(store.New(p.Pool), zap.NewNop()))
	engine.POST("/unauthenticated", func(c *gin.Context) { c.AbortWithStatus(401) })
	secured := engine.Group("/secured")
	secured.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, tid)
		c.Set(middleware.ContextUserID, uid)
		c.Set(middleware.ContextSession, store.Sessions{CsrfToken: "expected-csrf"})
	})
	secured.Use(middleware.RequireTenantTx(app, zap.NewNop()), middleware.RequireCSRF())
	secured.POST("/mutation", func(c *gin.Context) { c.Status(204) })
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/unauthenticated?tenant_id=999&user_id=999", nil))
	var tenant, actor pgtype.Int8
	if err := p.QueryRow(ctx, "SELECT tenant_id,actor_user_id FROM audit_logs WHERE action='POST /unauthenticated'").Scan(&tenant, &actor); err != nil || tenant.Valid || actor.Valid {
		t.Fatalf("trusted attacker identity: %v %v %v", tenant, actor, err)
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/secured/mutation", nil))
	if response.Code != 403 {
		t.Fatalf("missing csrf=%d", response.Code)
	}
	var count int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='POST /secured/mutation' AND detail->>'status'='403'", tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing csrf audit: %d %v", count, err)
	}
}

func TestPlatformAuditResolvesResourceTenantAndKeepsBrandsGlobal(t *testing.T) {
	_, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	var orderID int64
	if err := p.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency) VALUES($1,'audit-order','new','pending',100,'CNY') RETURNING id", tid).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, uid) })
	engine.Use(middleware.AuditPlatform(store.New(p.Pool), zap.NewNop()))
	engine.POST("/api/v1/platform/orders/:id/paid", func(c *gin.Context) { c.Status(409) })
	engine.PATCH("/api/v1/platform/brands/:id/settings", func(c *gin.Context) { c.Status(200) })
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/platform/orders/"+intString(orderID)+"/paid", nil))
	var got pgtype.Int8
	if err := p.QueryRow(ctx, "SELECT tenant_id FROM audit_logs WHERE action='POST /api/v1/platform/orders/:id/paid'").Scan(&got); err != nil || !got.Valid || got.Int64 != tid {
		t.Fatalf("wrong order tenant: %v %v", got, err)
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPatch, "/api/v1/platform/brands/"+intString(tid)+"/settings", nil))
	if err := p.QueryRow(ctx, "SELECT tenant_id FROM audit_logs WHERE action='PATCH /api/v1/platform/brands/:id/settings'").Scan(&got); err != nil || got.Valid {
		t.Fatalf("brand treated as tenant: %v %v", got, err)
	}
}

func TestAuditCompositeCursorDoesNotSkipIDTimeInversion(t *testing.T) {
	app, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	if _, err := p.Exec(ctx, "INSERT INTO audit_logs(tenant_id,action,created_at) VALUES($1,'newer-time',now()),($1,'older-time',now()-interval '1 hour')", tid); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, uid) })
	engine.Use(middleware.RequireTenantTx(app, zap.NewNop()))
	engine.GET("/audit", handlers.NewAuditHandler(store.New(app.Pool)).List)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/audit?limit=1", nil))
	var page struct {
		Logs []store.AuditLogs `json:"logs"`
		Next string            `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Logs) != 1 || page.Logs[0].Action != "newer-time" || page.Next == "" {
		t.Fatalf("first page: %s %v", response.Body, err)
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/audit?limit=1&cursor="+page.Next, nil))
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || len(page.Logs) != 1 || page.Logs[0].Action != "older-time" {
		t.Fatalf("second page skipped inverted id: %s %v", response.Body, err)
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/audit?limit=1&action=different&cursor="+page.Next, nil))
	if response.Code != 400 {
		t.Fatalf("cursor not bound to filters: %d", response.Code)
	}
}

func intString(id int64) string { return fmt.Sprint(id) }

func TestContentGrantOwnerOnlyAndLegacyConfirmation(t *testing.T) {
	app, p, tid, owner := transactionV3Fixture(t)
	ctx := context.Background()
	var admin int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('content-admin@example.test','x') RETURNING id").Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'admin')", admin, tid); err != nil {
		t.Fatal(err)
	}
	attempt := func(actor int64, reason string, expires pgtype.Timestamptz) error {
		tx, err := app.UserTx(ctx, tid, actor)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tid, UserID: admin, Scope: "full_content", ExpiresAt: expires, Reason: reason, GrantedBy: pgtype.Int8{Int64: actor, Valid: true}})
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	future := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	if err := attempt(admin, "self authorization", future); err == nil {
		t.Fatal("admin self-authorized content")
	}
	if err := attempt(owner, "", future); err == nil {
		t.Fatal("empty reason accepted")
	}
	if err := attempt(owner, "investigation", pgtype.Timestamptz{}); err == nil {
		t.Fatal("unlimited expiry accepted")
	}
	if err := attempt(owner, "investigation", future); err != nil {
		t.Fatal(err)
	}
	tx, err := app.UserTx(ctx, tid, admin)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.New(tx).GetComplianceGrant(ctx, store.GetComplianceGrantParams{TenantID: tid, UserID: admin, RequiredScope: "full_content"})
	if err != nil {
		t.Fatal(err)
	}
	if !grant.OwnerConfirmedAt.Valid {
		t.Fatal("owner confirmation not recorded")
	}
	if _, err = store.New(tx).GetComplianceGrant(ctx, store.GetComplianceGrantParams{TenantID: tid, UserID: admin, RequiredScope: "export"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("content implied export: %v", err)
	}
	_ = tx.Rollback(ctx)
	// Simulate a row carried from schema 34. No legacy row is silently blessed.
	if _, err = p.Exec(ctx, "ALTER TABLE compliance_grants DISABLE TRIGGER compliance_grants_content_owner_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "UPDATE compliance_grants SET owner_confirmed_at=NULL WHERE tenant_id=$1", tid); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "ALTER TABLE compliance_grants ENABLE TRIGGER compliance_grants_content_owner_guard"); err != nil {
		t.Fatal(err)
	}
	tx, err = app.UserTx(ctx, tid, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = store.New(tx).GetComplianceGrant(ctx, store.GetComplianceGrantParams{TenantID: tid, UserID: admin, RequiredScope: "full_content"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("legacy unconfirmed grant allowed: %v", err)
	}
}

func TestAuditKeepsBusinessCommitAndAfterCommitFailure(t *testing.T) {
	app, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, uid) })
	engine.Use(middleware.Audit(store.New(p.Pool), zap.NewNop()), middleware.RequireTenantTx(app, zap.NewNop()))
	engine.POST("/provider", func(c *gin.Context) {
		raw, _ := middleware.QueriesFromCtx(c)
		if _, err := raw.(*store.Queries).UpsertTenantSetting(c, store.UpsertTenantSettingParams{ScopeID: tid, Key: "locale", Value: []byte(`"en"`)}); err != nil {
			t.Fatal(err)
		}
		middleware.AfterCommit(c, func() { c.JSON(502, gin.H{"error": gin.H{"code": "PAYMENT_PROVIDER_ERROR"}}) })
	})
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/provider", nil))
	if response.Code != 502 {
		t.Fatalf("provider final status=%d", response.Code)
	}
	var committed, failed int
	if err := p.QueryRow(ctx, "SELECT count(*) FILTER (WHERE detail->>'phase'='transaction' AND detail->>'outcome'='succeeded'),count(*) FILTER (WHERE detail->>'phase'='after_commit' AND detail->>'status'='502' AND detail->>'outcome'='failed') FROM audit_logs WHERE tenant_id=$1 AND action='POST /provider'", tid).Scan(&committed, &failed); err != nil || committed != 1 || failed != 1 {
		t.Fatalf("lost commit/response facts: committed=%d failed=%d err=%v", committed, failed, err)
	}
}

func TestComplianceGrantManagementAPIAndMetadataExport(t *testing.T) {
	app, p, tid, owner := transactionV3Fixture(t)
	ctx := context.Background()
	var admin int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('grant-api-admin@example.test','x') RETURNING id").Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'admin')", admin, tid); err != nil {
		t.Fatal(err)
	}
	actor := owner
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, actor) })
	engine.Use(middleware.Audit(store.New(p.Pool), zap.NewNop()), middleware.RequireTenantTx(app, zap.NewNop()))
	h := &handlers.ComplianceGrantHandler{Store: store.New(app.Pool)}
	engine.GET("/grants", h.List)
	engine.POST("/grants", h.Create)
	engine.POST("/grants/:id/revoke", h.Revoke)
	engine.GET("/audit/export", handlers.NewAuditHandler(store.New(app.Pool)).Export)
	body := fmt.Sprintf(`{"user_id":%d,"scope":"export","reason":"case review","expires_at":%q}`, admin, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	actor = admin
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/grants", strings.NewReader(body)))
	if response.Code != 403 {
		t.Fatalf("admin grant status=%d %s", response.Code, response.Body)
	}
	actor = owner
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/grants", strings.NewReader(body)))
	var grant struct {
		Grant store.ComplianceGrants `json:"grant"`
	}
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &grant) != nil || !grant.Grant.OwnerConfirmedAt.Valid {
		t.Fatalf("owner grant=%d %s", response.Code, response.Body)
	}
	for range 2 {
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/grants/"+intString(grant.Grant.ID)+"/revoke", nil))
		if response.Code != 200 {
			t.Fatalf("idempotent revoke=%d %s", response.Code, response.Body)
		}
	}
	if _, err := p.Exec(ctx, "INSERT INTO audit_logs(tenant_id,action,target_type,target_id) VALUES($1,'=1+1','fixture','object')", tid); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/audit/export?target_type=fixture", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "'=1+1") || !strings.Contains(response.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("export unsafe or missing: %d %s", response.Code, response.Body)
	}
	var count int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='GET /audit/export'", tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("export not audited before release: %d %v", count, err)
	}
	if _, err := p.Exec(ctx, `CREATE FUNCTION audit_fixture_export_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='GET /audit/export' THEN RAISE EXCEPTION 'fixture audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER audit_fixture_export_failure BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION audit_fixture_export_failure();`); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/audit/export?target_type=fixture", nil))
	if response.Code != 503 || strings.Contains(response.Body.String(), "=1+1") {
		t.Fatalf("content released without audit: %d %s", response.Code, response.Body)
	}
}

func TestTenantAuditRecordsPanicAsFailureWithoutLeakingPanicMessage(t *testing.T) {
	app, p, tid, uid := transactionV3Fixture(t)
	engine := gin.New()
	engine.Use(middleware.Recover(zap.NewNop()))
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, uid) })
	engine.Use(middleware.Audit(store.New(p.Pool), zap.NewNop()), middleware.RequireTenantTx(app, zap.NewNop()))
	engine.POST("/panic", func(_ *gin.Context) { panic("private mail must not appear in audit") })
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/panic", nil))
	if response.Code != 500 {
		t.Fatalf("panic HTTP %d", response.Code)
	}
	var detail string
	if err := p.QueryRow(context.Background(), "SELECT detail::text FROM audit_logs WHERE tenant_id=$1 AND action='POST /panic'", tid).Scan(&detail); err != nil || !strings.Contains(detail, "500") || strings.Contains(detail, "private mail") {
		t.Fatalf("panic audit=%s err=%v", detail, err)
	}
}
