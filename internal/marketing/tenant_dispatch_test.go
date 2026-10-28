package marketing

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/bushixiucai/urmail/internal/store"
)

// fairTenant adds a second tenant to the same real database. It deliberately
// uses the queue's existing constraints; no sender or network is invoked.
func fairTenant(t *testing.T, f campaignFixture) campaignFixture {
	t.Helper()
	ctx := t.Context()
	p := f.platform
	other := campaignFixture{platform: p, app: f.app, owner: f.owner, box: f.box}
	if err := p.QueryRow(ctx, `INSERT INTO tenants(brand_id,owner_user_id,name,slug,status) SELECT brand_id,owner_user_id,'Fair second','fair-second','active' FROM tenants WHERE id=$1 RETURNING id`, f.tenant).Scan(&other.tenant); err != nil {
		t.Fatal(err)
	}
	var channel, template, list int64
	if err := p.QueryRow(ctx, `INSERT INTO marketing_channels(tenant_id,name,kind,sender_address,encrypted_config,enabled) SELECT $1,'Fair gateway',kind,sender_address,encrypted_config,enabled FROM marketing_channels WHERE tenant_id=$2 LIMIT 1 RETURNING id`, other.tenant, f.tenant).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO marketing_templates(tenant_id,name,subject,text_body) VALUES($1,'Fair template','Fair','Bounded fairness fixture') RETURNING id`, other.tenant).Scan(&template); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO marketing_lists(tenant_id,name) VALUES($1,'Fair list') RETURNING id`, other.tenant).Scan(&list); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO marketing_campaigns(tenant_id,name,channel_id,template_id,list_id,status,scheduled_at,subject_snapshot,body_snapshot) VALUES($1,'Fair campaign',$2,$3,$4,'running',now()-interval '1 minute','Fair','Bounded fixture') RETURNING id`, other.tenant, channel, template, list).Scan(&other.campaign); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO marketing_recipients(tenant_id,campaign_id,email) VALUES($1,$2,'second@example.test')`, other.tenant, other.campaign); err != nil {
		t.Fatal(err)
	}
	return other
}

func TestTenantDispatchPreventsLowIDBacklogStarvationPG(t *testing.T) {
	f := marketingFixture(t, 1000, 20)
	other := fairTenant(t, f)
	q := store.New(f.platform)
	for index, want := range []int64{f.tenant, other.tenant, f.tenant} {
		row, err := q.PlatformClaimMarketingRecipient(t.Context(), fmt.Sprintf("fair-worker-%d", index))
		if err != nil || row.TenantID != want {
			t.Fatalf("small tenant starved behind earlier backlog: claim %d tenant=%d want=%d err=%v", index, row.TenantID, want, err)
		}
	}
	// Reconstructing the query client has no effect on the durable cursor.
	q = store.New(f.platform)
	for i := range 12 {
		flowFixture(t, f, fmt.Sprintf("flow-%d@example.test", i))
	}
	flowFixture(t, other, "second-flow@example.test")
	if _, err := f.platform.Exec(t.Context(), "UPDATE marketing_flows SET status='active' WHERE tenant_id IN($1,$2)", f.tenant, other.tenant); err != nil {
		t.Fatal(err)
	}
	rows, err := q.PlatformClaimMarketingFlowPositions(t.Context(), store.PlatformClaimMarketingFlowPositionsParams{Batch: 4, LeaseOwner: "fair-flow", LeaseSeconds: 120})
	if err != nil || len(rows) != 4 {
		t.Fatalf("bounded fair batch: %d %v", len(rows), err)
	}
	foundSecond := false
	for _, row := range rows {
		if row.TenantID == other.tenant {
			foundSecond = true
		}
	}
	if !foundSecond {
		t.Fatal("flow batch starved a tenant behind earlier IDs")
	}
	rows, err = q.PlatformClaimMarketingFlowPositions(t.Context(), store.PlatformClaimMarketingFlowPositionsParams{Batch: 4, LeaseOwner: "fair-flow-next", LeaseSeconds: 120})
	if err != nil || len(rows) != 4 {
		t.Fatalf("single-tenant batch throughput reduced: %d %v", len(rows), err)
	}
	if _, err = f.app.Exec(t.Context(), "SELECT * FROM platform_marketing_dispatch"); err == nil {
		t.Fatal("tenant role read platform dispatch state")
	}
}

func TestConcurrentTenantClaimsStayFencedAndDistinctPG(t *testing.T) {
	f := marketingFixture(t, 1000, 20)
	other := fairTenant(t, f)
	var wait sync.WaitGroup
	var lock sync.Mutex
	ids := map[int64]bool{}
	tenants := map[int64]bool{}
	errors := []error{}
	for i := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			row, err := store.New(f.platform).PlatformClaimMarketingRecipient(context.Background(), fmt.Sprintf("parallel-%d", i))
			lock.Lock()
			defer lock.Unlock()
			if err != nil {
				errors = append(errors, err)
				return
			}
			if ids[row.ID] {
				errors = append(errors, fmt.Errorf("duplicate recipient %d", row.ID))
			}
			ids[row.ID] = true
			tenants[row.TenantID] = true
		}()
	}
	wait.Wait()
	if len(errors) > 0 || len(ids) != 2 {
		t.Fatalf("concurrent claims duplicated or failed: %v %v", ids, errors)
	}
	// Concurrent statement snapshots may briefly see the previous cursor.
	// Fresh subsequent claims must serve the small tenant within a bounded turn.
	for i := 0; i < 2 && !tenants[other.tenant]; i++ {
		row, err := store.New(f.platform).PlatformClaimMarketingRecipient(t.Context(), fmt.Sprintf("after-concurrent-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		tenants[row.TenantID] = true
	}
	if !tenants[f.tenant] || !tenants[other.tenant] {
		t.Fatalf("concurrent dispatch starved a tenant: %v", tenants)
	}
}
