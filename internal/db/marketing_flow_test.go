package db_test

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestMarketingUnsubscribeIsTerminal(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('flow','Flow') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'F','flow-f','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	flow, err := q.PlatformInsertMarketingFlow(ctx, store.PlatformInsertMarketingFlowParams{TenantID: tenant, Name: "welcome"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.PlatformInsertMarketingFlowStep(ctx, store.PlatformInsertMarketingFlowStepParams{TenantID: tenant, FlowID: flow, StepKey: "stop", Kind: "stop", Position: 0}); err != nil {
		t.Fatal(err)
	}
	row, err := q.PlatformAdvanceMarketingFlow(ctx, store.PlatformAdvanceMarketingFlowParams{TenantID: tenant, FlowID: flow, Email: "a@example.test", StepKey: "stop", Unsubscribed: true})
	if err != nil || !row.Unsubscribed {
		t.Fatal(err)
	}
	row, err = q.PlatformAdvanceMarketingFlow(ctx, store.PlatformAdvanceMarketingFlowParams{TenantID: tenant, FlowID: flow, Email: "a@example.test", StepKey: "later", Unsubscribed: false})
	if err != nil || row.StepKey != "stop" || !row.Unsubscribed {
		t.Fatalf("%+v %v", row, err)
	}
}

func TestMarketingFlowStopsWhenSendingStops(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, flow int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('gate','Gate') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'G','gate-g','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO marketing_flows(tenant_id,name) VALUES($1,'flow') RETURNING id", tenant).Scan(&flow); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key) VALUES($1,$2,'a@example.test','send')", tenant, flow); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err = tx.QueryRow(ctx, "SELECT urmail_marketing_flow_may_advance($1,$2,'a@example.test')", tenant, flow).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE subscriptions SET period_start=now()-interval '40 days', period_end=now()-interval '8 days' WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	var after bool
	if err = tx.QueryRow(ctx, "SELECT urmail_marketing_flow_may_advance($1,$2,'a@example.test')", tenant, flow).Scan(&after); err != nil || after || !allowed {
		t.Fatalf("before=%v after=%v err=%v", allowed, after, err)
	}
}

func TestBlockedMarketingStepStaysPut(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, flow int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('stay','Stay') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'S','stay-s','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO marketing_flows(tenant_id,name) VALUES($1,'flow') RETURNING id", tenant).Scan(&flow); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key) VALUES($1,$2,'a@example.test','welcome')", tenant, flow); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE subscriptions SET period_start=now()-interval '40 days', period_end=now()-interval '8 days' WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	// 0101 semantics: a gate-blocked commit is definitive ('blocked'), and the
	// position stays on its stored step. 'uncertain' is reserved for a send
	// whose outcome could not be proven (urmail_mark_marketing_flow_uncertain).
	var result string
	if err = tx.QueryRow(ctx, "SELECT urmail_commit_marketing_flow_step($1,$2,'a@example.test','send','next')", tenant, flow).Scan(&result); err != nil || result != "blocked" {
		t.Fatalf("%s %v", result, err)
	}
	var step, uncertain string
	if err = tx.QueryRow(ctx, "SELECT step_key, delivery_uncertain::text FROM marketing_flow_positions WHERE flow_id=$1", flow).Scan(&step, &uncertain); err != nil || step != "welcome" || uncertain != "false" {
		t.Fatalf("step=%s uncertain=%s err=%v", step, uncertain, err)
	}
}

func TestSendableCheckDoesNotMoveStep(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, flow int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('check','Check') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'C','check-c','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO marketing_flows(tenant_id,name) VALUES($1,'flow') RETURNING id", tenant).Scan(&flow); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key) VALUES($1,$2,'a@example.test','welcome')", tenant, flow); err != nil {
		t.Fatal(err)
	}
	var result, step string
	if err = tx.QueryRow(ctx, "SELECT urmail_marketing_flow_sendable($1,$2,'a@example.test')", tenant, flow).Scan(&result); err != nil || result != "welcome" {
		t.Fatalf("%s %v", result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT step_key FROM marketing_flow_positions WHERE flow_id=$1", flow).Scan(&step); err != nil || step != "welcome" {
		t.Fatalf("%s %v", step, err)
	}
}

