package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func seedAIQuotaDatabaseTenant(t *testing.T, pool *db.Pool) (int64, int64) {
	t.Helper()
	var brand, tenant, actor int64
	for _, entry := range []struct {
		sql    string
		args   []any
		target *int64
	}{
		{"INSERT INTO brands(slug,name)VALUES('quota-db','Quota')RETURNING id", nil, &brand},
	} {
		if err := pool.QueryRow(t.Context(), entry.sql, entry.args...).Scan(entry.target); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(t.Context(), "INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'Quota','quota-db','active')RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "INSERT INTO users(email,password_hash)VALUES('quota-owner@fixture.test','fixture')RETURNING id").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO user_tenant_roles(user_id,tenant_id,role)VALUES($1,$2,'owner')", actor, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO ai_provider_configs(tenant_id,mode,provider,endpoint,model)VALUES($1,'local','ollama','https://127.0.0.1:11434','owned')", tenant); err != nil {
		t.Fatal(err)
	}
	return tenant, actor
}
func TestAIQuota151DefaultDownUpAndPositiveProviderBudgetBackfill(t *testing.T) {
	for _, positive := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfigured", true: "positive_budget"}[positive], func(t *testing.T) {
			dsn := testdb.New(t)
			testdb.MigrateTo(t, dsn, 150)
			pool, err := db.OpenPlatform(t.Context(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			tenant, _ := seedAIQuotaDatabaseTenant(t, pool)
			if positive {
				if _, err = pool.Exec(t.Context(), `INSERT INTO settings(scope,scope_id,key,value)VALUES('tenant',$1,'business_timezone','"America/New_York"')`, tenant); err != nil {
					t.Fatal(err)
				}
			}
			if positive {
				if _, err = pool.Exec(t.Context(), "UPDATE ai_provider_configs SET daily_token_limit=100 WHERE tenant_id=$1", tenant); err != nil {
					t.Fatal(err)
				}
			}
			migrator, err := db.NewMigrator(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = migrator.Close() }()
			assertVersion := func(want uint) {
				t.Helper()
				version, dirty, err := migrator.Version()
				if err != nil || version != want || dirty {
					t.Fatalf("schema version = %d, dirty = %t, err = %v; want clean %d", version, dirty, err, want)
				}
			}
			// Pin this historical rollback test to 151: Up applies every pending
			// migration, while Down rolls back only the most recent one.
			testdb.MigrateTo(t, dsn, 151)
			assertVersion(151)
			// Current sqlc provider queries also select columns added after 151.
			// Read only the historical quota fields this migration owns.
			readQuota := func() store.AiProviderConfigs {
				t.Helper()
				var cfg store.AiProviderConfigs
				if err := pool.QueryRow(t.Context(), `SELECT nonplatform_call_limit, nonplatform_mailbox_call_limit, nonplatform_concurrency_limit, quota_timezone, daily_token_limit FROM ai_provider_configs WHERE tenant_id=$1`, tenant).Scan(
					&cfg.NonplatformCallLimit, &cfg.NonplatformMailboxCallLimit, &cfg.NonplatformConcurrencyLimit, &cfg.QuotaTimezone, &cfg.DailyTokenLimit,
				); err != nil {
					t.Fatal(err)
				}
				return cfg
			}
			cfg := readQuota()
			if cfg.NonplatformCallLimit != 0 || cfg.NonplatformMailboxCallLimit != 0 || cfg.NonplatformConcurrencyLimit != 0 {
				t.Fatal("upgrade silently granted AI calls")
			}
			if positive {
				if cfg.QuotaTimezone != "America/New_York" || cfg.DailyTokenLimit != 100 {
					t.Fatal("existing provider budget was not preserved and frozen", cfg.QuotaTimezone, cfg.DailyTokenLimit)
				}
				if err = migrator.Down(t.Context()); err == nil || !strings.Contains(err.Error(), "quota facts prevent downgrade") {
					t.Fatal("downgrade silently discarded frozen configuration", err)
				}
				// A rejected migration can mark schema metadata dirty, but must not
				// discard any of the durable configuration.
				if after := readQuota(); after.NonplatformCallLimit != cfg.NonplatformCallLimit || after.NonplatformMailboxCallLimit != cfg.NonplatformMailboxCallLimit || after.NonplatformConcurrencyLimit != cfg.NonplatformConcurrencyLimit || after.QuotaTimezone != cfg.QuotaTimezone || after.DailyTokenLimit != cfg.DailyTokenLimit {
					t.Fatal("rejected downgrade changed frozen configuration")
				}
			} else {
				if cfg.QuotaTimezone != "" {
					t.Fatal("unconfigured upgrade minted calendar configuration")
				}
				if err = migrator.Down(t.Context()); err != nil {
					t.Fatal(err)
				}
				assertVersion(150)
				testdb.MigrateTo(t, dsn, 151)
				assertVersion(151)
				if after := readQuota(); after.NonplatformCallLimit != 0 || after.NonplatformMailboxCallLimit != 0 || after.NonplatformConcurrencyLimit != 0 || after.QuotaTimezone != "" || after.DailyTokenLimit != 0 {
					t.Fatal("re-upgrade changed unconfigured quota defaults")
				}
			}
		})
	}
}
func TestAIQuota151PlatformOnlyFrozenAndTenantConfigurationPreservesCaps(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	tenant, actor := seedAIQuotaDatabaseTenant(t, platform)
	q := store.New(platform)
	if err := q.PlatformSetNonplatformAIQuota(t.Context(), store.PlatformSetNonplatformAIQuotaParams{TenantID: tenant, NonplatformCallLimit: 3, NonplatformMailboxCallLimit: 2, NonplatformConcurrencyLimit: 1, QuotaTimezone: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	tx, err := app.UserTx(t.Context(), tenant, actor)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), "UPDATE ai_provider_configs SET nonplatform_call_limit=999 WHERE tenant_id=$1", tenant); err == nil {
		t.Fatal("app directly granted host caps")
	}
	_ = tx.Rollback(t.Context())
	tx, err = app.UserTx(t.Context(), tenant, actor)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = store.New(tx).PlatformConfigureTenantAI(t.Context(), store.PlatformConfigureTenantAIParams{Mode: "local", Provider: "ollama", Endpoint: "https://127.0.0.1:11434", Model: "different-owned", Enabled: false, LocalOnly: true}); err != nil {
		t.Fatal("normal tenant configuration was broken", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg, err := q.PlatformGetAIProvider(t.Context(), tenant)
	if err != nil || cfg.NonplatformCallLimit != 3 || cfg.QuotaTimezone != "Asia/Shanghai" {
		t.Fatal("tenant configure changed host caps", err)
	}
	if _, err = platform.Exec(t.Context(), "UPDATE ai_provider_configs SET quota_timezone='UTC' WHERE tenant_id=$1", tenant); err == nil {
		t.Fatal("configured timezone was mutable")
	}
	// Even a future SECURITY DEFINER helper must not accidentally turn a
	// tenant caller into an authority to modify the platform-only fields.
	if _, err = platform.Exec(t.Context(), `CREATE FUNCTION test_quota_caps_escalation(bigint)RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,public AS $$UPDATE ai_provider_configs SET nonplatform_call_limit=999 WHERE tenant_id=$1$$;ALTER FUNCTION test_quota_caps_escalation(bigint)OWNER TO urmail_platform;GRANT EXECUTE ON FUNCTION test_quota_caps_escalation(bigint)TO urmail_app`); err != nil {
		t.Fatal(err)
	}
	tx, err = app.UserTx(t.Context(), tenant, actor)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), "SELECT test_quota_caps_escalation($1)", tenant); err == nil || !strings.Contains(err.Error(), "AI_QUOTA_PLATFORM_REQUIRED") {
		t.Fatal("tenant escaped caps guard through definer", err)
	}
	_ = tx.Rollback(t.Context())
	var exists bool
	if err = platform.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM ai_provider_configs WHERE tenant_id=$1 AND nonplatform_call_limit=3)", tenant).Scan(&exists); err != nil || !exists {
		t.Fatal("failed escalation changed data", err)
	}
}

