package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestPlatformMutationAuditFailureRollsBackEveryBusinessEffect(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(p.Pool)
	var actor, author, brand, tenant, ticket int64
	for _, s := range []struct {
		sql  string
		dest *int64
	}{{"INSERT INTO users(email,password_hash) VALUES('staff@atomic.test','unused') RETURNING id", &actor}, {"INSERT INTO users(email,password_hash) VALUES('author@atomic.test','unused') RETURNING id", &author}, {"INSERT INTO brands(slug,name) VALUES('atomic','Atomic') RETURNING id", &brand}} {
		if err := p.QueryRow(ctx, s.sql).Scan(s.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Original','atomic','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", actor); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tickets(tenant_id,opened_by,subject) VALUES($1,$2,'Audit rollback') RETURNING id", tenant, author).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	platform := NewPlatformHandler(q)
	platform.Pool = p
	reconcile := &atomicReconcile{}
	platform.Reconcile = reconcile
	support := &SupportHandler{Store: q, Platform: q, PlatformPool: p}
	if _, err := p.Exec(ctx, `CREATE FUNCTION reject_successful_platform_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.detail->>'outcome'='succeeded' THEN RAISE EXCEPTION 'fixture successful audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_successful_platform_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_successful_platform_audit();`); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(middleware.AuditPlatform(q, zap.NewNop()), func(c *gin.Context) { c.Set(middleware.ContextUserID, actor) })
	tx := middleware.RequirePlatformTx(p, zap.NewNop())
	r.PATCH("/api/v1/platform/tenants/:id", tx, platform.UpdateTenant)
	r.POST("/api/v1/platform/tenants/:id/suspend", tx, platform.SuspendTenant)
	r.POST("/api/v1/platform/tenants", tx, platform.CreateTenant)
	r.POST("/api/v1/platform/support/tenants/:id/tickets/:ticket_id/messages", tx, support.PlatformReply)
	cases := []struct{ method, path, body, check string }{
		{http.MethodPatch, fmt.Sprintf("/api/v1/platform/tenants/%d", tenant), `{"name":"Must roll back"}`, "SELECT count(*) FROM tenants WHERE name='Must roll back'"},
		{http.MethodPost, fmt.Sprintf("/api/v1/platform/tenants/%d/suspend", tenant), "", fmt.Sprintf("SELECT count(*) FROM tenants WHERE id=%d AND status='suspended'", tenant)},
		{http.MethodPost, "/api/v1/platform/tenants", `{"name":"Must roll back","slug":"new-atomic","brand_slug":"new-atomic","owner_email":"new@atomic.test","owner_password":"a-strong-atomic-test-password"}`, "SELECT count(*) FROM brands WHERE slug='new-atomic'"},
		{http.MethodPost, fmt.Sprintf("/api/v1/platform/support/tenants/%d/tickets/%d/messages", tenant, ticket), `{"body":"Must roll back"}`, "SELECT count(*) FROM ticket_messages WHERE body='Must roll back'"},
	}
	var err error
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != 500 || !strings.Contains(w.Body.String(), "AUDIT_UNAVAILABLE") {
			t.Fatalf("audit failure response for%s: %d %s", tc.path, w.Code, w.Body)
		}
		var count int
		if err = p.QueryRow(ctx, tc.check).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unaudited mutation committed for%s count%d err%v", tc.path, count, err)
		}
	}
	var outbox, newOwner, failed int
	if err = p.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE kind='support_reply'").Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "SELECT count(*) FROM users WHERE email='new@atomic.test'").Scan(&newOwner); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE actor_user_id=$1 AND detail->>'outcome'='failed' AND detail->>'status'='500'", actor).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if reconcile.calls.Load() != 0 {
		t.Fatal("reconcile ran before audited commit")
	}
	if outbox != 0 || newOwner != 0 || failed != len(cases) {
		t.Fatalf("rollback effects outbox%d newowner%d failedaudit%d", outbox, newOwner, failed)
	}
}

