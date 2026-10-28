package marketing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	flow "github.com/bushixiucai/urmail/internal/domain/marketingflow"
	"github.com/bushixiucai/urmail/internal/store"
)

func versionedRuntimeFixture(t *testing.T, f campaignFixture, name string) (int64, int64) {
	t.Helper()
	ctx := t.Context()
	q := store.New(f.platform.Pool)
	d := flow.Definition{SchemaVersion: 1, EventSource: "contact_enrolled", Entry: "send", Nodes: []flow.Node{{ID: "send", Kind: "send", TemplateID: 1, Next: "end"}, {ID: "end", Kind: "stop"}}}
	encoded, _ := json.Marshal(d)
	definition, err := q.CreateMarketingFlowDefinition(ctx, store.CreateMarketingFlowDefinitionParams{TenantID: f.tenant, Name: name, Draft: encoded})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := q.CreateMarketingRuntimeFlow(ctx, store.CreateMarketingRuntimeFlowParams{TenantID: f.tenant, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	for index, n := range d.Nodes {
		if err = q.CreateMarketingRuntimeStep(ctx, store.CreateMarketingRuntimeStepParams{TenantID: f.tenant, FlowID: runtime, StepKey: n.ID, Kind: n.Kind, NextKey: n.Next, Position: int32(index)}); err != nil {
			t.Fatal(err)
		}
	} // #nosec G115 -- fixed two-node fixture.
	if err = q.CreateMarketingFlowContentSnapshot(ctx, store.CreateMarketingFlowContentSnapshotParams{TenantID: f.tenant, RuntimeFlowID: runtime, StepKey: "send", TemplateID: 1, Subject: "Fixed flow", TextBody: "Fixed flow body"}); err != nil {
		t.Fatal(err)
	}
	version, err := q.CreateMarketingFlowVersion(ctx, store.CreateMarketingFlowVersionParams{TenantID: f.tenant, DefinitionID: definition.ID, Version: 1, DraftRevision: 1, RuntimeFlowID: runtime, Definition: encoded, PublishedBy: f.owner})
	if err != nil {
		t.Fatal(err)
	}
	var list int64
	if err = f.platform.QueryRow(ctx, "SELECT list_id FROM marketing_campaigns WHERE tenant_id=$1 AND id=$2", f.tenant, f.campaign).Scan(&list); err != nil {
		t.Fatal(err)
	}
	entry, err := q.CreateMarketingFlowEntry(ctx, store.CreateMarketingFlowEntryParams{TenantID: f.tenant, DefinitionID: definition.ID, VersionID: version.ID, EventKey: name + "-event-0001", ListID: list, RecipientCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateMarketingFlowInstance(ctx, store.CreateMarketingFlowInstanceParams{TenantID: f.tenant, FlowID: runtime, Email: "recipient-0@example.test", StepKey: "send", EntryID: pgtype.Int8{Int64: entry.ID, Valid: true}, EventFacts: []byte(`{"source":"contact_enrolled","email_domain":"example.test","consented":"true"}`)}); err != nil {
		t.Fatal(err)
	}
	return definition.ID, runtime
}

func TestVersionedFlowSharesBudgetAndParksUncertainAcrossRestart(t *testing.T) {
	f := marketingFixture(t, 2, 1)
	ctx := t.Context()
	q := store.New(f.platform.Pool)
	definition, runtime := versionedRuntimeFixture(t, f, "First")
	channel, err := q.PlatformGetActiveMarketingChannel(ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := q.PlatformClaimMarketingFlowPositions(ctx, store.PlatformClaimMarketingFlowPositionsParams{Batch: 1, LeaseOwner: "first-worker", LeaseSeconds: 120})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%v %v", claimed, err)
	}
	p := claimed[0]
	begin := func(owner string, fence int64) string {
		t.Helper()
		result, callErr := q.PlatformBeginMarketingFlowSend(ctx, store.PlatformBeginMarketingFlowSendParams{PTenant: f.tenant, PFlow: runtime, PEmail: p.Email, POwner: owner, PFence: fence, PChannel: channel.ID})
		if callErr != nil {
			t.Fatal(callErr)
		}
		return result
	}
	if got := begin("first-worker", p.LeaseFence+1); got != "stale_lease" {
		t.Fatalf("stale begin=%s", got)
	}
	if got := begin("first-worker", p.LeaseFence); got != "authorized" {
		t.Fatalf("begin=%s", got)
	}
	var used int64
	var uncertain, reserved bool
	if err = f.platform.QueryRow(ctx, "SELECT used_count FROM marketing_quota_windows WHERE tenant_id=$1", f.tenant).Scan(&used); err != nil || used != 1 {
		t.Fatalf("shared quota=%d %v", used, err)
	}
	if _, err = f.platform.Exec(ctx, "UPDATE marketing_flow_positions SET lease_until=now()-interval '1 second',updated_at=now()-interval '1 hour' WHERE tenant_id=$1 AND flow_id=$2", f.tenant, runtime); err != nil {
		t.Fatal(err)
	}
	worker := newTestFlowWorker(t, f.platform, func(context.Context, int64, int64, string, string) error { t.Fatal("uncertain replayed"); return nil })
	if _, err = worker.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.platform.QueryRow(ctx, "SELECT delivery_uncertain,flow_reserved FROM marketing_flow_positions WHERE tenant_id=$1 AND flow_id=$2", f.tenant, runtime).Scan(&uncertain, &reserved); err != nil || !uncertain || !reserved {
		t.Fatalf("uncertain/reservation lost=%v/%v %v", uncertain, reserved, err)
	}
	result, err := q.PlatformCommitMarketingFlowFenced(ctx, store.PlatformCommitMarketingFlowFencedParams{PTenant: f.tenant, PFlow: runtime, PEmail: p.Email, POwner: "first-worker", PFence: p.LeaseFence, PNext: "end", PConfirmed: true})
	if err != nil || result != "stale_lease" {
		t.Fatalf("late commit=%s %v", result, err)
	}
	tx, err := f.app.UserTx(ctx, f.tenant, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := store.New(tx).RetryMarketingFlowInstance(ctx, store.RetryMarketingFlowInstanceParams{PTenant: f.tenant, PDefinition: definition, PPosition: scalarFlowPosition(t, f, runtime), PConfirmedNotAccepted: true})
	if err != nil || !retried {
		_ = tx.Rollback(ctx)
		t.Fatalf("explicit retry=%v %v", retried, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.platform.QueryRow(ctx, "SELECT used_count FROM marketing_quota_windows WHERE tenant_id=$1", f.tenant).Scan(&used); err != nil || used != 0 {
		t.Fatalf("confirmed non-acceptance did not release quota=%d %v", used, err)
	}
	claimed, err = q.PlatformClaimMarketingFlowPositions(ctx, store.PlatformClaimMarketingFlowPositionsParams{Batch: 1, LeaseOwner: "second-worker", LeaseSeconds: 120})
	if err != nil || len(claimed) != 1 {
		t.Fatal("retry not claimable", err)
	}
	p = claimed[0]
	if got := begin("second-worker", p.LeaseFence); got != "authorized" {
		t.Fatal(got)
	}
	result, err = q.PlatformCommitMarketingFlowFenced(ctx, store.PlatformCommitMarketingFlowFencedParams{PTenant: f.tenant, PFlow: runtime, PEmail: p.Email, POwner: "second-worker", PFence: p.LeaseFence, PNext: "end", PConfirmed: true})
	if err != nil || result != "end" {
		t.Fatalf("confirmed commit=%s %v", result, err)
	}
	var campaigns int
	campaign := f.worker(t, func(context.Context, SMTPConfig, Delivery) error { campaigns++; return nil })
	if _, err = campaign.Step(ctx); err != nil || campaigns != 1 {
		t.Fatalf("campaign shared budget=%d %v", campaigns, err)
	}
	if err = f.platform.QueryRow(ctx, "SELECT used_count FROM marketing_quota_windows WHERE tenant_id=$1", f.tenant).Scan(&used); err != nil || used != 2 {
		t.Fatalf("flow and campaign used separate quota=%d %v", used, err)
	}
	_, second := versionedRuntimeFixture(t, f, "Second")
	claimed, err = q.PlatformClaimMarketingFlowPositions(ctx, store.PlatformClaimMarketingFlowPositionsParams{Batch: 1, LeaseOwner: "third-worker", LeaseSeconds: 120})
	if err != nil || len(claimed) != 1 {
		t.Fatal("second flow claim", err)
	}
	next := claimed[0]
	blocked, err := q.PlatformBeginMarketingFlowSend(ctx, store.PlatformBeginMarketingFlowSendParams{PTenant: f.tenant, PFlow: second, PEmail: next.Email, POwner: "third-worker", PFence: next.LeaseFence, PChannel: channel.ID})
	if err != nil || blocked != "quota_exhausted" {
		t.Fatalf("budget bypass=%s %v", blocked, err)
	}
	if _, err = f.platform.Exec(ctx, "UPDATE marketing_quota_windows SET used_count=0,next_send_at=now()+interval '1 hour' WHERE tenant_id=$1", f.tenant); err != nil {
		t.Fatal(err)
	}
	blocked, err = q.PlatformBeginMarketingFlowSend(ctx, store.PlatformBeginMarketingFlowSendParams{PTenant: f.tenant, PFlow: second, PEmail: next.Email, POwner: "third-worker", PFence: next.LeaseFence, PChannel: channel.ID})
	if err != nil || blocked != "rate_limited" {
		t.Fatalf("rate bypass=%s %v", blocked, err)
	}
}
func scalarFlowPosition(t *testing.T, f campaignFixture, runtime int64) int64 {
	t.Helper()
	var id int64
	if err := f.platform.QueryRow(t.Context(), "SELECT id FROM marketing_flow_positions WHERE tenant_id=$1 AND flow_id=$2", f.tenant, runtime).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
