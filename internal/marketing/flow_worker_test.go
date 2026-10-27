package marketing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/marketingflow"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func newTestFlowWorker(t *testing.T, pool *db.Pool, sender marketingflow.Sender) *FlowWorker {
	t.Helper()
	w, err := NewFlowWorker(FlowOptions{Pool: pool, Sender: sender, Enabled: true, Lease: 30 * time.Second, Batch: 10})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func setupFlow(t *testing.T, pool *db.Pool, steps [][3]string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var brand, tenant, flow int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('tick','Tick') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'T','tick-t','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO marketing_flows(tenant_id,name,status) VALUES($1,'welcome','active') RETURNING id", tenant).Scan(&flow); err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	for i, s := range steps {
		if err = q.PlatformInsertMarketingFlowStep(ctx, store.PlatformInsertMarketingFlowStepParams{TenantID: tenant, FlowID: flow, StepKey: s[0], Kind: s[1], NextKey: s[2], Position: int32(i)}); err != nil {
			t.Fatal(err)
		}
		if s[0] == "s2" {
			if _, err = tx.Exec(ctx, "UPDATE marketing_flow_steps SET wait_seconds=1 WHERE tenant_id=$1 AND flow_id=$2 AND step_key='s2'", tenant, flow); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return tenant, flow
}

func stepPosition(t *testing.T, pool *db.Pool, tenant, flow int64, email, step string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key) VALUES($1,$2,$3,$4)", tenant, flow, email, step); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func positionState(t *testing.T, pool *db.Pool, tenant, flow int64, email string) (string, bool, *time.Time, bool) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var step string
	var unsub, uncertain bool
	var wait *time.Time
	if err = tx.QueryRow(ctx, "SELECT step_key,unsubscribed,wait_until,delivery_uncertain FROM marketing_flow_positions WHERE tenant_id=$1 AND flow_id=$2 AND email=$3", tenant, flow, email).Scan(&step, &unsub, &wait, &uncertain); err != nil {
		t.Fatal(err)
	}
	return step, unsub, wait, uncertain
}

func TestFlowTickDisabled(t *testing.T) {
	w, err := NewFlowWorker(FlowOptions{Pool: &db.Pool{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Tick(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled tick: %v", err)
	}
}

func TestFlowTickSendWaitSend(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	var sends atomic.Int64
	sender := func(context.Context, int64, int64, string, string) error { sends.Add(1); return nil }
	w := newTestFlowWorker(t, platform, sender)
	tenant, flow := setupFlow(t, platform, [][3]string{{"s1", "send", "s2"}, {"s2", "wait", "s3"}, {"s3", "send", "s4"}, {"s4", "stop", ""}})
	stepPosition(t, platform, tenant, flow, "a@example.test", "s1")

	if n, err := w.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick1: %d %v", n, err)
	}
	step, _, wait, _ := positionState(t, platform, tenant, flow, "a@example.test")
	if step != "s2" || sends.Load() != 1 || wait == nil || wait.Before(time.Now()) {
		t.Fatalf("after tick1: step=%s sends=%d wait=%v", step, sends.Load(), wait)
	}
	// While waiting, no claim and no send.
	if n, err := w.Tick(ctx); err != nil || n != 0 || sends.Load() != 1 {
		t.Fatalf("tick2 during wait: %d %v sends=%d", n, err, sends.Load())
	}
	time.Sleep(1100 * time.Millisecond)
	if n, err := w.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick3: %d %v", n, err)
	}
	step, _, wait, _ = positionState(t, platform, tenant, flow, "a@example.test")
	if step != "s3" || sends.Load() != 1 || wait != nil {
		t.Fatalf("after wait expiry: step=%s sends=%d wait=%v", step, sends.Load(), wait)
	}
	if n, err := w.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick4: %d %v", n, err)
	}
	step, _, _, _ = positionState(t, platform, tenant, flow, "a@example.test")
	if step != "s4" || sends.Load() != 2 {
		t.Fatalf("after second send: step=%s sends=%d", step, sends.Load())
	}
	// The stop step is terminal: never claimed again.
	if n, err := w.Tick(ctx); err != nil || n != 0 || sends.Load() != 2 {
		t.Fatalf("tick5 on stop: %d %v sends=%d", n, err, sends.Load())
	}
}

