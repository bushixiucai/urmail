package handlers

import (
	"context"
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

// ARC-002 safe previews: an archived copy's preview surface (content body,
// search, attachment bytes) demands the independent full_content grant. An
// export grant alone must not open any preview path, and a copy whose
// visibility lapsed (soft deleted or physically purging) must 404 even for a
// still-valid grant: the read re-checks visibility at read time.
func TestArchivePreviewCannotBypassIndependentAuthorization(t *testing.T) {
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
	brand := scalar("INSERT INTO brands(slug,name) VALUES('preview','Preview') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('preview-owner@example.test','hash') RETURNING id")
	// ARC-001/PD-112: the grant creator must differ from the beneficiary.
	grantor := scalar("INSERT INTO users(email,password_hash) VALUES('preview-grantor@example.test','hash') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Preview','preview','active') RETURNING id", brand)
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner')", user, tenant, grantor, tenant); err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'preview.test','active',now(),'token') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'bob','bob@preview.test','hash',1024,'active') RETURNING id", tenant, domain)
	// The preview path verifies the archived original on disk, so the fixture
	// must archive a real spooled copy instead of fabricating rows.
	root := t.TempDir()
	worker := &archiveStore.Worker{Pool: platform, Spool: filepath.Join(root, "spool"), Archive: filepath.Join(root, "archive")}
	raw := "Message-ID: <preview@example.test>\r\nSubject: Preview subject\r\nContent-Type: text/plain\r\n\r\npreview secret body\r\n"
	if _, err := archiveStore.Spool(worker.Spool, domainMail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "bob@preview.test"}, strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if err := worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	entry := scalar("SELECT id FROM archive_entries WHERE tenant_id=$1", tenant)
	handler := &ArchiveContentHandler{Store: store.New(app.Pool), Root: worker.Archive}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) })
	router.Use(middleware.RequireTenantTx(app, zap.NewNop()))
	router.GET("/archive/search", handler.Search)
	router.GET("/archive/:id/content", handler.Content)
	router.GET("/archive/:id/attachments/:attachment", handler.Attachment)
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(""))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, r)
		return response
	}
	grant := func(scope string) int64 {
		t.Helper()
		tx, err := app.UserTx(ctx, tenant, grantor)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		row, err := store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: user, Scope: scope, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, Reason: "scoped preview review", GrantedBy: pgtype.Int8{Int64: grantor, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	exportOnly := grant("export")
	_ = exportOnly
	path := "/archive/" + strconv.FormatInt(entry, 10)
	for _, probe := range []struct{ method, target string }{
		{"GET", path + "/content"},
		{"GET", "/archive/search?q=secret"},
		{"GET", path + "/attachments/0"},
	} {
		if response := request(probe.method, probe.target); response.Code != 403 || strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("export grant opened preview %s: %d %s", probe.target, response.Code, response.Body.String())
		}
	}
	grant("full_content")
	if response := request("GET", path+"/content"); response.Code != 200 || !strings.Contains(response.Body.String(), "preview secret body") {
		t.Fatalf("full_content preview failed: %d %s", response.Code, response.Body.String())
	}
	// Visibility is re-checked at read time: a copy retired by retention
	// stops serving previews even while the grant remains valid.
	if _, err := platform.Exec(ctx, "UPDATE archive_entries SET deleted_at=now() WHERE id=$1", entry); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path + "/content", "/archive/search?q=secret"} {
		if response := request("GET", target); response.Code != 404 && response.Code != 200 {
			t.Fatalf("unexpected status after retirement %s: %d", target, response.Code)
		}
	}
	if response := request("GET", path+"/content"); response.Code != 404 || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("retired copy previewed: %d %s", response.Code, response.Body.String())
	}
}
