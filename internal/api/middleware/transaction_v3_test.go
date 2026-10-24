package middleware_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func transactionV3Fixture(t *testing.T) (*db.Pool, *db.Pool, int64, int64) {
	t.Helper()
	url := integrationURL(t)
	ctx := context.Background()
	migrator, err := db.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	if err = migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenApp(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	platform, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	name := fmt.Sprintf("transaction-%d", time.Now().UnixNano())
	var brand, tid, uid int64
	if err = platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES($1,'Test') RETURNING id", name).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Test',$2,'active') RETURNING id", brand, name).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id", name+"@example.test").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", uid, tid); err != nil {
		t.Fatal(err)
	}
	return app, platform, tid, uid
}
func TestTransactionCommitBeforeResponseAndCallbacks(t *testing.T) {
	app, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", fail), func(t *testing.T) {
			engine := gin.New()
			engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, uid) })
			engine.Use(middleware.RequireTenantTx(app, zap.NewNop()))
			recorder := httptest.NewRecorder()
			callback := false
			key := "locale"
			value := []byte(`"en"`)
			if fail {
				key = "theme"
				value = []byte(`{"primary":"#123456"}`)
			}
			engine.POST("/mutation", func(c *gin.Context) {
				requestCtx, cancel := context.WithCancel(c.Request.Context())
				t.Cleanup(cancel)
				c.Request = c.Request.WithContext(requestCtx)
				raw, _ := middleware.QueriesFromCtx(c)
				q := raw.(*store.Queries)
				if _, err := q.UpsertTenantSetting(c, store.UpsertTenantSettingParams{ScopeID: tid, Key: key, Value: value}); err != nil {
					t.Fatal(err)
				}
				middleware.AfterCommit(c, func() {
					callback = true
					var present bool
					if err := p.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM settings WHERE scope='tenant' AND scope_id=$1 AND key=$2)", tid, key).Scan(&present); err != nil || !present {
						t.Errorf("callback ran before commit: %v", err)
					}
				})
				c.JSON(201, gin.H{"committed": true})
				if recorder.Body.Len() != 0 {
					t.Error("response escaped before commit")
				}
				if fail {
					cancel()
				}
			})
			engine.ServeHTTP(recorder, httptest.NewRequestWithContext(ctx, http.MethodPost, "/mutation", nil))
			var present bool
			if err := p.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM settings WHERE scope='tenant' AND scope_id=$1 AND key=$2)", tid, key).Scan(&present); err != nil {
				t.Fatal(err)
			}
			if fail {
				if recorder.Code != 500 || callback || present || strings.Contains(recorder.Body.String(), "committed") {
					t.Fatalf("failed commit reported success: status=%d callback=%v present=%v body=%s", recorder.Code, callback, present, recorder.Body.String())
				}
			} else if recorder.Code != 201 || !callback || !present {
				t.Fatalf("success failed: %d %v %v", recorder.Code, callback, present)
			}
		})
	}
}
func TestTenantAuditSharesTransactionAndIsReadable(t *testing.T) {
	app, p, tid, uid := transactionV3Fixture(t)
	ctx := context.Background()
	writer := store.New(p.Pool)
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, uid) })
	engine.Use(middleware.RequireTenantTx(app, zap.NewNop()), middleware.Audit(writer, zap.NewNop()))
	engine.POST("/success", func(c *gin.Context) { c.JSON(201, gin.H{"ok": true}) })
	engine.POST("/rejected", func(c *gin.Context) { c.JSON(409, gin.H{"error": gin.H{"code": "CONFLICT"}}) })
	engine.GET("/audit", handlers.NewAuditHandler(store.New(app.Pool)).List)
	for _, path := range []string{"/success", "/rejected"} {
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodPost, path, nil))
	}
	var count int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='POST /success'", tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit success rows=%d err=%v", count, err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='POST /rejected'", tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected audit=%d err=%v", count, err)
	}
	var successStatus, failedStatus int
	if err := p.QueryRow(ctx, "SELECT (detail->>'status')::int FROM audit_logs WHERE tenant_id=$1 AND action='POST /success'", tid).Scan(&successStatus); err != nil || successStatus != 201 {
		t.Fatalf("success status=%d err=%v", successStatus, err)
	}
	if err := p.QueryRow(ctx, "SELECT (detail->>'status')::int FROM audit_logs WHERE tenant_id=$1 AND action='POST /rejected'", tid).Scan(&failedStatus); err != nil || failedStatus != 409 {
		t.Fatalf("failure status=%d err=%v", failedStatus, err)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequestWithContext(ctx, http.MethodGet, "/audit", nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "POST /success") {
		t.Fatalf("audit not readable: %d %s", recorder.Code, recorder.Body.String())
	}
}
