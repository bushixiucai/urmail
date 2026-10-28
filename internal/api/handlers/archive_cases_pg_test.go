package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	archivestore "github.com/bushixiucai/urmail/internal/archive"
	"github.com/bushixiucai/urmail/internal/archiveevidence"
	"github.com/bushixiucai/urmail/internal/db"
	domainmail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

type caseFixture struct {
	platform, app                                               *db.Pool
	router                                                      *gin.Engine
	tenant, creator, approver, outsider, mailbox, entry, entry2 int64
	root                                                        string
	raw                                                         []byte
}

func newCaseFixture(t *testing.T, bodyBytes int) *caseFixture {
	t.Helper()
	platform, app := authIntegrationDatabase(t)
	f := &caseFixture{platform: platform, app: app}
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(t.Context(), sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('arc-product','ARC') RETURNING id")
	f.tenant = scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'ARC','arc-product','active') RETURNING id", brand)
	users := make([]int64, 0, 4)
	for i := range 4 {
		users = append(users, scalar("INSERT INTO users(email,password_hash) VALUES($1,'hash') RETURNING id", fmt.Sprintf("arc-product-%d@example.test", i)))
	}
	grantor := users[0]
	f.creator = users[1]
	f.approver = users[2]
	f.outsider = users[3]
	for _, u := range users {
		role := "admin"
		if u == grantor {
			role = "owner"
		}
		if _, err := platform.Exec(t.Context(), "INSERT INTO user_tenant_roles(tenant_id,user_id,role) VALUES($1,$2,$3)", f.tenant, u, role); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []int64{f.creator, f.approver, f.outsider} {
		for _, scope := range []string{"full_content", "export"} {
			tx, err := app.UserTx(t.Context(), f.tenant, grantor)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.New(tx).CreateComplianceGrant(t.Context(), store.CreateComplianceGrantParams{TenantID: f.tenant, UserID: u, Scope: scope, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}, Reason: "Independent bounded investigation", GrantedBy: pgtype.Int8{Int64: grantor, Valid: true}})
			if err == nil {
				err = tx.Commit(t.Context())
			}
			_ = tx.Rollback(t.Context())
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'arc-product.test','active',now(),'fixture') RETURNING id", f.tenant)
	f.mailbox = scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'alice','alice@arc-product.test','hash','active') RETURNING id", f.tenant, domain)
	root := t.TempDir()
	f.root = filepath.Join(root, "archive")
	worker := &archivestore.Worker{Pool: platform, Spool: filepath.Join(root, "spool"), Archive: f.root}
	f.raw = []byte("Message-ID: <case-one@example.test>\r\nSubject: Original investigation source\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("x", bodyBytes) + "\r\n")
	for i, raw := range [][]byte{f.raw, []byte("Message-ID: <case-two@example.test>\r\nSubject: Second source\r\n\r\nSecond exact original")} {
		if _, err := archivestore.Spool(worker.Spool, domainmail.ArchiveRecipient{TenantID: f.tenant, MailboxID: f.mailbox, Address: "alice@arc-product.test"}, bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		if err := worker.Once(t.Context()); err != nil {
			t.Fatal(err)
		}
		id := scalar("SELECT id FROM archive_entries WHERE tenant_id=$1 AND message_id=$2", f.tenant, []string{"<case-one@example.test>", "<case-two@example.test>"}[i])
		if i == 0 {
			f.entry = id
		} else {
			f.entry2 = id
		}
	}
	gin.SetMode(gin.TestMode)
	f.router = gin.New()
	f.router.Use(func(c *gin.Context) {
		actor := f.creator
		if v := c.GetHeader("X-Test-Actor"); v != "" {
			actor, _ = strconv.ParseInt(v, 10, 64)
		}
		c.Set(middleware.ContextTenantID, f.tenant)
		c.Set(middleware.ContextUserID, actor)
	})
	f.router.Use(middleware.RequireTenantTx(app, zap.NewNop()))
	RegisterArchiveCaseRoutes(f.router.Group("/api/v1"), app, platform, f.root)
	return f
}
func (f *caseFixture) request(t *testing.T, actor int64, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Actor", strconv.FormatInt(actor, 10))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}
func caseResponse(t *testing.T, w *httptest.ResponseRecorder, code int) map[string]json.RawMessage {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status=%d want=%d body=%s", w.Code, code, w.Body.String())
	}
	result := map[string]json.RawMessage{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *caseFixture) detail(t *testing.T, id int64) (store.ArchiveInvestigations, string) {
	t.Helper()
	d := caseResponse(t, f.request(t, f.creator, "GET", fmt.Sprintf("/api/v1/archive/cases/%d", id), nil), 200)
	var row store.ArchiveInvestigations
	var digest string
	_ = json.Unmarshal(d["case"], &row)
	_ = json.Unmarshal(d["source_digest"], &digest)
	return row, digest
}
func (f *caseFixture) create(t *testing.T) int64 {
	t.Helper()
	input := map[string]any{"title": "Bounded investigation", "purpose": "Review approved archive sources", "expires_at": time.Now().Add(24 * time.Hour).UTC(), "request_key": uuid.NewString()}
	response := caseResponse(t, f.request(t, f.creator, "POST", "/api/v1/archive/cases", input), 201)
	var id int64
	_ = json.Unmarshal(response["id"], &id)
	again := caseResponse(t, f.request(t, f.creator, "POST", "/api/v1/archive/cases", input), 201)
	var same int64
	_ = json.Unmarshal(again["id"], &same)
	if id <= 0 || same != id {
		t.Fatal("case creation is not idempotent")
	}
	return id
}
func (f *caseFixture) approve(t *testing.T, id int64) int64 {
	t.Helper()
	row, digest := f.detail(t, id)
	base := fmt.Sprintf("/api/v1/archive/cases/%d", id)
	result := caseResponse(t, f.request(t, f.creator, "POST", base+"/approvals", map[string]any{"version": row.Version, "digest": digest}), 201)
	var approval int64
	_ = json.Unmarshal(result["id"], &approval)
	if w := f.request(t, f.creator, "POST", fmt.Sprintf("%s/approvals/%d/decision", base, approval), map[string]any{"decision": "approved", "digest": digest, "reason": "Self approval forbidden"}); w.Code != 403 {
		t.Fatalf("self approval=%d %s", w.Code, w.Body.String())
	}
	caseResponse(t, f.request(t, f.approver, "POST", fmt.Sprintf("%s/approvals/%d/decision", base, approval), map[string]any{"decision": "approved", "digest": digest, "reason": "Independent source set reviewed"}), 200)
	return approval
}
func (f *caseFixture) ready(t *testing.T) (int64, int64, string) {
	t.Helper()
	id := f.create(t)
	base := fmt.Sprintf("/api/v1/archive/cases/%d", id)
	row, _ := f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "POST", base+"/members", map[string]any{"user_id": f.approver, "version": row.Version}), 200)
	row, _ = f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "POST", base+"/sources", map[string]any{"entry_id": f.entry, "version": row.Version}), 200)
	approval := f.approve(t, id)
	_, digest := f.detail(t, id)
	prepared := caseResponse(t, f.request(t, f.creator, "POST", fmt.Sprintf("%s/approvals/%d/evidence", base, approval), map[string]any{"digest": digest, "confirm": true}), 201)
	var sha string
	_ = json.Unmarshal(prepared["manifest_sha256"], &sha)
	return id, approval, sha
}