func TestAIQuota151RestoreHoldKeepsQuotaTimezoneAndUnknownUsage(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	tenant, _ := seedAIQuotaDatabaseTenant(t, platform)
	q := store.New(platform)
	if err := q.PlatformSetNonplatformAIQuota(t.Context(), store.PlatformSetNonplatformAIQuotaParams{TenantID: tenant, NonplatformCallLimit: 3, NonplatformMailboxCallLimit: 2, NonplatformConcurrencyLimit: 1, QuotaTimezone: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	var mailbox int64
	if err := platform.QueryRow(t.Context(), `WITH domain AS (INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at)VALUES($1,'restore-quota.fixture.test','active','fixture',now())RETURNING id) INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash)SELECT $1,id,'owned','owned@restore-quota.fixture.test','fixture' FROM domain RETURNING id`, tenant).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(t.Context(), `INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,status,reserved_tokens,reserved_cost_micros)VALUES($1,$2,'restore-quota-unknown-request',repeat('c',64),now()-interval '1 day',now()+interval '30 days','byo','openai','owned','CNY',0,0,'reserved',500,0)`, tenant, mailbox); err != nil {
		t.Fatal(err)
	}
	before, err := q.PlatformRestoreSyncEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.HoldRestoredSideEffects(t.Context(), platform.Pool.Config().ConnString(), "ai-quota151-controlled-restore"); err != nil {
		t.Fatal(err)
	}
	after, err := q.PlatformRestoreSyncEpoch(t.Context())
	if err != nil || after == before {
		t.Fatal("restore epoch was not replaced", before, after, err)
	}
	cfg, err := q.PlatformGetAIProvider(t.Context(), tenant)
	if err != nil || cfg.NonplatformCallLimit != 3 || cfg.NonplatformMailboxCallLimit != 2 || cfg.QuotaTimezone != "Asia/Shanghai" {
		t.Fatal("restore changed frozen quota configuration", err)
	}
	var status string
	var reserved int64
	if err = platform.QueryRow(t.Context(), "SELECT status,reserved_tokens FROM ai_usage_requests WHERE tenant_id=$1 AND request_key='restore-quota-unknown-request'", tenant).Scan(&status, &reserved); err != nil || status != "uncertain" || reserved != 500 {
		t.Fatal("restore released or reset unknown usage", status, reserved, err)
	}
	var count int
	if err = platform.QueryRow(t.Context(), "SELECT count(*) FROM ai_usage_requests WHERE tenant_id=$1 AND mode IN('byo','local')AND status<>'released'", tenant).Scan(&count); err != nil || count != 1 {
		t.Fatal("restore minted another call allowance", count, err)
	}
}
