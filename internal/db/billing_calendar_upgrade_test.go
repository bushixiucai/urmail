package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	domainSettings "github.com/bushixiucai/urmail/internal/domain/settings"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func calendarDatabaseAt63(t *testing.T) (string, *pgx.Conn, int64, int64) {
	t.Helper()
	ctx := t.Context()
	dsn := testdb.New(t)
	source, err := iofs.New(db.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	migrationURL, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := migrationURL.Query()
	params.Set("role", "urmail_migrate")
	migrationURL.RawQuery = params.Encode()
	sqlDB, err := sql.Open("pgx", migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	driver, err := migratepgx.WithInstance(sqlDB, &migratepgx.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Migrate(63); err != nil {
		t.Fatal(err)
	}
	if a, b := m.Close(); a != nil || b != nil {
		t.Fatal(a, b)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err = conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	var brand, tenant int64
	if err = conn.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('calendar-upgrade','Calendar upgrade') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Calendar upgrade','calendar-upgrade','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	return dsn, conn, brand, tenant
}

func seedCalendarUpgradePaidFacts(t *testing.T, conn *pgx.Conn, tenant int64) {
	t.Helper()
	ctx := t.Context()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('calendar-upgrade','{\"en\":\"Historical\"}',10000,'CNY',1073741824,3,2,10)")
	exec("UPDATE subscriptions SET plan_id='calendar-upgrade',period_start='2024-01-31T01:23:45Z',period_end='2024-02-29T01:23:45Z',plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='calendar-upgrade') WHERE tenant_id=$1", tenant)
	var snapshot []byte
	if err := conn.QueryRow(ctx, "SELECT to_jsonb(p) FROM plans p WHERE code='calendar-upgrade'").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	planDigest, err := billing.SnapshotDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	docDigest, err := billing.SnapshotDigest([]byte(`{"seller":{"name":"Frozen historical seller"}}`))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, time.January, 31, 1, 23, 45, 0, time.UTC)
	end := time.Date(2024, time.February, 29, 1, 23, 45, 0, time.UTC)
	quote := billing.Quote{TenantID: tenant, Kind: "renew", PlanCode: "calendar-upgrade", BillingCycle: "monthly", Currency: "CNY", BaseAmountCents: 10000, FinalAmountCents: 10000, PlanAmountCents: 10000, PlanSnapshotDigest: planDigest, DocumentSnapshotDigest: docDigest, PeriodStart: start, PeriodEnd: end, GeneratedAt: start.Add(-time.Hour), ExpiresAt: start.Add(time.Hour)}
	quote.Digest = quote.ComputeDigest([]byte("historical-quote-signing-key-at-least-thirty-two-bytes"))
	payload, err := json.Marshal(quote)
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO billing_quotes(digest,tenant_id,payload,plan_snapshot,document_snapshot,expires_at) VALUES($1,$2,$3,$4,'{\"seller\":{\"name\":\"Frozen historical seller\"}}',$5)", quote.Digest, tenant, payload, snapshot, quote.ExpiresAt)
	var order int64
	if err := conn.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,kind,status,amount_cents,currency,plan_code,billing_cycle,quote_digest,paid_at) VALUES($1,'CALENDAR-UPGRADE-PAID','renew','paid',10000,'CNY','calendar-upgrade','monthly',$2,'2024-01-31T01:23:45Z') RETURNING id", tenant, quote.Digest).Scan(&order); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO subscription_terms(order_id,tenant_id,period_start,period_end,billing_period_start,plan_snapshot,base_amount_cents,currency,billing_cycle) VALUES($1,$2,$3,$4,$3,$5,10000,'CNY','monthly')", order, tenant, start, end, snapshot)
	exec("INSERT INTO invoices(tenant_id,order_id,number,period_start,period_end,amount_cents,currency,status,issued_at,document_snapshot) VALUES($1,$2,'CALENDAR-UPGRADE-INVOICE',$3,$4,10000,'CNY','paid',$3,'{\"seller\":{\"name\":\"Frozen historical seller\"}}')", tenant, order, start, end)
	exec("INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'business_timezone','\"America/New_York\"')", tenant)
}

func calendarUpgradeFacts(t *testing.T, conn *pgx.Conn, tenant int64) string {
	t.Helper()
	var facts string
	err := conn.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'subscription',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM subscriptions s WHERE tenant_id=$1),
 'orders',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM orders o WHERE tenant_id=$1),
 'terms',(SELECT jsonb_agg(to_jsonb(st)-'calendar_snapshot' ORDER BY order_id) FROM subscription_terms st WHERE tenant_id=$1),
 'quotes',(SELECT jsonb_agg(to_jsonb(q) ORDER BY digest) FROM billing_quotes q WHERE tenant_id=$1),
 'invoices',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i WHERE tenant_id=$1))::text`, tenant).Scan(&facts)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestBillingCalendarUpgradePreservesPaidFactsAndRequiresReview(t *testing.T) {
	dsn, conn, _, tenant := calendarDatabaseAt63(t)
	seedCalendarUpgradePaidFacts(t, conn, tenant)
	before := calendarUpgradeFacts(t, conn, tenant)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if err = m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := calendarUpgradeFacts(t, conn, tenant); after != before {
		t.Fatal("migration rewrote historical paid dates, immutable snapshots, quote payload or invoice")
	}
	var count int
	if err = conn.QueryRow(t.Context(), "SELECT count(*) FROM billing_calendars").Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration invented first paid timezone/calendar: %d %v", count, err)
	}
	required, err := store.New(conn).BillingCalendarReviewRequired(t.Context(), tenant)
	if err != nil || !required {
		t.Fatalf("unknown historical timezone did not require review: %+v %v", required, err)
	}
	var nullSnapshot bool
	if err = conn.QueryRow(t.Context(), "SELECT calendar_snapshot IS NULL FROM subscription_terms WHERE tenant_id=$1", tenant).Scan(&nullSnapshot); err != nil || !nullSnapshot {
		t.Fatal("migration backfilled unproven historical calendar")
	}
}

func TestBillingCalendarRollbackRefusesFacts(t *testing.T) {
	for _, kind := range []string{"calendar", "term-snapshot"} {
		t.Run(kind, func(t *testing.T) {
			dsn, conn, _, tenant := calendarDatabaseAt63(t)
			seedCalendarUpgradePaidFacts(t, conn, tenant)
			m, err := db.NewMigrator(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = m.Close() }()
			if err = m.Up(t.Context()); err != nil {
				t.Fatal(err)
			}
			if kind == "calendar" {
				_, err = conn.Exec(t.Context(), "INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason) VALUES($1,'Asia/Shanghai','2024-01-31T01:23:45Z','2024-01-31T01:23:45Z','manual_legacy','Verified historical calendar facts')", tenant)
			} else {
				_, err = conn.Exec(t.Context(), "UPDATE subscription_terms SET calendar_snapshot='{\"timezone\":\"Asia/Shanghai\",\"version\":1}' WHERE tenant_id=$1", tenant)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Down advances exactly one migration. Reach the calendar migration
			// explicitly so later schema additions cannot bypass this assertion.
			for {
				version, dirty, versionErr := m.Version()
				if versionErr != nil || dirty || version < 64 {
					t.Fatalf("cannot reach clean schema64: version=%d dirty=%t err=%v", version, dirty, versionErr)
				}
				if version == 64 {
					break
				}
				if err = m.Down(t.Context()); err != nil {
					t.Fatal("reach schema64 without changing calendar facts", err)
				}
			}
			err = m.Down(t.Context())
			if err == nil || !strings.Contains(err.Error(), "BILLING_CALENDAR_REQUIRES_CONTROLLED_ROLLBACK") {
				t.Fatalf("rollback dropped protected calendar facts: %v", err)
			}
			var preserved bool
			if kind == "calendar" {
				err = conn.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM billing_calendars WHERE tenant_id=$1)", tenant).Scan(&preserved)
			} else {
				err = conn.QueryRow(t.Context(), "SELECT calendar_snapshot IS NOT NULL FROM subscription_terms WHERE tenant_id=$1", tenant).Scan(&preserved)
			}
			if err != nil || !preserved {
				t.Fatal("refused rollback still lost facts", err)
			}
		})
	}
}

func TestBillingCalendarWallGuardsAndInheritedTimezonePreserveQuotaWindow(t *testing.T) {
	dsn, conn, brand, tenant := calendarDatabaseAt63(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if err = m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason) VALUES($1,'Asia/Shanghai','2024-01-31T01:23:45.123456Z','2024-01-31T01:23:45.123456Z','manual_legacy','Verified historical calendar facts')", tenant)
	for _, assignment := range []string{"timezone='UTC'", "first_paid_at='2024-01-30T01:23:45Z'", "first_anchor_wall='{}'::jsonb"} {
		if _, err = conn.Exec(ctx, "UPDATE billing_calendars SET "+assignment+",anchor_at='2025-02-10T02:23:45Z',version=version+1,source='reactivation' WHERE tenant_id=$1", tenant); err == nil {
			t.Fatalf("platform changed immutable first calendar fact: %s", assignment)
		}
	}
	exec("UPDATE billing_calendars SET anchor_at='2025-02-10T02:23:45Z',anchor_wall='{\"day\":1}',version=version+1,source='reactivation' WHERE tenant_id=$1", tenant)
	var firstDay, activeDay, hour, nanos int
	if err = conn.QueryRow(ctx, "SELECT (first_anchor_wall->>'day')::int,(anchor_wall->>'day')::int,(first_anchor_wall->>'hour')::int,(first_anchor_wall->>'nanosecond')::int FROM billing_calendars WHERE tenant_id=$1", tenant).Scan(&firstDay, &activeDay, &hour, &nanos); err != nil || firstDay != 31 || activeDay != 10 || hour != 9 || nanos != 123456000 {
		t.Fatalf("persisted wall anchors lost or spoofed: %d %d %d %d %v", firstDay, activeDay, hour, nanos, err)
	}
	checkZone := func(want string) {
		t.Helper()
		zone, err := store.New(conn).GetBillingBusinessTimezone(ctx, tenant)
		if err != nil || zone != want {
			t.Fatalf("business timezone %s want %s err=%v", zone, want, err)
		}
		rows, err := store.New(conn).ListSettingsByScopes(ctx, store.ListSettingsByScopesParams{ScopeID: brand, ScopeID_2: tenant})
		if err != nil {
			t.Fatal(err)
		}
		entries := make([]domainSettings.SettingEntry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, domainSettings.SettingEntry{Scope: domainSettings.Scope(row.Scope), ScopeID: row.ScopeID, Key: row.Key, Value: row.Value})
		}
		merged, err := domainSettings.Merge3Layers(entries)
		if err != nil {
			t.Fatal(err)
		}
		uiZone := "Asia/Shanghai"
		if raw, ok := merged["business_timezone"]; ok {
			if err = json.Unmarshal(raw, &uiZone); err != nil {
				t.Fatal(err)
			}
		}
		if uiZone != zone {
			t.Fatalf("Tenant UI and shared function disagree: %s vs %s", uiZone, zone)
		}
	}
	// Narrow a superuser fixture session to the actual app role: session_user
	// membership must never turn SET LOCAL ROLE app into a metadata bypass.
	for _, scope := range []string{"", strconv.FormatInt(tenant+9999, 10), strconv.FormatInt(tenant, 10)} {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, "SET LOCAL ROLE urmail_app"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,true)", scope); err != nil {
			t.Fatal(err)
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM billing_calendars WHERE tenant_id=$1", tenant).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if scope == strconv.FormatInt(tenant, 10) {
			want = 1
		}
		if count != want {
			t.Fatalf("calendar RLS scope=%q exposed %d rows", scope, count)
		}
		_ = tx.Rollback(ctx)
		for _, statement := range []string{"SELECT urmail_business_timezone($1)", "SELECT urmail_billing_history_requires_review($1,0)"} {
			tx, err = conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, "SET LOCAL ROLE urmail_app"); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, "SELECT set_config('urmail.tenant_id',$1,true)", scope); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, statement, tenant)
			_ = tx.Rollback(ctx)
			if scope == strconv.FormatInt(tenant, 10) {
				if err != nil {
					t.Fatalf("own metadata helper refused: %v", err)
				}
			} else {
				var pgError *pgconn.PgError
				if !errors.As(err, &pgError) || pgError.Code != "42501" || !strings.Contains(pgError.Message, "TENANT_CONTEXT_REQUIRED") {
					t.Fatalf("metadata helper bypassed narrowed role scope=%q: %v", scope, err)
				}
			}
		}
	}
	checkZone("Asia/Shanghai")
	exec("INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'business_timezone','\"UTC\"') ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value")
	checkZone("UTC")
	exec("UPDATE plans SET daily_send_limit=2 WHERE code='free'")
	exec("UPDATE subscriptions SET plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='free') WHERE tenant_id=$1", tenant)
	var domain, mailbox int64
	if err = conn.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES($1,'calendar-quota.example.test','active','fixture',now(),'ready') RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'self','self@calendar-quota.example.test','fixture',1024) RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO mailbox_usage(mailbox_id,tenant_id,day,messages_sent) VALUES($1,$2,urmail_quota_day($2),2)", mailbox, tenant)
	var original string
	if err = conn.QueryRow(ctx, "SELECT to_jsonb(q)::text FROM tenant_quota_calendar q WHERE tenant_id=$1", tenant).Scan(&original); err != nil {
		t.Fatal(err)
	}
	assertWindow := func() {
		t.Helper()
		var allowed, mailAllowed, same bool
		if err := conn.QueryRow(ctx, "SELECT urmail_mail_allowed($1),urmail_take_mailbox_quota($1,$2)", tenant, mailbox).Scan(&mailAllowed, &allowed); err != nil {
			t.Fatal(err)
		}
		// Read after the side-effecting function, not from its statement snapshot.
		if err := conn.QueryRow(ctx, "SELECT to_jsonb(q)::text=$2 FROM tenant_quota_calendar q WHERE tenant_id=$1", tenant, original).Scan(&same); err != nil {
			t.Fatal(err)
		}
		if !mailAllowed || allowed || !same {
			t.Fatalf("timezone change reset live allowance: mail=%v take=%v same=%v", mailAllowed, allowed, same)
		}
	}
	for _, phase := range []struct {
		scope, zone string
		id          int64
	}{{"brand", "Pacific/Kiritimati", brand}, {"tenant", "America/New_York", tenant}, {"platform", "Pacific/Pago_Pago", 0}} {
		value, _ := json.Marshal(phase.zone)
		exec("INSERT INTO settings(scope,scope_id,key,value) VALUES($1,$2,'business_timezone',$3) ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value", phase.scope, phase.id, value)
		want := phase.zone
		if phase.scope == "platform" {
			want = "America/New_York"
		}
		checkZone(want)
		assertWindow()
	}
	exec("DELETE FROM settings WHERE scope='tenant' AND scope_id=$1 AND key='business_timezone'", tenant)
	checkZone("Pacific/Kiritimati")
	assertWindow()
	exec("DELETE FROM settings WHERE scope='brand' AND scope_id=$1 AND key='business_timezone'", brand)
	checkZone("Pacific/Pago_Pago")
	assertWindow()
	var used int
	if err = conn.QueryRow(ctx, "SELECT messages_sent FROM mailbox_usage WHERE mailbox_id=$1", mailbox).Scan(&used); err != nil || used != 2 {
		t.Fatalf("timezone mutation changed counter: %d %v", used, err)
	}
}

func TestLegacyCurrentEntitlementMigrationRollbackGate(t *testing.T) {
	for _, active := range []bool{true, false} {
		name := "boundary-past"
		if active {
			name = "current-fallback"
		}
		t.Run(name, func(t *testing.T) {
			dsn, conn, _, tenant := calendarDatabaseAt63(t)
			ctx := t.Context()
			source, err := iofs.New(db.FS(), ".")
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			params := u.Query()
			params.Set("role", "urmail_migrate")
			u.RawQuery = params.Encode()
			sqlDB, err := sql.Open("pgx", u.String())
			if err != nil {
				t.Fatal(err)
			}
			driver, err := migratepgx.WithInstance(sqlDB, &migratepgx.Config{})
			if err != nil {
				t.Fatal(err)
			}
			m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = m.Close() }()
			if err = m.Migrate(65); err != nil {
				t.Fatal(err)
			}
			first := time.Now().UTC().Add(-15 * 24 * time.Hour).Truncate(time.Second)
			if !active {
				first = first.Add(-30 * 24 * time.Hour)
			}
			boundary := first.Add(30 * 24 * time.Hour)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := conn.Exec(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("INSERT INTO plans(code,name,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('legacy-down','{}',100,'CNY',1073741824,5,2,100)")
			exec("UPDATE subscriptions SET plan_id='legacy-down',status='active',period_start=$2,period_end=$3,plan_snapshot=(SELECT to_jsonb(p) FROM plans p WHERE code='legacy-down') WHERE tenant_id=$1", tenant, first, boundary.Add(30*24*time.Hour))
			var order int64
			if err = conn.QueryRow(ctx, "INSERT INTO orders(tenant_id,order_no,status,kind,amount_cents,currency,plan_code,billing_cycle) VALUES($1,'LEGACY-65-ROLLBACK','paid','renew',100,'CNY','legacy-down','monthly') RETURNING id", tenant).Scan(&order); err != nil {
				t.Fatal(err)
			}
			exec("INSERT INTO subscription_terms(order_id,tenant_id,period_start,period_end,billing_period_start,plan_snapshot,base_amount_cents,currency,billing_cycle) SELECT $1,$2,$3,$4,$3,to_jsonb(p),100,'CNY','monthly' FROM plans p WHERE code='legacy-down'", order, tenant, boundary, boundary.Add(30*24*time.Hour))
			before := calendarUpgradeFacts(t, conn, tenant)
			var legacyEnd *time.Time
			if err = conn.QueryRow(ctx, "SELECT urmail_legacy_current_entitlement_end($1)", tenant).Scan(&legacyEnd); err != nil || (legacyEnd != nil) != active {
				t.Fatalf("fixture fallback=%v active=%t err=%v", legacyEnd, active, err)
			}
			err = m.Migrate(64)
			if active {
				if err == nil || !strings.Contains(err.Error(), "LEGACY_CURRENT_RIGHTS_REQUIRE_CONTROLLED_ROLLBACK") {
					t.Fatalf("down dropped an active legacy coverage dependency: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var removed bool
				if err = conn.QueryRow(ctx, "SELECT to_regprocedure('urmail_legacy_current_entitlement_end(bigint)') IS NULL").Scan(&removed); err != nil || !removed {
					t.Fatalf("down65 did not remove helper: %t %v", removed, err)
				}
				if err = m.Migrate(65); err != nil {
					t.Fatal(err)
				}
			}
			if after := calendarUpgradeFacts(t, conn, tenant); after != before {
				t.Fatal("rollback gate/down-up rewrote paid entitlement facts")
			}
			var allowed bool
			if err = conn.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", tenant).Scan(&allowed); err != nil || !allowed {
				t.Fatalf("rollback check changed current service: %t %v", allowed, err)
			}
		})
	}
}
