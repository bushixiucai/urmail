package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

type calendarHarness struct {
	t            *testing.T
	platform     *db.Pool
	tenant, user int64
	router       *gin.Engine
}

func newCalendarHarness(t *testing.T) calendarHarness {
	t.Helper()
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('calendar','Calendar') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Calendar','calendar','active') RETURNING id", brand)
	user := scalar("INSERT INTO users(email,password_hash) VALUES('calendar@example.test','unused') RETURNING id")
	for _, sql := range []string{
		fmt.Sprintf("INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(%d,%d,'owner')", user, tenant),
		`INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('calendar','{"en":"Calendar"}',10000,'CNY',1073741824,8,5,1000,'{}')`,
		`INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('calendar','CNY','monthly',10000),('calendar','CNY','quarterly',27000),('calendar','CNY','annual',100000)`,
	} {
		if _, err := platform.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	seedVerifiedBillingDomain(t, platform, tenant)
	h := NewBillingHandler(store.New(app.Pool))
	h.Pool = platform
	h.QuoteHMACKey = []byte("calendar-production-quote-key-at-least-32-bytes")
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) }, middleware.RequireTenantTx(app, zap.NewNop()))
	r.POST("/quote", h.Quote)
	r.POST("/checkout", h.Checkout)
	r.PATCH("/tenant", NewTenantHandler(store.New(app.Pool), store.New(platform.Pool)).UpdateSettings)
	return calendarHarness{t, platform, tenant, user, r}
}

