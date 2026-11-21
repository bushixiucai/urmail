package middleware_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/store"
)

func openSQLiteMiddlewarePools(t *testing.T) (*sql.DB, *db.Pool, *db.Pool, int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	sqliteDB, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "mw-sqlite.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqliteDB.Close() })

	repoRoot := filepath.Join("..", "..", "..")
	if err := sqlitedb.Migrate(ctx, sqliteDB, os.DirFS(repoRoot), "migrations_sqlite"); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}

	stmts := []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'default','Default')`,
		`INSERT INTO users(id,email,password_hash,status) VALUES(11,'owner@t1.test','x','active'),(12,'outsider@t2.test','x','active')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(101,1,'T1','t1','active',11),(202,1,'T2','t2','active',12)`,
		`INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(11,101,'owner'),(12,202,'owner')`,
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
	return sqliteDB, appPool, platformPool, 101, 11, 12
}

func TestSQLiteRequireTenantTxGetPostPutDeleteAndAudit(t *testing.T) {
	db.SetIsolationHook(isolation.DBTXScopeHook())
	t.Cleanup(func() { db.SetIsolationHook(nil) })

	sqliteDB, appPool, platformPool, tid, ownerID, outsiderID := openSQLiteMiddlewarePools(t)
	gin.SetMode(gin.TestMode)

	auditWriter := db.Querier(platformPool)
	engine := gin.New()
	engine.Use(middleware.Audit(auditWriter, zap.NewNop()))
	engine.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Test-Actor") {
		case "owner":
			c.Set(middleware.ContextTenantID, tid)
			c.Set(middleware.ContextUserID, ownerID)
		case "outsider":
			c.Set(middleware.ContextTenantID, tid)
			c.Set(middleware.ContextUserID, outsiderID)
		case "no-tenant":
			c.Set(middleware.ContextUserID, ownerID)
		}
		c.Next()
	})
	engine.Use(middleware.RequireTenantTx(appPool, zap.NewNop()))

	var createdDomainID int64
	engine.GET("/domains", func(c *gin.Context) {
		raw, ok := middleware.QueriesFromCtx(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		q := raw.(*store.Queries)
		rows, err := q.ListDomainsByTenant(c.Request.Context(), tid)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"count": len(rows)})
	})
	engine.POST("/domains", func(c *gin.Context) {
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		d, err := q.CreateDomain(c.Request.Context(), store.CreateDomainParams{
			TenantID:          tid,
			Fqdn:              "sqlite-mw.example.test",
			Mode:              "delegated",
			Status:            "pending_dns",
			VerificationToken: "tok-1",
			TlsMode:           "letsencrypt",
			DkimSelector:      "mail",
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		createdDomainID = d.ID
		c.JSON(http.StatusCreated, gin.H{"id": d.ID, "fqdn": d.Fqdn})
	})
	engine.PUT("/domains", func(c *gin.Context) {
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		d, err := q.GetDomainByID(c.Request.Context(), store.GetDomainByIDParams{TenantID: tid, ID: createdDomainID})
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": d.ID, "fqdn": d.Fqdn})
	})
	engine.DELETE("/domains", func(c *gin.Context) {
		raw, _ := middleware.QueriesFromCtx(c)
		q := raw.(*store.Queries)
		if err := q.DeleteDomain(c.Request.Context(), store.DeleteDomainParams{TenantID: tid, ID: createdDomainID}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": createdDomainID})
	})

	doReq := func(method, actor string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, "/domains", nil)
		if actor != "" {
			req.Header.Set("X-Test-Actor", actor)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}

	// GET, POST, PUT, DELETE all pass on SQLite without PG advisory lock failure.
	if w := doReq(http.MethodGet, "owner"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":0`) {
		t.Fatalf("GET /domains failed: %d %s", w.Code, w.Body.String())
	}
	if w := doReq(http.MethodPost, "owner"); w.Code != http.StatusCreated || createdDomainID == 0 {
		t.Fatalf("POST /domains failed (pg_advisory_xact_lock compatibility?): %d %s", w.Code, w.Body.String())
	}
	if w := doReq(http.MethodPut, "owner"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "sqlite-mw.example.test") {
		t.Fatalf("PUT /domains failed: %d %s", w.Code, w.Body.String())
	}
	if w := doReq(http.MethodDelete, "owner"); w.Code != http.StatusOK {
		t.Fatalf("DELETE /domains failed: %d %s", w.Code, w.Body.String())
	}
	if w := doReq(http.MethodGet, "owner"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":0`) {
		t.Fatalf("GET /domains after delete failed: %d %s", w.Code, w.Body.String())
	}

	// Non-member & missing tenant fail closed (403).
	if w := doReq(http.MethodPost, "outsider"); w.Code != http.StatusForbidden {
		t.Fatalf("outsider POST = %d, want 403", w.Code)
	}
	if w := doReq(http.MethodPost, "no-tenant"); w.Code != http.StatusForbidden {
		t.Fatalf("no-tenant POST = %d, want 403", w.Code)
	}

	// Verify transaction audit rows were written on SQLite for POST/PUT/DELETE.
	var auditCount int
	if err := sqliteDB.QueryRow(`SELECT count(*) FROM audit_logs WHERE tenant_id=?`, tid).Scan(&auditCount); err != nil || auditCount < 3 {
		t.Fatalf("expected >=3 tenant audit rows on SQLite, got %d (err=%v)", auditCount, err)
	}
}

func TestSQLiteRequirePlatformTxWithIsolationHook(t *testing.T) {
	db.SetIsolationHook(isolation.DBTXScopeHook())
	t.Cleanup(func() { db.SetIsolationHook(nil) })

	sqliteDB, _, platformPool, tid, ownerID, _ := openSQLiteMiddlewarePools(t)
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.AuditPlatform(db.Querier(platformPool), zap.NewNop()))
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUserID, ownerID)
		c.Next()
	})
	r.POST("/api/v1/platform/tenants/check", middleware.RequirePlatformTx(platformPool, zap.NewNop()), func(c *gin.Context) {
		raw, ok := middleware.QueriesFromCtx(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		q := raw.(*store.Queries)
		tenants, err := q.PlatformListTenants(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"tenant_count": len(tenants), "first_id": tid})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/platform/tenants/check", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RequirePlatformTx on SQLite failed: %d %s", w.Code, w.Body.String())
	}
	var payload struct {
		TenantCount int `json:"tenant_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload.TenantCount != 2 {
		t.Fatalf("unexpected platform response: %s (%v)", w.Body.String(), err)
	}

	var platAudit int
	if err := sqliteDB.QueryRow(`SELECT count(*) FROM audit_logs WHERE target_type='platform' AND actor_user_id=?`, ownerID).Scan(&platAudit); err != nil || platAudit != 1 {
		t.Fatalf("expected 1 platform audit row on SQLite, got %d (err=%v)", platAudit, err)
	}
}
