package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type countedAI struct {
	calls   atomic.Int64
	unknown bool
	fail    bool
	delay   time.Duration
}

func (p *countedAI) Name() string { return "offline-model" }
func (p *countedAI) Complete(ctx context.Context, _ ai.CompletionRequest) (ai.CompletionResponse, error) {
	p.calls.Add(1)
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return ai.CompletionResponse{}, ctx.Err()
		}
	}
	if p.fail {
		return ai.CompletionResponse{}, errors.New("provider unavailable")
	}
	return ai.CompletionResponse{Content: `{"summary":"Safe offline suggestion"}`, PromptTokens: 10, OutputTokens: 5, TotalTokens: 15, UsageKnown: !p.unknown}, nil
}

func mailboxAIFixture(t *testing.T, calls int64, consentOption ...bool) (*db.Pool, store.PlatformConsumeMailboxTicketRow) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if err = m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	// calls==0 means unconfigured: no host caps and no settings quota, so AI
	// must fail closed instead of receiving a zero-sized window.
	if calls > 0 {
		if _, err = p.Exec(ctx, "INSERT INTO settings(scope,scope_id,key,value) VALUES('platform',0,'ai_default_call_limit',to_jsonb($1::bigint))", calls); err != nil {
			t.Fatal(err)
		}
	}
	var brand, tenant, domain, mailbox int64
	for _, seed := range []struct {
		sql    string
		args   []any
		target *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('ai-test','AI') RETURNING id", nil, &brand},
		{"INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'AI','ai-test','active') RETURNING id", []any{&brand}, &tenant},
	} {
		if len(seed.args) > 0 {
			seed.args = []any{brand}
		}
		if err = p.QueryRow(ctx, seed.sql, seed.args...).Scan(seed.target); err != nil {
			t.Fatal(err)
		}
	}
	if err = p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'ai.example.test','active','test',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'independent','independent@ai.example.test','test') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	_, hash, err := mailboxprincipal.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(p)
	if _, err = q.PlatformCreateMailboxSession(ctx, store.PlatformCreateMailboxSessionParams{TokenHash: hash, Address: "independent@ai.example.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformSetAIProvider(ctx, store.PlatformSetAIProviderParams{TenantID: tenant, Mode: "platform", Provider: "openai", Endpoint: "https://offline.invalid/v1", Model: "offline", Enabled: true, InputMicrosPerMillion: 1000000, OutputMicrosPerMillion: 1000000}); err != nil {
		t.Fatal(err)
	}
	var owner int64
	if err = p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('ai-owner@example.test','test') RETURNING id").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", owner, tenant); err != nil {
		t.Fatal(err)
	}
	if len(consentOption) == 0 || consentOption[0] {
		approveMailboxAICloud(t, p, tenant, true)
	}
	return p, store.PlatformConsumeMailboxTicketRow{TenantID: tenant, MailboxID: mailbox, Address: "independent@ai.example.test", Folder: "INBOX", MessageUid: 1, UidValidity: 1, Scope: "ai_context", SessionHash: hash}
}

func TestMailboxAIBudgetConcurrentCallsAndStableIntent(t *testing.T) {
	p, principal := mailboxAIFixture(t, 2)
	model := &countedAI{delay: 30 * time.Millisecond}
	service := MailboxAIService{Pool: p, Provider: func(store.AiProviderConfigs) (ai.Provider, error) { return model, nil }}
	target := mailboxAIVersion(t, p, principal.TenantID)
	input := ai.OperationInput{Operation: "summarize", Body: "Only the authenticated mailbox selected this message."}
	var group sync.WaitGroup
	for i := range 12 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, _ = service.Run(context.Background(), principal, fmt.Sprintf("request-key-%016d", index), input, target)
		}(i)
	}
	group.Wait()
	if model.calls.Load() > 2 || model.calls.Load() == 0 {
		t.Fatalf("budget exceeded: %d", model.calls.Load())
	}
	rows, err := store.New(p).PlatformAIUsage(context.Background(), principal.TenantID)
	if err != nil || len(rows) > 2 {
		t.Fatalf("ledger %d %v", len(rows), err)
	}
	var key string
	for _, row := range rows {
		if row.Status == "completed" {
			key = row.RequestKey
			break
		}
	}
	if key == "" {
		t.Fatal("no completed reservation")
	}
	before := model.calls.Load()
	result, err := service.Run(context.Background(), principal, key, input, target)
	if err != nil || !json.Valid(result) || model.calls.Load() != before {
		t.Fatalf("idempotent replay called provider: %v", err)
	}
	input.Body = "Changed purchase intent"
	if _, err = service.Run(context.Background(), principal, key, input, target); !errors.Is(err, errAIIntent) {
		t.Fatalf("changed input %v", err)
	}
}