func TestFlowTickUnsubscribedDuringWait(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	var sends atomic.Int64
	w := newTestFlowWorker(t, platform, func(context.Context, int64, int64, string, string) error { sends.Add(1); return nil })
	tenant, flow := setupFlow(t, platform, [][3]string{{"s1", "send", "s2"}, {"s2", "wait", "s3"}, {"s3", "stop", ""}})
	stepPosition(t, platform, tenant, flow, "a@example.test", "s1")
	if _, err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE marketing_flow_positions SET unsubscribed=true,wait_until=now()-interval '1 minute' WHERE flow_id=$1", flow); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("tick after unsubscribe: %d %v", n, err)
	}
	step, unsub, _, _ := positionState(t, platform, tenant, flow, "a@example.test")
	if step != "s2" || !unsub || sends.Load() != 1 {
		t.Fatalf("unsubscribed position moved or resent: step=%s unsub=%v sends=%d", step, unsub, sends.Load())
	}
}

func TestFlowTickUncertainRecovers(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	var sends atomic.Int64
	var uncertain atomic.Bool
	w := newTestFlowWorker(t, platform, func(context.Context, int64, int64, string, string) error {
		if uncertain.Load() {
			sends.Add(1)
			return nil
		}
		return marketingflow.ErrUncertain
	})
	tenant, flow := setupFlow(t, platform, [][3]string{{"s1", "send", "s2"}, {"s2", "stop", ""}})
	stepPosition(t, platform, tenant, flow, "a@example.test", "s1")

	if n, err := w.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick1: %d %v", n, err)
	}
	step, _, _, unc := positionState(t, platform, tenant, flow, "a@example.test")
	if step != "s1" || !unc || sends.Load() != 0 {
		t.Fatalf("uncertain position not kept: step=%s uncertain=%v", step, unc)
	}
	// Frozen: no claim while the quiet period runs.
	if n, err := w.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("tick2 frozen: %d %v", n, err)
	}
	// Age past the ten-minute quiet period and recover: the position resumes
	// on its current step and the send is retried (at-least-once).
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE marketing_flow_positions SET updated_at=now()-interval '11 minutes' WHERE flow_id=$1", flow); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	uncertain.Store(true)
	if n, err := w.Tick(ctx); err != nil || n != 1 || sends.Load() != 1 {
		t.Fatalf("recovery tick: %d %v sends=%d", n, err, sends.Load())
	}
	step, _, _, unc = positionState(t, platform, tenant, flow, "a@example.test")
	if step != "s2" || unc {
		t.Fatalf("after recovery: step=%s uncertain=%v", step, unc)
	}
}

func TestFlowTickConcurrentClaimsDoNotDoubleSend(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	var sends atomic.Int64
	w1 := newTestFlowWorker(t, platform, func(context.Context, int64, int64, string, string) error {
		sends.Add(1)
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	w2 := newTestFlowWorker(t, platform, func(context.Context, int64, int64, string, string) error { sends.Add(1); return nil })
	tenant, flow := setupFlow(t, platform, [][3]string{{"s1", "send", "s2"}, {"s2", "stop", ""}})
	stepPosition(t, platform, tenant, flow, "a@example.test", "s1")

	var wg sync.WaitGroup
	var n1, n2 int
	var e1, e2 error
	wg.Add(2)
	go func() { defer wg.Done(); n1, e1 = w1.Tick(context.Background()) }()
	go func() { defer wg.Done(); n2, e2 = w2.Tick(context.Background()) }()
	wg.Wait()
	if e1 != nil || e2 != nil {
		t.Fatalf("ticks: %v %v", e1, e2)
	}
	if n1+n2 != 1 || sends.Load() != 1 {
		t.Fatalf("concurrent ticks claimed %d positions, %d sends", n1+n2, sends.Load())
	}
	step, _, _, _ := positionState(t, platform, tenant, flow, "a@example.test")
	if step != "s2" {
		t.Fatalf("position did not advance: %s", step)
	}
}