func TestManualTenantCreationRequiresExplicitFiniteFreeGrant(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(p.Pool)
	var actor int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('staff@free.test','unused') RETURNING id").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	var oldBrand, oldTenant int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('existing-free','Existing') RETURNING id").Scan(&oldBrand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Existing','existing-free','active') RETURNING id", oldBrand).Scan(&oldTenant); err != nil {
		t.Fatal(err)
	}
	var oldStatus string
	if err := p.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", oldTenant).Scan(&oldStatus); err != nil || oldStatus != "active" {
		t.Fatal(err)
	}
	h := NewPlatformHandler(q)
	h.Pool = p
	r := gin.New()
	r.Use(middleware.AuditPlatform(q, zap.NewNop()), func(c *gin.Context) { c.Set(middleware.ContextUserID, actor) }, middleware.RequirePlatformTx(p, zap.NewNop()))
	r.POST("/api/v1/platform/tenants", h.CreateTenant)
	create := func(slug, extra string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"name":"Tenant","slug":%q,"brand_slug":"manual","owner_email":%q,"owner_password":"a-strong-manual-test-password"%s}`, slug, slug+"@free.test", extra)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/platform/tenants", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	pending := create("pending", "")
	if pending.Code != 201 {
		t.Fatalf("pending tenant %d %s", pending.Code, pending.Body)
	}
	var response struct {
		Tenant PlatformTenant `json:"tenant"`
	}
	if err := json.Unmarshal(pending.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var pendingStatus string
	if err := p.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", response.Tenant.ID).Scan(&pendingStatus); err != nil || pendingStatus != "pending" || response.Tenant.Status != "pending" {
		t.Fatalf("implicit activation status=%s stored=%s err=%v", response.Tenant.Status, pendingStatus, err)
	}
	until := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Second)
	gift := create("gift", fmt.Sprintf(`,"free_until":%q,"free_reason":"Approved migration assistance"`, until.Format(time.RFC3339)))
	if gift.Code != 201 {
		t.Fatalf("finite gift %d %s", gift.Code, gift.Body)
	}
	if err := json.Unmarshal(gift.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var giftedStatus string
	if err := p.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", response.Tenant.ID).Scan(&giftedStatus); err != nil || giftedStatus != "active" || response.Tenant.Status != "active" {
		t.Fatalf("finite grant status=%s stored=%s err=%v", response.Tenant.Status, giftedStatus, err)
	}
	var audits int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND detail->>'grant_reason'='Approved migration assistance' AND detail->>'free_until'=$2 AND detail->>'phase'='transaction' AND detail->>'outcome'='succeeded'", response.Tenant.ID, until.Format(time.RFC3339)).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("finite gift audit%d err%v", audits, err)
	}
	invalid := create("unbounded", `,"free_until":"infinity","free_reason":"Not allowed"`)
	if invalid.Code != 400 {
		t.Fatalf("infinite free grant accepted %d", invalid.Code)
	}
	if err := p.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", oldTenant).Scan(&oldStatus); err != nil || oldStatus != "active" {
		t.Fatalf("existing tenant status changed %s %v", oldStatus, err)
	}
}

type atomicReconcile struct {
	calls  atomic.Int32
	panics bool
}

func (r *atomicReconcile) Trigger() {
	r.calls.Add(1)
	if r.panics {
		panic("fixture reconcile panic")
	}
}
func TestPlatformAfterCommitPanicPreservesBusinessAndBothAuditPhases(t *testing.T) {
	p, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(p.Pool)
	var actor, brand, tenant int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('after@atomic.test','unused') RETURNING id").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('after-commit','After') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'After','after-commit','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	reconcile := &atomicReconcile{panics: true}
	h := NewPlatformHandler(q)
	h.Pool = p
	h.Reconcile = reconcile
	r := gin.New()
	r.Use(middleware.Recover(zap.NewNop()), middleware.AuditPlatform(q, zap.NewNop()), func(c *gin.Context) { c.Set(middleware.ContextUserID, actor) })
	r.POST("/api/v1/platform/tenants/:id/suspend", middleware.RequirePlatformTx(p, zap.NewNop()), h.SuspendTenant)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/api/v1/platform/tenants/%d/suspend", tenant), nil))
	if w.Code != 500 {
		t.Fatalf("callback panic status%d %s", w.Code, w.Body)
	}
	var status string
	if err := p.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", tenant).Scan(&status); err != nil || status != "suspended" || reconcile.calls.Load() != 1 {
		t.Fatalf("durable business status%s calls%d err%v", status, reconcile.calls.Load(), err)
	}
	var committed, failed int
	if err := p.QueryRow(ctx, "SELECT count(*) FILTER(WHERE detail->>'phase'='transaction' AND detail->>'outcome'='succeeded'),count(*) FILTER(WHERE detail->>'phase'='after_commit' AND detail->>'status'='500') FROM audit_logs WHERE tenant_id=$1 AND actor_user_id=$2", tenant, actor).Scan(&committed, &failed); err != nil || committed != 1 || failed != 1 {
		t.Fatalf("after-commit audit committed%d failed%d err%v", committed, failed, err)
	}
}

func TestGeneralSettingsNeverReturnPrivateProviderMaterial(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(p.Pool)
	var brand, tenant int64
	if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('settings-private','Settings') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Settings','settings-private','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'locale','"en"'),('platform',0,'notification_channel','"enc:v1:private-relay"'),('platform',0,'provider_secret','"private-provider-secret"'),('brand',$1,'theme','{"primary":"#123456"}'),('brand',$1,'notification_channel','"enc:v1:private-brand-relay"')`, brand); err != nil {
		t.Fatal(err)
	}
	h := NewPlatformHandler(q)
	h.Pool = p
	r := gin.New()
	r.GET("/settings", h.Settings)
	r.GET("/brands/:id/settings", h.BrandSettings)
	for _, path := range []string{"/settings", fmt.Sprintf("/brands/%d/settings", brand)} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
		if w.Code != 200 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "notification_channel") || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("private setting returned %d %s", w.Code, w.Body)
		}
	}
	tx, err := a.TenantTx(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var secrets int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM settings WHERE key IN('notification_channel','provider_secret')").Scan(&secrets); err != nil || secrets != 0 {
		t.Fatalf("app SQL bypassed settings privacy boundary count%d err%v", secrets, err)
	}
	rows, err := q.PlatformListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if !publicSettingKey(row.Key) {
			t.Fatalf("platform general query exposed %s", row.Key)
		}
	}
	brandRows, err := q.PlatformListBrandSettings(ctx, brand)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range brandRows {
		if !publicSettingKey(row.Key) {
			t.Fatalf("brand general query exposed %s", row.Key)
		}
	}
}