func TestMarketingWaitBlocksUntilDue(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, flow int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('wait','Wait') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'W','wait-w','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO marketing_flows(tenant_id,name) VALUES($1,'flow') RETURNING id", tenant).Scan(&flow); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,wait_until) VALUES($1,$2,'a@example.test','wait',now()+interval '1 hour')", tenant, flow); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = tx.QueryRow(ctx, "SELECT urmail_commit_marketing_flow_step($1,$2,'a@example.test','send','next')", tenant, flow).Scan(&result); err != nil || result != "blocked" {
		t.Fatalf("%s %v", result, err)
	}
	var step string
	if err = tx.QueryRow(ctx, "SELECT step_key FROM marketing_flow_positions WHERE flow_id=$1", flow).Scan(&step); err != nil || step != "wait" {
		t.Fatalf("%s %v", step, err)
	}
}

func TestDueMarketingWaitAdvancesAndClears(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, flow int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('due','Due') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'D','due-d','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO marketing_flows(tenant_id,name) VALUES($1,'flow') RETURNING id", tenant).Scan(&flow); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,wait_until) VALUES($1,$2,'a@example.test','wait',now()-interval '1 minute')", tenant, flow); err != nil {
		t.Fatal(err)
	}
	var result, step string
	var wait any
	if err = tx.QueryRow(ctx, "SELECT urmail_commit_marketing_flow_step($1,$2,'a@example.test','none','send')", tenant, flow).Scan(&result); err != nil || result != "send" {
		t.Fatalf("%s %v", result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT step_key, wait_until FROM marketing_flow_positions WHERE flow_id=$1", flow).Scan(&step, &wait); err != nil || step != "send" || wait != nil {
		t.Fatalf("%s %#v %v", step, wait, err)
	}
}

func TestUncertainMarketingPositionDoesNotAdvance(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, flow int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('uncertain','Uncertain') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'U','uncertain-u','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO marketing_flows(tenant_id,name) VALUES($1,'flow') RETURNING id", tenant).Scan(&flow); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key) VALUES($1,$2,'a@example.test','welcome')", tenant, flow); err != nil {
		t.Fatal(err)
	}
	var marked, result, step string
	if err = tx.QueryRow(ctx, "SELECT urmail_mark_marketing_flow_uncertain($1,$2,'a@example.test')", tenant, flow).Scan(&marked); err != nil || marked != "uncertain" {
		t.Fatalf("%s %v", marked, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_marketing_flow_sendable($1,$2,'a@example.test')", tenant, flow).Scan(&result); err != nil || result != "uncertain" {
		t.Fatalf("%s %v", result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_commit_marketing_flow_step($1,$2,'a@example.test','send','next')", tenant, flow).Scan(&result); err != nil || result != "uncertain" {
		t.Fatalf("%s %v", result, err)
	}
	if err = tx.QueryRow(ctx, "SELECT step_key FROM marketing_flow_positions WHERE flow_id=$1", flow).Scan(&step); err != nil || step != "welcome" {
		t.Fatalf("%s %v", step, err)
	}
}

func TestMarketingUncertainQueryDoesNotMoveStep(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, flow int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('markq','Mark') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'M','mark-m','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO marketing_flows(tenant_id,name) VALUES($1,'flow') RETURNING id", tenant).Scan(&flow); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key) VALUES($1,$2,'a@example.test','welcome')", tenant, flow); err != nil {
		t.Fatal(err)
	}
	result, err := store.New(tx).PlatformMarkMarketingFlowUncertain(ctx, store.PlatformMarkMarketingFlowUncertainParams{PTenant: tenant, PFlow: flow, PEmail: "a@example.test"})
	if err != nil || result != "uncertain" {
		t.Fatalf("%s %v", result, err)
	}
	var step string
	if err = tx.QueryRow(ctx, "SELECT step_key FROM marketing_flow_positions WHERE flow_id=$1", flow).Scan(&step); err != nil || step != "welcome" {
		t.Fatalf("%s %v", step, err)
	}
}
