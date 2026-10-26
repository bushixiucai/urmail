package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	archiveStore "github.com/bushixiucai/urmail/internal/archive"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestArchiveContentAuthorizationAuditExportAndRetentionPreview(t *testing.T) {
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
	brand := scalar("INSERT INTO brands(slug,name) VALUES('archive-http','Archive HTTP') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('archive-owner@example.test','hash') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Archive','archive-http','active') RETURNING id", brand)
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, tenant); err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'archive-http.test','active',now(),'token') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'alice','alice@archive-http.test','hash',1024,'active') RETURNING id", tenant, domain)
	root := t.TempDir()
	worker := &archiveStore.Worker{Pool: platform, Spool: filepath.Join(root, "spool"), Archive: filepath.Join(root, "archive")}
	raw := "Message-ID: <http@example.test>\r\nSubject: Search subject\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nsensitive searchable body\r\n--x\r\nContent-Type: text/html\r\nContent-Disposition: attachment; filename=\"evil.html\"\r\n\r\n<script>bad()</script>\r\n--x--\r\n"
	if _, err := archiveStore.Spool(worker.Spool, domainMail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "alice@archive-http.test"}, strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if err := worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	entry := scalar("SELECT id FROM archive_entries WHERE tenant_id=$1", tenant)
	handler := &ArchiveContentHandler{Store: store.New(app.Pool), Root: worker.Archive}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) })
	router.Use(middleware.RequireTenantTx(app, zap.NewNop()))
	router.GET("/archive/search", handler.Search)
	router.GET("/archive/:id/content", handler.Content)
	router.GET("/archive/:id/export", handler.Export)
	router.GET("/archive/:id/attachments/:attachment", handler.Attachment)
	router.POST("/retention/preview", handler.RetentionPreview)
	router.POST("/retention/confirm", handler.RetentionConfirm)
	router.DELETE("/retention", handler.RetentionDisable)
	router.POST("/archive/:id/restore", handler.Restore)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, r)
		return response
	}
	path := "/archive/" + strconv.FormatInt(entry, 10)
	if response := request("GET", path+"/content", ""); response.Code != 403 || strings.Contains(response.Body.String(), "sensitive") {
		t.Fatalf("owner bypassed grant: %d %s", response.Code, response.Body.String())
	}
	grant := func(scope string) int64 {
		t.Helper()
		tx, err := app.UserTx(ctx, tenant, user)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		row, err := store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: user, Scope: scope, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, Reason: "explicit content review", GrantedBy: pgtype.Int8{Int64: user, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	fullGrant := grant("full_content")
	if response := request("GET", path+"/content", ""); response.Code != 200 || !strings.Contains(response.Body.String(), "sensitive searchable body") {
		t.Fatalf("content read failed: %d %s", response.Code, response.Body.String())
	}
	if response := request("GET", path+"/export", ""); response.Code != 403 {
		t.Fatalf("content grant implied export: %d", response.Code)
	}
	response := request("GET", path+"/attachments/0", "")
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/octet-stream" || response.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("unsafe attachment: %d %v", response.Code, response.Header())
	}
	// Failure to commit the mandatory content audit never releases bytes.
	if _, err := platform.Exec(ctx, `CREATE FUNCTION reject_archive_read_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'READ archive.%' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_archive_read_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_archive_read_audit();`); err != nil {
		t.Fatal(err)
	}
	if response := request("GET", path+"/content", ""); response.Code != 500 || strings.Contains(response.Body.String(), "sensitive searchable") {
		t.Fatalf("audit failure released content: %d %s", response.Code, response.Body.String())
	}
	if _, err := platform.Exec(ctx, "DROP TRIGGER reject_archive_read_audit ON audit_logs; DROP FUNCTION reject_archive_read_audit()"); err != nil {
		t.Fatal(err)
	}
	grant("export")
	if response = request("GET", path+"/export", ""); response.Code != 200 || !bytes.Equal(response.Body.Bytes(), []byte(raw)) {
		t.Fatalf("original export mismatch: %d", response.Code)
	}
	if response = request("GET", "/archive/search?q=searchable", ""); response.Code != 200 || !strings.Contains(response.Body.String(), strconv.FormatInt(entry, 10)) {
		t.Fatalf("search failed: %d %s", response.Code, response.Body.String())
	}
	// Composite pagination must retain every row at an identical timestamp.
	if _, err := platform.Exec(ctx, `WITH copies AS (INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,subject,from_addr,received_at,size_bytes,raw_sha256) SELECT tenant_id,mailbox_id,'cursor-'||number,path,'Cursor copy','sender@cursor.test',statement_timestamp(),size_bytes,raw_sha256 FROM archive_entries CROSS JOIN generate_series(1,105) number WHERE id=$1 RETURNING id,tenant_id,raw_sha256) INSERT INTO archive_content(entry_id,tenant_id,body_text,original_sha256) SELECT id,tenant_id,'paginationneedle 中文归档文本',raw_sha256 FROM copies`, entry); err != nil {
		t.Fatal(err)
	}
	type pageResult struct {
		Entries []struct {
			ID int64 `json:"id"`
		} `json:"entries"`
		Next string `json:"next_cursor"`
	}
	first := request("GET", "/archive/search?q=paginationneedle", "")
	var firstPage pageResult
	if json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || first.Code != 200 || len(firstPage.Entries) != 100 || firstPage.Next == "" {
		t.Fatalf("first keyset page invalid: %d %s", first.Code, first.Body.String())
	}
	second := request("GET", "/archive/search?q=paginationneedle&cursor="+url.QueryEscape(firstPage.Next), "")
	var secondPage pageResult
	if json.Unmarshal(second.Body.Bytes(), &secondPage) != nil || second.Code != 200 || len(secondPage.Entries) != 5 || secondPage.Next != "" {
		t.Fatalf("second keyset page invalid: %d %s", second.Code, second.Body.String())
	}
	ids := map[int64]bool{}
	for _, row := range append(firstPage.Entries, secondPage.Entries...) {
		if ids[row.ID] {
			t.Fatal("duplicate at cursor boundary")
		}
		ids[row.ID] = true
	}
	if response := request("GET", "/archive/search?q=changed&cursor="+url.QueryEscape(firstPage.Next), ""); response.Code != 400 {
		t.Fatalf("cursor accepted another filter: %d", response.Code)
	}
	if response := request("GET", "/archive/search?q="+url.QueryEscape("归档")+"&mailbox_id="+strconv.FormatInt(mailbox+1, 10), ""); response.Code != 200 || strings.Contains(response.Body.String(), "Cursor copy") {
		t.Fatalf("mailbox filter leaked rows: %d %s", response.Code, response.Body.String())
	}
	if _, err := platform.Exec(ctx, "UPDATE archive_entries SET received_at=now()-interval '100 days' WHERE id=$1", entry); err != nil {
		t.Fatal(err)
	}
	if denied := request("POST", "/retention/preview", `{"retention_days":30}`); denied.Code != 403 {
		t.Fatalf("content/export scope granted destructive retention: %d", denied.Code)
	}
	grant("retention_manage")
	response = request("POST", "/retention/preview", `{"retention_days":30}`)
	if response.Code != 200 {
		t.Fatalf("preview failed: %d %s", response.Code, response.Body.String())
	}
	var preview struct {
		Preview struct {
			ID             string `json:"id"`
			CandidateCount int64  `json:"candidate_count"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || preview.Preview.CandidateCount != 1 {
		t.Fatalf("wrong preview: %s %v", response.Body.String(), err)
	}
	if response = request("POST", "/retention/confirm", `{"preview_id":"`+preview.Preview.ID+`","confirm":true}`); response.Code != 200 {
		t.Fatalf("confirm failed: %d %s", response.Code, response.Body.String())
	}
	if err := worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if response = request("GET", path+"/content", ""); response.Code != 404 {
		t.Fatalf("soft-deleted original exposed: %d", response.Code)
	}
	var auditCount int
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action LIKE 'READ archive.%'", tenant).Scan(&auditCount); err != nil || auditCount < 4 {
		t.Fatalf("content read not audited: %d %v", auditCount, err)
	}
	if response = request("DELETE", "/retention", ""); response.Code != 200 {
		t.Fatalf("policy could not disable: %d %s", response.Code, response.Body.String())
	}
	if response = request("POST", path+"/restore", ""); response.Code != 200 {
		t.Fatalf("intact copy could not restore: %d %s", response.Code, response.Body.String())
	}
	if response = request("GET", path+"/content", ""); response.Code != 200 {
		t.Fatalf("restored copy unreadable: %d %s", response.Code, response.Body.String())
	}
	tx, err := app.UserTx(ctx, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).RevokeComplianceGrant(ctx, store.RevokeComplianceGrantParams{TenantID: tenant, ID: fullGrant, RevokedBy: pgtype.Int8{Int64: user, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if response = request("GET", path+"/content", ""); response.Code != 403 {
		t.Fatalf("revoked content grant still worked: %d", response.Code)
	}
	if response = request("GET", path+"/export", ""); response.Code != 200 {
		t.Fatalf("separate export grant lost: %d", response.Code)
	}
	if _, err = platform.Exec(ctx, "UPDATE user_tenant_roles SET role='member' WHERE tenant_id=$1 AND user_id=$2", tenant, user); err != nil {
		t.Fatal(err)
	}
	if response = request("GET", path+"/export", ""); response.Code != 403 {
		t.Fatalf("downgraded role retained content: %d", response.Code)
	}

}

func TestLargeArchiveExportAndAttachmentWaitForAuditCommit(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := t.Context()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('archive-large','Archive Large') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('archive-large@example.test','hash') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Large','archive-large','active') RETURNING id", brand)
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, tenant); err != nil {
		t.Fatal(err)
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'archive-large.test','active',now(),'token') RETURNING id", tenant)
	mailbox := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'alice','alice@archive-large.test','hash',1024,'active') RETURNING id", tenant, domain)
	root := t.TempDir()
	worker := &archiveStore.Worker{Pool: platform, Spool: filepath.Join(root, "spool"), Archive: filepath.Join(root, "archive")}
	payload := strings.Repeat("x", 9<<20)
	raw := "Message-ID: <large@archive.test>\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=large.bin\r\n\r\n" + payload
	if _, err := archiveStore.Spool(worker.Spool, domainMail.ArchiveRecipient{TenantID: tenant, MailboxID: mailbox, Address: "alice@archive-large.test"}, strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if err := worker.Once(ctx); err != nil {
		t.Fatal(err)
	}
	entry := scalar("SELECT id FROM archive_entries WHERE tenant_id=$1", tenant)
	for _, scope := range []string{"full_content", "export"} {
		tx, err := app.UserTx(ctx, tenant, user)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: user, Scope: scope, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, Reason: "large original review", GrantedBy: pgtype.Int8{Int64: user, Valid: true}})
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	handler := &ArchiveContentHandler{Store: store.New(app.Pool), Root: worker.Archive}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) })
	logs, observed := observer.New(zap.WarnLevel)
	logger := zap.New(logs)
	router.Use(middleware.Audit(store.New(platform), logger))
	router.Use(middleware.RequireTenantTx(app, logger))
	router.GET("/api/v1/archive/:id/export", handler.Export)
	router.GET("/api/v1/archive/:id/attachments/:attachment", handler.Attachment)
	request := func(suffix string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(ctx, "GET", "/api/v1/archive/"+strconv.FormatInt(entry, 10)+suffix, nil))
		return response
	}
	if response := request("/export"); response.Code != 200 || response.Body.String() != raw {
		t.Fatalf("large original not exported: %d/%d", response.Code, response.Body.Len())
	}
	if response := request("/attachments/0"); response.Code != 200 || response.Body.String() != payload {
		t.Fatalf("large attachment not exported: %d/%d", response.Code, response.Body.Len())
	}
	// The client accepts a prefix and then fails: the committed read remains
	// durable, while an independent audit records incomplete delivery.
	before := failedArchiveStreams(t)
	interrupted := &archiveInterruptedWriter{ResponseRecorder: httptest.NewRecorder(), remaining: 4096}
	router.ServeHTTP(interrupted, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/archive/"+strconv.FormatInt(entry, 10)+"/export", nil))
	if interrupted.Code != http.StatusOK || interrupted.Body.Len() != 4096 {
		t.Fatalf("expected an already-started response prefix, got %d/%d", interrupted.Code, interrupted.Body.Len())
	}
	if got := scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='GET /api/v1/archive/:id/export' AND detail->>'phase'='stream_failed' AND detail->>'status'='502'", tenant); got != 1 {
		t.Fatalf("missing independent incomplete-stream audit: %d", got)
	}
	if got := scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='READ archive.export'", tenant); got != 2 {
		t.Fatalf("authorized read was not committed before interrupted delivery: %d", got)
	}
	if failedArchiveStreams(t) != before+1 || observed.FilterMessage("post-commit response stream incomplete").Len() != 1 {
		t.Fatal("incomplete stream omitted its metric or warning")
	}
	if len(archiveTransferSlots) != 0 {
		t.Fatal("interrupted stream retained its snapshot transfer permit")
	}
	// A deferred failure occurs at COMMIT, after the handler staged its stream.
	if _, err := platform.Exec(ctx, `CREATE FUNCTION fail_large_export_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='READ archive.export' THEN RAISE EXCEPTION 'injected deferred audit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_large_export_commit AFTER INSERT ON audit_logs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_large_export_commit();`); err != nil {
		t.Fatal(err)
	}
	response := request("/export")
	if response.Code != 500 || strings.Contains(response.Body.String(), payload[:1024]) {
		t.Fatalf("failed COMMIT released bytes: %d/%d", response.Code, response.Body.Len())
	}
	if len(archiveTransferSlots) != 0 {
		t.Fatal("failed COMMIT retained transfer permit")
	}
}

// archiveInterruptedWriter models a peer that disconnects after receiving bytes.
type archiveInterruptedWriter struct {
	*httptest.ResponseRecorder
	remaining int
}

func (w *archiveInterruptedWriter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, io.ErrClosedPipe
	}
	n := len(p)
	if n > w.remaining {
		n = w.remaining
	}
	written, err := w.ResponseRecorder.Write(p[:n])
	w.remaining -= written
	if err != nil {
		return written, err
	}
	if n < len(p) {
		return written, io.ErrClosedPipe
	}
	return written, nil
}
func failedArchiveStreams(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "urmail_http_deferred_streams_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "result" && label.GetValue() == "failed" {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