func TestArchiveCaseProductEndToEndPG(t *testing.T) {
	f := newCaseFixture(t, 1024)
	id, approval, sha := f.ready(t)
	base := fmt.Sprintf("/api/v1/archive/cases/%d", id)
	path := fmt.Sprintf("%s/approvals/%d/package", base, approval)
	_, liveDigest := f.detail(t, id)
	repeated := caseResponse(t, f.request(t, f.creator, "POST", fmt.Sprintf("%s/approvals/%d/evidence", base, approval), map[string]any{"digest": liveDigest, "confirm": true}), 200)
	var repeatedSHA string
	_ = json.Unmarshal(repeated["manifest_sha256"], &repeatedSHA)
	if repeatedSHA != sha {
		t.Fatal("repeated execution changed the immutable manifest")
	}
	var exports int64
	if err := f.platform.QueryRow(t.Context(), "SELECT count(*) FROM archive_evidence_exports WHERE tenant_id=$1 AND approval_id=$2", f.tenant, approval).Scan(&exports); err != nil || exports != 1 {
		t.Fatalf("duplicate evidence generation: %d %v", exports, err)
	}

	var visible bool
	var hidden bool
	if err := f.app.QueryRow(t.Context(), "SELECT urmail_archive_export_visible($1,$2,$3),urmail_archive_case_snapshot($1,$4,$3) IS NULL", f.tenant, approval, f.creator, id).Scan(&visible, &hidden); err != nil || visible || !hidden {
		t.Fatalf("definer helpers without context: visible=%t hidden=%t err=%v", visible, hidden, err)
	}
	tx, err := f.app.UserTx(t.Context(), f.tenant, f.outsider)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(t.Context(), "SELECT urmail_archive_export_visible($1,$2,$3),urmail_archive_case_snapshot($1,$4,$3) IS NULL", f.tenant, approval, f.creator, id).Scan(&visible, &hidden); err != nil || visible || !hidden {
		t.Fatalf("borrowed actor helper: visible=%t hidden=%t err=%v", visible, hidden, err)
	}
	_ = tx.Rollback(t.Context())
	response := f.request(t, f.creator, "GET", path, nil)
	if response.Code != 200 {
		t.Fatalf("package: %d %s", response.Code, response.Body.String())
	}
	head := f.request(t, f.creator, "HEAD", path, nil)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("X-Manifest-SHA256") != sha || head.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD representation/auth: %d %s", head.Code, head.Body.String())
	}
	if response.Header().Get("X-Manifest-SHA256") != sha {
		t.Fatal("manifest anchor missing")
	}
	doc, err := archiveevidence.VerifyPackage(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()), sha)
	if err != nil || doc.CaseID != id || doc.ApprovalID != approval || doc.RequesterID == doc.ApproverID {
		t.Fatalf("offline verification %+v %v", doc, err)
	}
	zipReader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range zipReader.File {
		if strings.HasPrefix(file.Name, "messages/") {
			r, e := file.Open()
			if e != nil {
				t.Fatal(e)
			}
			raw, e := io.ReadAll(r)
			_ = r.Close()
			if e != nil || !bytes.Equal(raw, f.raw) {
				t.Fatal("export changed original bytes")
			}
		}
	}
	var otherTenant, otherCase int64
	if err = f.platform.QueryRow(t.Context(), "INSERT INTO tenants(brand_id,name,slug,status) SELECT brand_id,'Other','arc-product-other','active' FROM tenants WHERE id=$1 RETURNING id", f.tenant).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	if err = f.platform.QueryRow(t.Context(), "INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at) VALUES($1,'Other confidential case','other scope',$2,now()+interval '1 day') RETURNING id", otherTenant, f.creator).Scan(&otherCase); err != nil {
		t.Fatal(err)
	}
	if w := f.request(t, f.creator, "GET", fmt.Sprintf("/api/v1/archive/cases/%d", otherCase), nil); w.Code != 404 || strings.Contains(w.Body.String(), "Other confidential") {
		t.Fatalf("cross-tenant case metadata: %d %s", w.Code, w.Body.String())
	}
	// Same-tenant owner/admin with independent grants still cannot see a case
	// without explicit membership, including its title and count.
	list := caseResponse(t, f.request(t, f.outsider, "GET", "/api/v1/archive/cases", nil), 200)
	if string(list["cases"]) != "[]" {
		t.Fatalf("nonmember case metadata leaked: %s", list["cases"])
	}
	if w := f.request(t, f.outsider, "GET", base, nil); w.Code != 404 || strings.Contains(w.Body.String(), "Bounded investigation") {
		t.Fatalf("nonmember read=%d %s", w.Code, w.Body.String())
	}
	if w := f.request(t, f.creator, "GET", fmt.Sprintf("/api/v1/archive/cases/%d/approvals/%d/package", id+1, approval), nil); w.Code != 404 {
		t.Fatal("approval copied across case URL")
	}
	// Removing a member changes the version/snapshot; re-adding that same
	// member never revives the old approval or the prepared manifest.
	row, _ := f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "DELETE", fmt.Sprintf("%s/members/%d", base, f.approver), map[string]any{"version": row.Version}), 200)
	if w := f.request(t, f.creator, "GET", path, nil); w.Code == 200 {
		t.Fatal("member revocation failed to invalidate evidence")
	}
	row, _ = f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "POST", base+"/members", map[string]any{"version": row.Version, "user_id": f.approver}), 200)
	if w := f.request(t, f.creator, "GET", path, nil); w.Code == 200 {
		t.Fatal("re-added member revived an old approval")
	}
	// Source-set drift similarly invalidates an approved target.
	approval = f.approve(t, id)
	row, digest := f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "POST", base+"/sources", map[string]any{"version": row.Version, "entry_id": f.entry2}), 200)
	if w := f.request(t, f.creator, "POST", fmt.Sprintf("%s/approvals/%d/evidence", base, approval), map[string]any{"digest": digest, "confirm": true}); w.Code != 409 {
		t.Fatalf("stale source approval accepted: %d %s", w.Code, w.Body.String())
	}
	row, _ = f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "POST", base+"/close", map[string]any{"version": row.Version}), 200)
	var count int64
	if err = f.app.QueryRow(t.Context(), "SELECT count(*) FROM archive_investigations").Scan(&count); err != nil || count != 0 {
		t.Fatalf("RLS without context=%d %v", count, err)
	}
	if err = f.platform.QueryRow(t.Context(), "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action LIKE '%archive.%'", f.tenant).Scan(&count); err != nil || count < 8 {
		t.Fatalf("missing custody audit: %d %v", count, err)
	}
}