func TestMailboxAIUnknownUsageRemainsChargedAndCannotReplay(t *testing.T) {
	p, principal := mailboxAIFixture(t, 1)
	model := &countedAI{unknown: true}
	service := MailboxAIService{Pool: p, Provider: func(store.AiProviderConfigs) (ai.Provider, error) { return model, nil }}
	target := mailboxAIVersion(t, p, principal.TenantID)
	input := ai.OperationInput{Operation: "summarize", Body: "Private message"}
	key := "usage-unknown-intent-0001"
	if _, err := service.Run(context.Background(), principal, key, input, target); !errors.Is(err, errAIBudget) {
		t.Fatalf("unknown usage free success: %v", err)
	}
	row, err := store.New(p).PlatformGetAIRequest(context.Background(), store.PlatformGetAIRequestParams{TenantID: principal.TenantID, RequestKey: key})
	if err != nil || row.Status != "uncertain" || row.ReservedCostMicros <= 0 || row.ActualCostMicros.Valid {
		t.Fatalf("unknown reservation released: %+v %v", row, err)
	}
	if _, err = service.Run(context.Background(), principal, key, input, target); !errors.Is(err, errAIIntent) {
		t.Fatalf("unknown replay %v", err)
	}
	if _, err = service.Run(context.Background(), principal, "other-unknown-intent-0001", input, target); !errors.Is(err, errAIBudget) {
		t.Fatalf("unknown spent quota reused %v", err)
	}
	if model.calls.Load() != 1 {
		t.Fatalf("unknown retry sent again %d", model.calls.Load())
	}
	if _, err = p.Exec(context.Background(), "UPDATE mailboxes SET password_hash='changed' WHERE tenant_id=$1 AND id=$2", principal.TenantID, principal.MailboxID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Run(context.Background(), principal, "post-revoke-intent-0001", input, target); !errors.Is(err, ai.ErrUnauthorized) {
		t.Fatalf("revoked principal used: %v", err)
	}
}

func mailboxAIVersion(t *testing.T, p *db.Pool, tenant int64) string {
	t.Helper()
	cfg, err := store.New(p).PlatformGetAIProvider(t.Context(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.ConfigurationDigest
}
func approveMailboxAICloud(t *testing.T, p *db.Pool, tenant int64, allow bool) {
	t.Helper()
	ctx := t.Context()
	var owner int64
	if err := p.QueryRow(ctx, "SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner' LIMIT 1", tenant).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenApp(ctx, p.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	tx, err := app.UserTx(ctx, tenant, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cfg, err := store.New(p).PlatformGetAIProvider(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.New(tx).TenantConsentAICloud(ctx, store.TenantConsentAICloudParams{ConfigurationDigest: cfg.ConfigurationDigest, ScopeHash: AICloudScopeHash, AllowCloud: allow}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMailboxAICloudConsentIsTenantBoundAndTargetVersioned(t *testing.T) {
	p, principal := mailboxAIFixture(t, 20, false)
	ctx := t.Context()
	q := store.New(p)
	model := &countedAI{}
	service := MailboxAIService{Pool: p, Provider: func(store.AiProviderConfigs) (ai.Provider, error) { return model, nil }}
	input := ai.OperationInput{Operation: "summarize", Body: "Private own-mailbox content"}
	target := mailboxAIVersion(t, p, principal.TenantID)
	call := func(key, target string) error { _, err := service.Run(ctx, principal, key, input, target); return err }
	if err := call("cloud-unconsented-0001", target); !errors.Is(err, errAIConsent) || model.calls.Load() != 0 {
		t.Fatalf("platform setup authorized cloud export: %v", err)
	}
	if _, err := p.Exec(ctx, "UPDATE ai_provider_configs SET tenant_local_only=false,cloud_consent_digest=configuration_digest WHERE tenant_id=$1", principal.TenantID); err == nil {
		t.Fatal("platform connection overrode tenant local-only policy")
	}
	approveMailboxAICloud(t, p, principal.TenantID, true)
	if err := call("cloud-consented-000001", target); err != nil {
		t.Fatal(err)
	}
	if model.calls.Load() != 1 {
		t.Fatal("approved cloud call missing")
	}
	cfg, err := q.PlatformGetAIProvider(ctx, principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	update := func(name string) {
		t.Helper()
		_, err := q.PlatformSetAIProvider(ctx, store.PlatformSetAIProviderParams{TenantID: principal.TenantID, Mode: cfg.Mode, Provider: cfg.Provider, Endpoint: cfg.Endpoint, Model: name, Enabled: true, InputMicrosPerMillion: cfg.InputMicrosPerMillion, OutputMicrosPerMillion: cfg.OutputMicrosPerMillion, CostCurrency: cfg.CostCurrency})
		if err != nil {
			t.Fatal(err)
		}
	}
	update("changed-model")
	if err := call("cloud-target-stale-001", target); !errors.Is(err, errAITargetChanged) {
		t.Fatalf("stale target accepted: %v", err)
	}
	next := mailboxAIVersion(t, p, principal.TenantID)
	if err := call("cloud-new-unconsented", next); !errors.Is(err, errAIConsent) {
		t.Fatalf("configuration change reused old consent: %v", err)
	}
	update(cfg.Model)
	next = mailboxAIVersion(t, p, principal.TenantID)
	if next == target {
		t.Fatal("changing a target back resurrected its old version")
	}
	if err := call("cloud-reverted-target", next); !errors.Is(err, errAIConsent) {
		t.Fatal("old consent resurrected after configuration was reverted", err)
	}
	approveMailboxAICloud(t, p, principal.TenantID, true)
	approveMailboxAICloud(t, p, principal.TenantID, false)
	if err := call("cloud-after-revocation", next); !errors.Is(err, errAIConsent) {
		t.Fatal("revoked consent permitted a new external call", err)
	}
	var owner, other int64
	if err := p.QueryRow(ctx, "SELECT user_id FROM user_tenant_roles WHERE tenant_id=$1 AND role='owner'", principal.TenantID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) SELECT brand_id,'Foreign','foreign-ai','active' FROM tenants WHERE id=$1 RETURNING id", principal.TenantID).Scan(&other); err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenApp(ctx, p.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	tx, err := app.UserTx(ctx, other, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.New(tx).TenantConsentAICloud(ctx, store.TenantConsentAICloudParams{ConfigurationDigest: next, ScopeHash: AICloudScopeHash, AllowCloud: true}); err == nil {
		t.Fatal("foreign tenant context accepted a non-member's cloud consent")
	}
	_ = tx.Rollback(ctx)
	if _, err = q.PlatformSetAIProvider(ctx, store.PlatformSetAIProviderParams{TenantID: principal.TenantID, Mode: "local", Provider: "ollama", Endpoint: "http://127.0.0.1:11434", Model: "local-fixture", Enabled: true, LocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := call("local-after-cloud-revoke", mailboxAIVersion(t, p, principal.TenantID)); err != nil {
		t.Fatalf("independent local-only path failed: %v", err)
	}
	if model.calls.Load() != 2 {
		t.Fatalf("rejected calls reached the provider: %d", model.calls.Load())
	}
}