func (h calendarHarness) exec(sql string, args ...any) {
	h.t.Helper()
	if _, err := h.platform.Exec(context.Background(), sql, args...); err != nil {
		h.t.Fatal(err)
	}
}
func (h calendarHarness) call(path string, body []byte, key string) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	h.router.ServeHTTP(rec, req)
	return rec
}
func (h calendarHarness) quote(cycle string) billing.Quote {
	h.t.Helper()
	res := h.call("/quote", []byte(fmt.Sprintf(`{"plan_code":"calendar","currency":"CNY","billing_cycle":%q}`, cycle)), "")
	if res.Code != 200 {
		h.t.Fatalf("quote %d: %s", res.Code, res.Body.String())
	}
	var q billing.Quote
	if err := json.Unmarshal(res.Body.Bytes(), &q); err != nil {
		h.t.Fatal(err)
	}
	return q
}
func (h calendarHarness) pay(q billing.Quote, key string) int64 {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{"quote": q})
	res := h.call("/checkout", body, key)
	if res.Code != 201 {
		h.t.Fatalf("checkout %d: %s", res.Code, res.Body.String())
	}
	var out struct {
		Order store.Orders `json:"order"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		h.t.Fatal(err)
	}
	if err := SettleOrder(context.Background(), h.platform, out.Order.ID); err != nil {
		h.t.Fatal(err)
	}
	return out.Order.ID
}

func TestBillingCalendarFirstPaidFreezesTimezoneAndRejectsTampering(t *testing.T) {
	h := newCalendarHarness(t)
	h.exec("INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'business_timezone','\"Asia/Shanghai\"')", h.tenant)
	quote := h.quote("monthly")
	if quote.Calendar == nil || !quote.Calendar.Provisional || quote.Calendar.Timezone != "Asia/Shanghai" {
		t.Fatalf("missing provisional calendar: %+v", quote.Calendar)
	}
	tampered := quote
	copyCalendar := *quote.Calendar
	copyCalendar.Timezone = "America/New_York"
	tampered.Calendar = &copyCalendar
	body, _ := json.Marshal(map[string]any{"quote": tampered})
	if res := h.call("/checkout", body, "tampered"); res.Code != 409 {
		t.Fatalf("calendar mutation accepted: %d %s", res.Code, res.Body.String())
	}
	order := h.pay(quote, "first")
	q := store.New(h.platform.Pool)
	row, err := q.GetBillingCalendar(context.Background(), h.tenant)
	if err != nil {
		t.Fatal(err)
	}
	var start, end time.Time
	var snapshot []byte
	if err = h.platform.QueryRow(context.Background(), "SELECT billing_period_start,period_end,calendar_snapshot FROM subscription_terms WHERE order_id=$1", order).Scan(&start, &end, &snapshot); err != nil {
		t.Fatal(err)
	}
	if row.Timezone != "Asia/Shanghai" || row.Version != 1 || !row.AnchorAt.Time.Equal(start) || !row.FirstPaidAt.Time.Equal(start) || !strings.Contains(string(snapshot), "Asia/Shanghai") {
		t.Fatalf("calendar was not frozen with the fulfilled term: %+v %s", row, snapshot)
	}
	h.exec("UPDATE settings SET value='\"America/New_York\"' WHERE scope='tenant' AND scope_id=$1 AND key='business_timezone'", h.tenant)
	renewed := h.quote("monthly")
	if renewed.Calendar.Timezone != "Asia/Shanghai" || !renewed.Calendar.AnchorAt.Equal(start) || renewed.Calendar.Provisional {
		t.Fatalf("later timezone rewrote billing calendar: %+v", renewed.Calendar)
	}
	h.pay(renewed, "renew")
	var oldEnd time.Time
	if err = h.platform.QueryRow(context.Background(), "SELECT period_end FROM subscription_terms WHERE order_id=$1", order).Scan(&oldEnd); err != nil || !oldEnd.Equal(end) {
		t.Fatalf("paid history changed: %v", err)
	}
}

// Seed a complete trusted paid timeline, including future prepaid terms. These
// are immutable fixture facts; all new quotes and payments use production code.
func (h calendarHarness) seedTimeline(first time.Time, zone, cycle string, lastEnd time.Time) {
	h.t.Helper()
	ctx := context.Background()
	anchor, err := billing.NewCalendarAnchor(first, zone)
	if err != nil {
		h.t.Fatal(err)
	}
	h.exec("INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason,created_by) VALUES($1,$2,$3,$3,'manual_legacy','Verified historical fixture facts',$4)", h.tenant, zone, first, h.user)
	var currentStart time.Time
	for begin, index := first, 0; begin.Before(lastEnd); index++ {
		end, e := billing.AddAnchoredCycle(begin, cycle, anchor)
		if e != nil {
			h.t.Fatal(e)
		}
		var order int64
		e = h.platform.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle,paid_at) VALUES($1,$2,'renew','paid',10000,'CNY','calendar',$3,$4) RETURNING id", h.tenant, fmt.Sprintf("HISTORY-%d-%d", h.tenant, index), cycle, begin).Scan(&order)
		if e != nil {
			h.t.Fatal(e)
		}
		h.exec("INSERT INTO subscription_terms(order_id,tenant_id,period_start,period_end,billing_period_start,base_amount_cents,currency,billing_cycle,plan_snapshot) SELECT $1,$2,$3,$4,$3,10000,'CNY',$5,to_jsonb(p) FROM plans p WHERE code='calendar'", order, h.tenant, begin, end, cycle)
		if !begin.After(time.Now()) && end.After(time.Now()) {
			currentStart = begin
		}
		begin = end
	}
	if currentStart.IsZero() {
		h.t.Fatal("fixture has no current paid term")
	}
	h.exec("UPDATE subscriptions SET status='canceled' WHERE tenant_id=$1", h.tenant)
	h.exec("INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end,billing_cycle,plan_snapshot) SELECT $1,'calendar','active',$2,$3,$4,to_jsonb(p) FROM plans p WHERE code='calendar'", h.tenant, currentStart, lastEnd, cycle)
}

func TestBillingCalendarOriginalDayAndLeapReturnThroughProduction(t *testing.T) {
	for _, day := range []int{30, 31} {
		t.Run(fmt.Sprint(day), func(t *testing.T) {
			h := newCalendarHarness(t)
			zone, _ := time.LoadLocation("Asia/Shanghai")
			year := time.Now().Year() + 1
			for time.Date(year, 3, 0, 0, 0, 0, 0, zone).Day() != 28 {
				year++
			}
			first := time.Date(time.Now().Year()-1, 1, day, 0, 30, 0, 0, zone)
			last := time.Date(year, 2, 28, 0, 30, 0, 0, zone)
			h.seedTimeline(first, "Asia/Shanghai", "monthly", last)
			quote := h.quote("monthly")
			expected := time.Date(year, 3, day, 0, 30, 0, 0, zone)
			if !quote.PeriodStart.Equal(last) || !quote.PeriodEnd.Equal(expected) {
				t.Fatalf("clamp lost original day: %s -> %s", quote.PeriodStart, quote.PeriodEnd)
			}
			order := h.pay(quote, "after-short-month")
			var end time.Time
			if err := h.platform.QueryRow(context.Background(), "SELECT period_end FROM subscription_terms WHERE order_id=$1", order).Scan(&end); err != nil || !end.Equal(expected) {
				t.Fatalf("settlement calendar differs: %v %s", err, end)
			}
		})
	}
	t.Run("leap anniversary", func(t *testing.T) {
		h := newCalendarHarness(t)
		zone, _ := time.LoadLocation("Asia/Shanghai")
		leap := time.Now().Year() + 1
		for time.Date(leap, 3, 0, 0, 0, 0, 0, zone).Day() != 29 {
			leap++
		}
		first := time.Date(leap-8, 2, 29, 0, 30, 0, 0, zone)
		last := time.Date(leap-1, 2, 28, 0, 30, 0, 0, zone)
		h.seedTimeline(first, "Asia/Shanghai", "annual", last)
		quote := h.quote("annual")
		expected := time.Date(leap, 2, 29, 0, 30, 0, 0, zone)
		if !quote.PeriodEnd.Equal(expected) {
			t.Fatalf("leap anniversary lost: %s", quote.PeriodEnd)
		}
		order := h.pay(quote, "leap-return")
		var end time.Time
		if err := h.platform.QueryRow(context.Background(), "SELECT period_end FROM subscription_terms WHERE order_id=$1", order).Scan(&end); err != nil || !end.Equal(expected) {
			t.Fatalf("leap settlement: %v %s", err, end)
		}
	})
}

func TestBillingCalendarExpiredServiceStartsFullCycleWithoutChangingFirstTimezone(t *testing.T) {
	h := newCalendarHarness(t)
	ctx := context.Background()
	zone, _ := time.LoadLocation("Asia/Shanghai")
	first := time.Date(time.Now().Year()-2, 1, 31, 0, 30, 0, 0, zone)
	oldStart := time.Date(time.Now().Year(), time.Now().Month()-2, 1, 0, 30, 0, 0, zone)
	oldEnd := oldStart.AddDate(0, 1, 0)
	h.exec("INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason,created_by) VALUES($1,'Asia/Shanghai',$2,$2,'manual_legacy','Verified legacy fixture before expired reactivation',$3)", h.tenant, first, h.user)
	h.exec("INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'business_timezone','\"America/New_York\"')", h.tenant)
	var oldOrder int64
	if err := h.platform.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle,paid_at) VALUES($1,'EXPIRED','renew','paid',10000,'CNY','calendar','monthly',$2) RETURNING id", h.tenant, oldStart).Scan(&oldOrder); err != nil {
		t.Fatal(err)
	}
	h.exec("INSERT INTO subscription_terms(order_id,tenant_id,period_start,period_end,billing_period_start,base_amount_cents,currency,billing_cycle,plan_snapshot) SELECT $1,$2,$3,$4,$3,10000,'CNY','monthly',to_jsonb(p) FROM plans p WHERE code='calendar'", oldOrder, h.tenant, oldStart, oldEnd)
	h.exec("UPDATE subscriptions SET status='canceled' WHERE tenant_id=$1", h.tenant)
	h.exec("INSERT INTO subscriptions(tenant_id,plan_id,status,period_start,period_end,billing_cycle,plan_snapshot) SELECT $1,'calendar','active',$2,$3,'monthly',to_jsonb(p) FROM plans p WHERE code='calendar'", h.tenant, oldStart, oldEnd)
	quote := h.quote("monthly")
	if !quote.Calendar.Reanchor || !quote.Calendar.Provisional || quote.Calendar.Timezone != "Asia/Shanghai" {
		t.Fatalf("bad reactivation quote: %+v", quote.Calendar)
	}
	order := h.pay(quote, "complete-reactivation")
	q := store.New(h.platform.Pool)
	row, err := q.GetBillingCalendar(ctx, h.tenant)
	if err != nil {
		t.Fatal(err)
	}
	var start, end time.Time
	if err = h.platform.QueryRow(ctx, "SELECT billing_period_start,period_end FROM subscription_terms WHERE order_id=$1", order).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	anchor, err := billing.NewCalendarAnchor(start, "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := billing.AddAnchoredCycle(start, "monthly", anchor)
	if err != nil {
		t.Fatal(err)
	}
	if row.Version != 2 || row.Timezone != "Asia/Shanghai" || !row.FirstPaidAt.Time.Equal(first) || !row.AnchorAt.Time.Equal(start) || !end.Equal(expected) {
		t.Fatalf("reactivation failed full-cycle/calendar preservation: %+v %s %s", row, start, end)
	}
	var historicalEnd time.Time
	if err = h.platform.QueryRow(ctx, "SELECT period_end FROM subscription_terms WHERE order_id=$1", oldOrder).Scan(&historicalEnd); err != nil || !historicalEnd.Equal(oldEnd) {
		t.Fatalf("historical term changed: %v", err)
	}
	next := h.quote("monthly")
	if next.Calendar.Reanchor || next.Calendar.Provisional || !next.Calendar.AnchorAt.Equal(start) || next.Calendar.Version != 2 {
		t.Fatalf("new date anchor was not retained: %+v", next.Calendar)
	}
}

func TestBillingCalendarEmptyBusinessTimezoneCannotReplaceExistingSetting(t *testing.T) {
	h := newCalendarHarness(t)
	h.exec("INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'business_timezone','\"Asia/Shanghai\"')", h.tenant)
	for _, body := range []string{`{"settings":{"business_timezone":""}}`, `{"settings":{"business_timezone":"Local"}}`} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/tenant", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.router.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("invalid timezone mutated settings: %d %s", rec.Code, rec.Body.String())
		}
	}
	zone, err := store.New(h.platform.Pool).GetBillingBusinessTimezone(t.Context(), h.tenant)
	if err != nil || zone != "Asia/Shanghai" {
		t.Fatalf("previous timezone lost: %s %v", zone, err)
	}
	quote := h.quote("monthly")
	if quote.Calendar.Timezone != "Asia/Shanghai" {
		t.Fatal("invalid setting changed first-payment estimate")
	}
}