func TestArchiveCaseHTTPStreamStopsAfterMemberRevocationPG(t *testing.T) {
	f := newCaseFixture(t, 8<<20)
	id, approval, _ := f.ready(t)
	server := httptest.NewServer(f.router)
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/api/v1/archive/cases/%d/approvals/%d/package", server.URL, id, approval), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test-Actor", strconv.FormatInt(f.creator, 10))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("stream %d %s", response.StatusCode, raw)
	}
	first := make([]byte, 32<<10)
	n, err := io.ReadFull(response.Body, first)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := f.detail(t, id)
	started := time.Now()
	caseResponse(t, f.request(t, f.creator, "DELETE", fmt.Sprintf("/api/v1/archive/cases/%d/members/%d", id, f.approver), map[string]any{"version": row.Version}), 200)
	if time.Since(started) > 5*time.Second {
		t.Fatal("download blocked revocation on a held case/grant lock")
	}
	rest, readErr := io.ReadAll(response.Body)
	if readErr == nil || int64(n+len(rest)) >= response.ContentLength {
		t.Fatal("revoked transfer completed")
	}
	var recorded bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = f.platform.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM audit_logs WHERE tenant_id=$1 AND action='archive.evidence_transfer_finished' AND detail->>'completed'='false')", f.tenant).Scan(&recorded)
		if err != nil {
			t.Fatal(err)
		}
		if recorded {
			break
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !recorded {
		t.Fatal("interrupted evidence transfer has no custody outcome")
	}
}

