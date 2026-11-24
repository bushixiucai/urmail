package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/store"
)

func setupS3AuthSQLiteEnv(t *testing.T) (*sql.DB, *db.Pool, *db.Pool, string) {
	t.Helper()
	ctx := context.Background()
	sqliteDB, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "s3-auth-e2e.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqliteDB.Close() })

	if err := sqlitedb.Migrate(ctx, sqliteDB, os.DirFS(filepath.Join("..", "..")), "migrations_sqlite"); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}

	const password = "S3-EndToEnd-Password-123!"
	hash, err := panelpassword.Hash(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	stmts := []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'default','Default')`,
		// User 1: platform admin with NO tenant memberships (tests nullable session tenant_id).
		// User 2: multi-tenant owner across tenant 10 & tenant 20.
		fmt.Sprintf(`INSERT INTO users(id,email,password_hash,status,locale) VALUES(1,'admin@s3.test','%s','active','zh-CN'),(2,'owner@s3.test','%s','active','zh-CN')`, hash, hash),
		`INSERT INTO platform_admins(user_id) VALUES(1)`,
		`INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(10,1,'TenTen','ten10','active',2),(20,1,'TenTwenty','ten20','active',2)`,
		`INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(2,10,'owner'),(2,20,'owner')`,
	}
	for _, stmt := range stmts {
		if _, err := sqliteDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	appPool, err := db.NewSQLitePool(sqliteDB, isolation.ScopeTenant)
	if err != nil {
		t.Fatalf("NewSQLitePool app: %v", err)
	}
	platformPool, err := db.NewSQLitePool(sqliteDB, isolation.ScopePlatform)
	if err != nil {
		t.Fatalf("NewSQLitePool platform: %v", err)
	}
	return sqliteDB, appPool, platformPool, password
}

func TestSQLiteS3AuthRequireAuthTenantTxAndPlatformTxEndToEnd(t *testing.T) {
	db.SetIsolationHook(isolation.DBTXScopeHook())
	t.Cleanup(func() { db.SetIsolationHook(isolation.DBTXScopeHook()) })

	sqliteDB, appPool, platformPool, password := setupS3AuthSQLiteEnv(t)
	gin.SetMode(gin.TestMode)

	// Wire AuthHandler, RequireAuth, RequirePlatformAdmin, and Audit directly
	// through db.Querier (702-method *isolation.ScopedQuerier decorator).
	authQuerier := db.Querier(platformPool)
	authHandler := handlers.NewAuthHandler(authQuerier)

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/auth/login", authHandler.Login)

	userLevel := v1.Group("")
	userLevel.Use(middleware.RequireAuth(authQuerier))
	userLevel.Use(middleware.RequireCSRF())
	userLevel.GET("/auth/me", authHandler.Me)
	userLevel.GET("/auth/sessions", authHandler.ListSessions)
	userLevel.POST("/auth/switch-tenant", authHandler.SwitchTenant)
	userLevel.POST("/auth/logout", authHandler.Logout)

	tenantGroup := v1.Group("/tenant")
	tenantGroup.Use(middleware.Audit(authQuerier, zap.NewNop()))
	tenantGroup.Use(middleware.RequireAuth(authQuerier))
	tenantGroup.Use(middleware.RequireCSRF())
	tenantGroup.Use(middleware.RequireTenantTx(appPool, zap.NewNop()))
	tenantGroup.GET("/domains", func(c *gin.Context) {
		tid, _ := middleware.GetTenantID(c)
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		domains, err := q.ListDomainsByTenant(c.Request.Context(), tid)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"tenant_id": tid, "domains": domains})
	})
	tenantGroup.POST("/domains", func(c *gin.Context) {
		tid, _ := middleware.GetTenantID(c)
		var req struct {
			Fqdn string `json:"fqdn"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		d, err := q.CreateDomain(c.Request.Context(), store.CreateDomainParams{
			TenantID:          tid,
			Fqdn:              req.Fqdn,
			Mode:              "delegated",
			Status:            "pending_dns",
			VerificationToken: "tok-" + req.Fqdn,
			TlsMode:           "letsencrypt",
			DkimSelector:      "mail",
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"id": d.ID, "tenant_id": d.TenantID, "fqdn": d.Fqdn})
	})
	tenantGroup.PUT("/domains/:id", func(c *gin.Context) {
		tid, _ := middleware.GetTenantID(c)
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		domains, err := q.ListDomainsByTenant(c.Request.Context(), tid)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"tenant_id": tid, "count": len(domains)})
	})
	tenantGroup.DELETE("/domains/:id", func(c *gin.Context) {
		tid, _ := middleware.GetTenantID(c)
		var id int64
		_, _ = fmt.Sscanf(c.Param("id"), "%d", &id)
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		if err := q.DeleteDomain(c.Request.Context(), store.DeleteDomainParams{TenantID: tid, ID: id}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": id})
	})

	platformGroup := v1.Group("/platform")
	platformGroup.Use(middleware.AuditPlatform(authQuerier, zap.NewNop()))
	platformGroup.Use(middleware.RequireAuth(authQuerier))
	platformGroup.Use(middleware.RequirePlatformAdmin(authQuerier))
	platformGroup.Use(middleware.RequireCSRF())
	platformGroup.POST("/tenants/summary", middleware.RequirePlatformTx(platformPool, zap.NewNop()), func(c *gin.Context) {
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		rows, err := q.PlatformListTenants(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"tenants": len(rows)})
	})

	call := func(method, path, token, csrf, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. Tenantless platform-admin login -> nullable TenantID session works end-to-end.
	w := call(http.MethodPost, "/api/v1/auth/login", "", "", fmt.Sprintf(`{"email":"admin@s3.test","password":%q}`, password))
	if w.Code != http.StatusOK {
		t.Fatalf("tenantless login failed: %d %s", w.Code, w.Body.String())
	}
	var adminLogin handlers.LoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &adminLogin); err != nil {
		t.Fatalf("decode admin login: %v", err)
	}
	if adminLogin.CurrentTenantID != nil || !adminLogin.IsAdmin || adminLogin.Token == "" {
		t.Fatalf("unexpected admin login payload: %+v", adminLogin)
	}

	// RequireAuth on GET /auth/me & GET /auth/sessions works for tenantless session.
	if w := call(http.MethodGet, "/api/v1/auth/me", adminLogin.Token, "", ""); w.Code != http.StatusOK {
		t.Fatalf("tenantless /auth/me failed: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/auth/sessions", adminLogin.Token, "", ""); w.Code != http.StatusOK {
		t.Fatalf("tenantless /auth/sessions failed: %d %s", w.Code, w.Body.String())
	}
	// RequireTenantTx rejects tenantless session with 403 NO_TENANT_CONTEXT.
	if w := call(http.MethodGet, "/api/v1/tenant/domains", adminLogin.Token, "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("tenantless /tenant/domains = %d %s, want 403", w.Code, w.Body.String())
	}
	// RequirePlatformTx succeeds for platform admin.
	if w := call(http.MethodPost, "/api/v1/platform/tenants/summary", adminLogin.Token, adminLogin.CSRFToken, "{}"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tenants":2`) {
		t.Fatalf("RequirePlatformTx failed: %d %s", w.Code, w.Body.String())
	}

	// 2. Multi-tenant owner login -> defaults to first tenant (TenTen = 10).
	w = call(http.MethodPost, "/api/v1/auth/login", "", "", fmt.Sprintf(`{"email":"owner@s3.test","password":%q}`, password))
	if w.Code != http.StatusOK {
		t.Fatalf("owner login failed: %d %s", w.Code, w.Body.String())
	}
	var ownerLogin handlers.LoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &ownerLogin); err != nil {
		t.Fatalf("decode owner login: %v", err)
	}
	if ownerLogin.CurrentTenantID == nil || *ownerLogin.CurrentTenantID != 10 {
		t.Fatalf("expected default tenant 10, got %+v", ownerLogin.CurrentTenantID)
	}

	// RequirePlatformAdmin blocks non-admin tenant owner.
	if w := call(http.MethodPost, "/api/v1/platform/tenants/summary", ownerLogin.Token, ownerLogin.CSRFToken, "{}"); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin platform access = %d, want 403", w.Code)
	}

	// RequireTenantTx GET, POST, PUT, DELETE all pass on SQLite with DBTXScopeHook ON.
	if w := call(http.MethodGet, "/api/v1/tenant/domains", ownerLogin.Token, "", ""); w.Code != http.StatusOK {
		t.Fatalf("GET /tenant/domains failed: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodPost, "/api/v1/tenant/domains", ownerLogin.Token, ownerLogin.CSRFToken, `{"fqdn":"alpha.s3.test"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /tenant/domains failed: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID       int64 `json:"id"`
		TenantID int64 `json:"tenant_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.TenantID != 10 || created.ID == 0 {
		t.Fatalf("unexpected POST /tenant/domains payload: %s (%v)", w.Body.String(), err)
	}
	if w := call(http.MethodPut, fmt.Sprintf("/api/v1/tenant/domains/%d", created.ID), ownerLogin.Token, ownerLogin.CSRFToken, "{}"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatalf("PUT /tenant/domains failed: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodDelete, fmt.Sprintf("/api/v1/tenant/domains/%d", created.ID), ownerLogin.Token, ownerLogin.CSRFToken, ""); w.Code != http.StatusOK {
		t.Fatalf("DELETE /tenant/domains failed: %d %s", w.Code, w.Body.String())
	}

	// SwitchTenant moves session from tenant 10 -> 20 via ScopedQuerier.UpdateSessionTenant.
	if w := call(http.MethodPost, "/api/v1/auth/switch-tenant", ownerLogin.Token, ownerLogin.CSRFToken, `{"tenant_id":20}`); w.Code != http.StatusOK {
		t.Fatalf("switch-tenant failed: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/tenant/domains", ownerLogin.Token, "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tenant_id":20`) {
		t.Fatalf("expected switched tenant 20 on /tenant/domains: %d %s", w.Code, w.Body.String())
	}

	// Logout revokes the session via ScopedQuerier.DeleteSession; subsequent RequireAuth returns 401.
	if w := call(http.MethodPost, "/api/v1/auth/logout", ownerLogin.Token, ownerLogin.CSRFToken, ""); w.Code != http.StatusOK {
		t.Fatalf("logout failed: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/auth/me", ownerLogin.Token, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session still authorized: %d %s", w.Code, w.Body.String())
	}

	// 3. Direct fail-closed assertions on ScopedQuerier & db.Queries when scope is missing or cross-tenant.
	ctxBg := context.Background()
	tenant10Ctx := isolation.WithScope(ctxBg, isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: 10, ActorID: 2})
	tenantQuerier := db.Querier(appPool)
	directQueries := db.Queries(appPool)

	if _, err := tenantQuerier.GetSessionByID(ctxBg, "any"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("unscoped ScopedQuerier.GetSessionByID err=%v", err)
	}
	if _, err := directQueries.GetSessionByID(ctxBg, "any"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("unscoped db.Queries.GetSessionByID err=%v", err)
	}
	if _, err := tenantQuerier.ListDomainsByTenant(tenant10Ctx, 20); !errors.Is(err, isolation.ErrCrossTenantAccess) {
		t.Fatalf("cross-tenant ScopedQuerier.ListDomainsByTenant err=%v", err)
	}
	if _, err := tenantQuerier.CreateSession(tenant10Ctx, store.CreateSessionParams{
		ID: "bad", UserID: 2, TenantID: pgtype.Int8{Int64: 10, Valid: true},
	}); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("tenant ctx calling CreateSession err=%v", err)
	}
	if _, err := directQueries.ListDomainsByTenant(ctxBg, 10); !errors.Is(err, isolation.ErrMissingTenantScope) {
		t.Fatalf("unscoped db.Queries.ListDomainsByTenant err=%v", err)
	}

	var auditRows int
	if err := sqliteDB.QueryRowContext(t.Context(), `SELECT count(*) FROM audit_logs`).Scan(&auditRows); err != nil || auditRows < 4 {
		t.Fatalf("expected >=4 committed audit_logs rows across tenant/platform calls, got %d (err=%v)", auditRows, err)
	}
}
