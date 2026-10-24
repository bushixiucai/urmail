package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestLegacyBillingCalendarRequiresOnceOnlyAuditedPlatformDecision(t *testing.T) {
	p, app := authIntegrationDatabase(t)
	ctx := t.Context()
	q := store.New(p.Pool)
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := p.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := p.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('calendar-admin','Calendar') RETURNING id")
	staff := scalar("INSERT INTO users(email,password_hash) VALUES('calendar-staff@example.test','unused') RETURNING id")
	outsider := scalar("INSERT INTO users(email,password_hash) VALUES('calendar-owner@example.test','unused') RETURNING id")
	exec("INSERT INTO platform_admins(user_id) VALUES($1)", staff)
	exec("INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('calendar-legacy','{}',100,'CNY',1073741824,5,2,100)")
	makeTenant := func(name string, paid bool) int64 {
		t.Helper()
		tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,$2,$2,'active') RETURNING id", brand, name)
		if paid {
			exec("UPDATE subscriptions SET plan_id='calendar-legacy',period_start='2024-01-31T01:00:00Z',period_end='2024-02-29T01:00:00Z',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='calendar-legacy') WHERE tenant_id=$1", tenant)
			order := scalar("INSERT INTO orders(tenant_id,order_no,status,amount_cents,currency,plan_code,kind,billing_cycle) VALUES($1,$2,'paid',100,'CNY','calendar-legacy','new','monthly') RETURNING id", tenant, "CALENDAR-LEGACY-"+name)
			exec("INSERT INTO subscription_terms(order_id,tenant_id,period_start,period_end,billing_period_start,plan_snapshot,base_amount_cents,currency,billing_cycle) SELECT $1,$2,'2024-01-31T01:00:00Z','2024-02-29T01:00:00Z','2024-01-31T01:00:00Z',to_jsonb(p),100,'CNY','monthly' FROM plans p WHERE code='calendar-legacy'", order, tenant)
		}
		return tenant
	}
	legacy := makeTenant("legacy-one", true)
	concurrent := makeTenant("legacy-two", true)
	failure := makeTenant("legacy-three", true)
	fresh := makeTenant("fresh", false)
	exec("INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'business_timezone','\"America/New_York\"')", legacy)
	appTx, appErr := app.TenantTx(ctx, fresh)
	if appErr != nil {
		t.Fatal(appErr)
	}
	_, appErr = store.New(appTx).PlatformInitializeBillingCalendar(ctx, store.PlatformInitializeBillingCalendarParams{TenantID: fresh, Timezone: "UTC", FirstPaidAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}, AnchorAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}, Source: "manual_legacy", ReviewReason: "Application must not fabricate a protected billing calendar", CreatedBy: pgtype.Int8{Int64: staff, Valid: true}})
	_ = appTx.Rollback(ctx)
	if appErr == nil {
		t.Fatal("application role forged billing calendar")
	}
	var actor atomic.Int64
	actor.Store(staff)
	h := &BillingCalendarAdminHandler{Store: q}
	r := gin.New()
	r.Use(middleware.AuditPlatform(q, zap.NewNop()), func(c *gin.Context) {
		if id := actor.Load(); id > 0 {
			c.Set(middleware.ContextUserID, id)
		}
	}, middleware.RequirePlatformAdmin(q))
	r.GET("/api/v1/platform/tenants/:id/billing-calendar", h.Get)
	r.PUT("/api/v1/platform/tenants/:id/billing-calendar", middleware.RequirePlatformTx(p, zap.NewNop()), h.Put)
	call := func(method string, tenant int64, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, method, fmt.Sprintf("/api/v1/platform/tenants/%d/billing-calendar", tenant), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	body := `{"timezone":"Asia/Shanghai","first_paid_at":"2024-01-31T09:00:00+08:00","reason":"Verified historical records and confirmed original timezone with tenant owner"}`
	before := scalar("SELECT extract(epoch FROM period_end)::bigint FROM subscription_terms WHERE tenant_id=$1", legacy)
	got := call(http.MethodGet, legacy, "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"review_required":true`) || !strings.Contains(got.Body.String(), `"calendar":null`) || !strings.Contains(got.Body.String(), `"current_business_timezone":"America/New_York"`) {
		t.Fatalf("legacy GET%d %s", got.Code, got.Body)
	}
	if scalar("SELECT count(*) FROM billing_calendars WHERE tenant_id=$1", legacy) != 0 {
		t.Fatal("GET invented missing historical facts")
	}
	actor.Store(outsider)
	if w := call(http.MethodPut, legacy, body); w.Code != 403 {
		t.Fatalf("nonstaff%d %s", w.Code, w.Body)
	}
	actor.Store(0)
	if w := call(http.MethodGet, legacy, ""); w.Code != 401 {
		t.Fatalf("unauthenticated%d", w.Code)
	}
	actor.Store(staff)
	for _, invalid := range []string{strings.Replace(body, "Asia/Shanghai", "Local", 1), strings.Replace(body, "Asia/Shanghai", "Invalid/Zone", 1), strings.Replace(body, "2024-01-31T09:00:00+08:00", time.Now().Add(time.Hour).Format(time.RFC3339), 1), strings.Replace(body, "2024-01-31T09:00:00+08:00", "2024-01-31T09:00:00", 1), `{"timezone":"Asia/Shanghai","first_paid_at":"2024-01-31T09:00:00+08:00","reason":"Too short"}`} {
		if w := call(http.MethodPut, legacy, invalid); w.Code != 400 {
			t.Fatalf("invalid input%d %s", w.Code, w.Body)
		}
	}
	if w := call(http.MethodPut, fresh, body); w.Code != 409 {
		t.Fatalf("new tenant manual calendar%d %s", w.Code, w.Body)
	}
	if w := call(http.MethodPut, 999999, body); w.Code != 404 {
		t.Fatalf("unknown tenant%d %s", w.Code, w.Body)
	}
	saved := call(http.MethodPut, legacy, strings.TrimSuffix(body, "}")+fmt.Sprintf(`,"tenant_id":%d,"created_by":%d,"source":"first_paid"}`, fresh, outsider))
	if saved.Code != 200 {
		t.Fatalf("legacy PUT%d %s", saved.Code, saved.Body)
	}
	var response struct {
		Calendar struct {
			Timezone, Source string
			Version          int64
			FirstPaidAt      time.Time `json:"first_paid_at"`
			AnchorAt         time.Time `json:"anchor_at"`
		}
		ReviewRequired bool `json:"review_required"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	expected := time.Date(2024, time.January, 31, 1, 0, 0, 0, time.UTC)
	if response.Calendar.Timezone != "Asia/Shanghai" || response.Calendar.Source != "manual_legacy" || response.Calendar.Version != 1 || !response.Calendar.FirstPaidAt.Equal(expected) || !response.Calendar.AnchorAt.Equal(expected) || response.ReviewRequired {
		t.Fatalf("calendar DTO%s", saved.Body)
	}
	if scalar("SELECT count(*) FROM billing_calendars WHERE tenant_id=$1", fresh) != 0 {
		t.Fatal("body tenant claim changed calendar ownership")
	}
	if strings.Contains(saved.Body.String(), "review_reason") || strings.Contains(saved.Body.String(), "created_by") {
		t.Fatal("read DTO leaked operator metadata")
	}
	if scalar("SELECT extract(epoch FROM period_end)::bigint FROM subscription_terms WHERE tenant_id=$1", legacy) != before || scalar("SELECT extract(epoch FROM period_end)::bigint FROM subscriptions WHERE tenant_id=$1 AND status='active'", legacy) != before {
		t.Fatal("legacy verification rewrote paid periods")
	}
	if scalar("SELECT count(*) FROM billing_calendars WHERE tenant_id=$1 AND created_by=$2 AND source='manual_legacy'", legacy, staff) != 1 || scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND actor_user_id=$2 AND detail->>'reason'='Verified historical records and confirmed original timezone with tenant owner' AND detail->>'source'='manual_legacy' AND detail->>'status'='200'", legacy, staff) != 1 {
		t.Fatal("verified facts and mandatory audit were not committed once")
	}
	readTx, readErr := app.TenantTx(ctx, concurrent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	_, readErr = store.New(readTx).GetBillingCalendar(ctx, legacy)
	_ = readTx.Rollback(ctx)
	if !errors.Is(readErr, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant calendar exposed: %v", readErr)
	}
	if w := call(http.MethodPut, legacy, strings.Replace(body, "Asia/Shanghai", "UTC", 1)); w.Code != 409 {
		t.Fatalf("existing calendar overwritten%d %s", w.Code, w.Body)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); statuses <- call(http.MethodPut, concurrent, body).Code }()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for code := range statuses {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent verification%v", counts)
	}
	exec("CREATE FUNCTION reject_calendar_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.tenant_id=" + fmt.Sprint(failure) + " AND NEW.detail->>'outcome'='succeeded' THEN RAISE EXCEPTION 'injected mandatory audit failure'; END IF; RETURN NEW; END $$")
	exec("CREATE TRIGGER reject_calendar_admin_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_calendar_admin_audit()")
	if w := call(http.MethodPut, failure, body); w.Code != 500 || !strings.Contains(w.Body.String(), "AUDIT_UNAVAILABLE") {
		t.Fatalf("audit failure%d %s", w.Code, w.Body)
	}
	if scalar("SELECT count(*) FROM billing_calendars WHERE tenant_id=$1", failure) != 0 || scalar("SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND detail->>'outcome'='failed'", failure) != 1 {
		t.Fatal("audit failure committed calendar or lost failure audit")
	}
	direct := gin.New()
	direct.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, staff) })
	direct.PUT("/:id", h.Put)
	unscoped := httptest.NewRecorder()
	direct.ServeHTTP(unscoped, httptest.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("/%d", failure), strings.NewReader(body)))
	if unscoped.Code != 503 {
		t.Fatalf("missing mandatory transaction%d", unscoped.Code)
	}
}