func TestRestoredProductEvidenceRequiresFreshIndependentApprovalPG(t *testing.T) {
	f := newCaseFixture(t, 1024)
	id, approval, sha := f.ready(t)
	if err := db.HoldRestoredSideEffects(t.Context(), f.platform.Config().ConnConfig.ConnString(), "archive-product-restore"); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := f.platform.QueryRow(t.Context(), "SELECT state,decision_reason FROM archive_approval_requests WHERE tenant_id=$1 AND id=$2", f.tenant, approval).Scan(&state, &reason); err != nil || state != "expired" || !strings.Contains(reason, "RESTORE_RECONCILIATION_REQUIRED") {
		t.Fatalf("restored approval %s %s %v", state, reason, err)
	}
	var retained bool
	if err := f.platform.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM archive_evidence_exports WHERE tenant_id=$1 AND approval_id=$2 AND manifest_sha256=$3)", f.tenant, approval, sha).Scan(&retained); err != nil || !retained {
		t.Fatal("restore lost the immutable custody fact")
	}
	if w := f.request(t, f.creator, "GET", fmt.Sprintf("/api/v1/archive/cases/%d/approvals/%d/package", id, approval), nil); w.Code == 200 {
		t.Fatal("restore replayed an old evidence approval")
	}
	newer := f.approve(t, id)
	if newer == approval {
		t.Fatal("old approval was reused")
	}
}
