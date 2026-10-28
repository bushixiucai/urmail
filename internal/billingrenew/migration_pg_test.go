package billingrenew

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/migrations"
)

func TestRecurringMigrationRollbackReapplyPG(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	up, err := migrations.FS.ReadFile("0138_recurring_enrollment.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0138_recurring_enrollment.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = platform.Exec(t.Context(), string(down)); err != nil {
			t.Fatal(err)
		}
		if _, err = platform.Exec(t.Context(), string(up)); err != nil {
			t.Fatal(err)
		}
	}
	// Role ownership and FORCE RLS remain authoritative after repeated upgrade.
	var forced bool
	if err = platform.QueryRow(t.Context(), "SELECT relforcerowsecurity FROM pg_class WHERE oid='subscription_renewal_enrollments'::regclass").Scan(&forced); err != nil || !forced {
		t.Fatalf("RLS rollback drift %v %v", forced, err)
	}
	// A recorded financial intent makes destructive down migration refuse before
	// touching consent, receipt or intent facts.
	var brand, tenant, subscription int64
	if err = platform.QueryRow(t.Context(), "INSERT INTO brands(slug,name) VALUES('recurring-down','Recurring rollback') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(t.Context(), "INSERT INTO tenants(brand_id,name,slug) VALUES($1,'Rollback','recurring-down') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = platform.QueryRow(t.Context(), "SELECT id FROM subscriptions WHERE tenant_id=$1", tenant).Scan(&subscription); err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(t.Context(), "INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state,side_effect_intent_at) VALUES($1,$2,now(),'uncertain',now())", tenant, subscription); err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Exec(t.Context(), string(down)); err == nil || !strings.Contains(err.Error(), "RECURRING_FINANCIAL_HISTORY_REQUIRES_FORWARD_SCHEMA") {
		t.Fatalf("financial history was discarded: %v", err)
	}
	var state string
	if err = platform.QueryRow(t.Context(), "SELECT state FROM subscription_renewal_attempts WHERE tenant_id=$1", tenant).Scan(&state); err != nil || state != "uncertain" {
		t.Fatalf("failed down mutated intent: %q %v", state, err)
	}
}
