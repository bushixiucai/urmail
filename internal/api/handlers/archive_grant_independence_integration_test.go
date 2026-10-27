package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	archiveStore "github.com/bushixiucai/urmail/internal/archive"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

// ARC-001 / PD-112: a pre-existing self-grant row (a legacy row from before
// migration 0130) must be refused at EXERCISE time even though the stored row
// itself looks valid: the search/export path re-checks that the exercising
// actor differs from the grantor instead of trusting the row alone. A normal
// two-user grant keeps working end to end.
func TestArchiveExerciseRefusesLegacySelfGrant(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('grant-exercise','GrantExercise') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('grant-exercise-owner@example.test','hash') RETURNING id")
	grantor := scalar("INSERT INTO users(email,password_hash) VALUES('grant-exercise-grantor@example.test','hash') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Exercise','grant-exercise','active') RETURNING id", brand)
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner')", user, tenant, grantor, tenant); err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'grant-exercise.test','active',now(),'token') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'eve','eve@grant-exercise.test','hash',1024,'active') RETURNING id", tenant, domain)
	root := t.TempDir()
	worker := &archiveStore.Worker{Pool: platform, Spool: filepath.Join(root, "spool"), Archive: filepath.Join(root, "archive")}
	raw := "Message-ID: <exercise@example.test>\r\nSubject: Exercise subject\r\nContent-Type: text/plain\r\n\r\nexercise secret body\r\n"
	if _, err := archiveStore.Spool(worker.Spool, domainMail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "eve@grant-exercise.test"}, strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if err := worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	entry := scalar("SELECT id FROM archive_entries WHERE tenant_id=$1", tenant)

	// Simulate a row carried from schema 0129 by disabling BOTH grant triggers
	// for this fixture insert only: the row alone must not confer authority.
	if _, err := platform.Exec(ctx, "ALTER TABLE compliance_grants DISABLE TRIGGER compliance_grants_content_owner_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "ALTER TABLE compliance_grants DISABLE TRIGGER compliance_grants_independence_guard"); err != nil {
		t.Fatal(err)
	}
	_, selfGrantErr := platform.Exec(ctx,
		"INSERT INTO compliance_grants(tenant_id,user_id,scope,expires_at,reason,granted_by,owner_confirmed_at)"+
			" VALUES($1,$2,'full_content',$3,'legacy self authorization',$2,now())",
		tenant, user, time.Now().Add(time.Hour))
	if _, err := platform.Exec(ctx, "ALTER TABLE compliance_grants ENABLE TRIGGER compliance_grants_content_owner_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "ALTER TABLE compliance_grants ENABLE TRIGGER compliance_grants_independence_guard"); err != nil {
		t.Fatal(err)
	}
	if selfGrantErr != nil {
		t.Fatal(selfGrantErr)
	}

	handler := &ArchiveContentHandler{Store: store.New(app.Pool), Root: worker.Archive}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) })
	router.Use(middleware.RequireTenantTx(app, zap.NewNop()))
	router.GET("/archive/search", handler.Search)
	router.GET("/archive/:id/content", handler.Content)
	router.GET("/archive/:id/export", handler.Export)
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
		return response
	}
	path := "/archive/" + strconv.FormatInt(entry, 10)
	// search/content resolve the valid-looking full_content self-grant row and
	// must still refuse it with FORBIDDEN; export has no export scope at all.
	for _, probe := range []struct{ target, mustContain string }{
		{"/archive/search?q=secret", "FORBIDDEN"},
		{path + "/content", "FORBIDDEN"},
		{path + "/export", ""},
	} {
		response := request(probe.target)
		if response.Code != 403 {
			t.Fatalf("legacy self-grant exercised %s: %d %s", probe.target, response.Code, response.Body.String())
		}
		if probe.mustContain != "" && !strings.Contains(response.Body.String(), probe.mustContain) {
			t.Fatalf("legacy self-grant refused without %s at %s: %s", probe.mustContain, probe.target, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "exercise secret body") {
			t.Fatalf("self-grant leaked content at %s", probe.target)
		}
	}

	// The independent grantor authorizes the owner beneficiary: the same
	// requests now succeed end to end.
	gtx, err := app.UserTx(ctx, tenant, grantor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(gtx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{
		TenantID: tenant, UserID: user, Scope: "full_content", Reason: "independent investigation approval",
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, GrantedBy: pgtype.Int8{Int64: grantor, Valid: true},
	}); err != nil {
		_ = gtx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = gtx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	response := request("/archive/search?q=secret")
	if response.Code != 200 || !strings.Contains(response.Body.String(), strconv.FormatInt(entry, 10)) {
		t.Fatalf("two-user grant search failed: %d %s", response.Code, response.Body.String())
	}
	if response = request(path + "/content"); response.Code != 200 || !strings.Contains(response.Body.String(), "exercise secret body") {
		t.Fatalf("two-user grant content read failed: %d %s", response.Code, response.Body.String())
	}
}
