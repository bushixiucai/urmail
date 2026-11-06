package marketing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type campaignFixture struct {
	platform *db.Pool
	app      *db.Pool
	tenant   int64
	owner    int64
	campaign int64
	box      *secrets.Box
}

func marketingFixture(t *testing.T, limit int, recipients int) campaignFixture {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	err = m.Up(ctx)
	_ = m.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	a, err := db.OpenApp(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	f := campaignFixture{platform: p, app: a}
	f.box, err = secrets.New([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	var brand, domain int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('marketing','Marketing') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('owner@example.test','unused') RETURNING id").Scan(&f.owner); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) VALUES($1,'Marketing','marketing','active',$2) RETURNING id", brand, f.owner).Scan(&f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", f.owner, f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'marketing_daily_recipient_limit',to_jsonb($2::bigint)),('tenant',$1,'marketing_rate_per_minute','600000')", f.tenant, limit); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'marketing.example.test','active','proof',now()) RETURNING id", f.tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled) VALUES($1,$2,'sender','sender@marketing.example.test','unused',268435456,'active',true)", f.tenant, domain); err != nil {
		t.Fatal(err)
	}
	cfg := SMTPConfig{Host: "gateway.example.test", Port: 587, From: "sender@marketing.example.test", Username: "gateway-user", Password: "gateway-secret", Dedicated: true, FeedbackKey: strings.Repeat("f", 32)}
	plain, _ := json.Marshal(cfg)
	encrypted, err := f.box.EncryptString(string(plain))
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(p.Pool)
	channel, err := q.PlatformCreateMarketingChannel(ctx, store.PlatformCreateMarketingChannelParams{TenantID: f.tenant, Name: "Dedicated", Kind: "dedicated_gateway", SenderAddress: cfg.From, EncryptedConfig: encrypted, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	template, err := q.CreateMarketingTemplate(ctx, store.CreateMarketingTemplateParams{TenantID: f.tenant, Name: "Test", Subject: "Campaign", TextBody: "Consented test message"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := q.CreateMarketingList(ctx, store.CreateMarketingListParams{TenantID: f.tenant, Name: "Consented"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range recipients {
		if _, err = q.UpsertMarketingContact(ctx, store.UpsertMarketingContactParams{TenantID: f.tenant, ListID: list.ID, Email: fmt.Sprintf("recipient-%d@example.test", i), ConsentSource: "explicit test consent", OptedIn: true}); err != nil {
			t.Fatal(err)
		}
	}
	campaign, err := q.CreateMarketingCampaign(ctx, store.CreateMarketingCampaignParams{TenantID: f.tenant, Name: "Campaign", ChannelID: channel.ID, TemplateID: template.ID, ListID: list.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.campaign = campaign.ID
	tx, err := a.UserTx(ctx, f.tenant, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.New(tx).ScheduleMarketingCampaign(ctx, store.ScheduleMarketingCampaignParams{TenantID: f.tenant, CampaignID: f.campaign, ScheduledAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}})
	if err != nil || result != "scheduled" {
		_ = tx.Rollback(ctx)
		t.Fatalf("schedule: %s %v", result, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f campaignFixture) worker(t *testing.T, sender Sender) *Worker {
	t.Helper()
	tokens, err := domainMail.NewSuppressionManager([]byte(strings.Repeat("u", 32)))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(Options{Pool: f.platform, Secrets: f.box, Tokens: tokens, Enabled: true, Hostname: "box.example.test", UnsubscribeURL: "https://panel.example.test/api/v1/marketing/unsubscribe", Sender: sender})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func TestDurableCampaignRestartAndConcurrentBudget(t *testing.T) {
	f := marketingFixture(t, 1, 2)
	var sends atomic.Int32
	sender := func(_ context.Context, c SMTPConfig, d Delivery) error {
		if c.Password != "gateway-secret" || !strings.Contains(d.UnsubscribeURL, "token=") {
			t.Error("encrypted channel or unsubscribe token lost")
		}
		sends.Add(1)
		return nil
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.worker(t, sender).Step(context.Background())
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if sends.Load() != 1 {
		t.Fatalf("daily concurrent sends=%d", sends.Load())
	}
	var used int64
	if err := f.platform.QueryRow(context.Background(), "SELECT sum(used_count) FROM marketing_quota_windows WHERE tenant_id=$1", f.tenant).Scan(&used); err != nil || used != 1 {
		t.Fatalf("reservation=%d err=%v", used, err)
	}
	// A new process sees the persisted result and cannot resubmit the sent row.
	if _, err := f.worker(t, sender).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 {
		t.Fatal("restart duplicated an accepted send")
	}
}

func TestDurableCampaignUnsubscribeBeforeFinalAuthorization(t *testing.T) {
	f := marketingFixture(t, 10, 1)
	if _, err := store.New(f.platform.Pool).AddMarketingSuppression(context.Background(), store.AddMarketingSuppressionParams{TenantID: f.tenant, Email: "recipient-0@example.test", Reason: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := f.worker(t, func(context.Context, SMTPConfig, Delivery) error { calls++; return nil }).Step(context.Background())
	if err != nil || calls != 0 {
		t.Fatalf("late unsubscribe not enforced: calls=%d err=%v", calls, err)
	}
	var state string
	if err = f.platform.QueryRow(context.Background(), "SELECT status FROM marketing_recipients WHERE tenant_id=$1 AND campaign_id=$2", f.tenant, f.campaign).Scan(&state); err != nil || state != "suppressed" {
		t.Fatalf("state=%s err=%v", state, err)
	}
}

func TestDurableCampaignUnknownOutcomeNeverBlindlyRetries(t *testing.T) {
	f := marketingFixture(t, 10, 1)
	calls := 0
	worker := f.worker(t, func(context.Context, SMTPConfig, Delivery) error {
		calls++
		return domainMail.MarkDeliveryUncertain(errors.New("transport disconnected"))
	})
	if _, err := worker.Step(context.Background()); !errors.Is(err, domainMail.ErrDeliveryUncertain) {
		t.Fatalf("uncertain result: %v", err)
	}
	if _, err := f.worker(t, func(context.Context, SMTPConfig, Delivery) error { calls++; return nil }).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unknown delivery was blindly re-sent")
	}
	var state string
	var reserved bool
	if err := f.platform.QueryRow(context.Background(), "SELECT status,reserved FROM marketing_recipients WHERE tenant_id=$1 AND campaign_id=$2", f.tenant, f.campaign).Scan(&state, &reserved); err != nil || state != "uncertain" || !reserved {
		t.Fatalf("state=%s reserved=%t err=%v", state, reserved, err)
	}
}

func TestExpiredSubmittingAttemptIsFencedAndManuallyResolved(t *testing.T) {
	f := marketingFixture(t, 10, 1)
	ctx := context.Background()
	q := store.New(f.platform.Pool)
	r, err := q.PlatformClaimMarketingRecipient(ctx, "old-process")
	if err != nil {
		t.Fatal(err)
	}
	result, err := q.PlatformAuthorizeMarketingSend(ctx, store.PlatformAuthorizeMarketingSendParams{TenantID: f.tenant, RecipientID: r.ID, LeaseOwner: "old-process", Fence: r.Fence})
	if err != nil || result != "authorized" {
		t.Fatalf("authorize=%s %v", result, err)
	}
	if _, err = f.platform.Exec(ctx, "UPDATE marketing_recipients SET lease_until=now()-interval '1 second' WHERE id=$1", r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformRecoverMarketingSubmissions(ctx); err != nil {
		t.Fatal(err)
	}
	finished, err := q.PlatformFinishMarketingSend(ctx, store.PlatformFinishMarketingSendParams{TenantID: f.tenant, RecipientID: r.ID, LeaseOwner: "old-process", Fence: r.Fence, Result: "sent"})
	if err != nil || finished {
		t.Fatalf("stale worker wrote success: %t %v", finished, err)
	}
	resolved, err := q.PlatformResolveMarketingAttempt(ctx, store.PlatformResolveMarketingAttemptParams{TenantID: f.tenant, RecipientID: r.ID, Outcome: "not_submitted", Reason: "Operator confirmed no SMTP connection was opened"})
	if err != nil || !resolved {
		t.Fatalf("resolve=%t %v", resolved, err)
	}
	var used int64
	if err = f.platform.QueryRow(ctx, "SELECT used_count FROM marketing_quota_windows WHERE tenant_id=$1", f.tenant).Scan(&used); err != nil || used != 0 {
		t.Fatalf("old-window reservation not released: %d %v", used, err)
	}
}

func TestDisabledDevelopmentAndQuarantinedWorkersNeverClaim(t *testing.T) {
	for _, opts := range []Options{{}, {Enabled: true, DevMode: true}, {Enabled: true, Quarantined: true}} {
		worker, err := NewWorker(opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = worker.Step(context.Background()); !errors.Is(err, ErrDisabled) {
			t.Fatalf("egress gate bypassed: %v", err)
		}
	}
}

func TestApplicationCannotReadChannelSecretsOrForgeDeliveryState(t *testing.T) {
	f := marketingFixture(t, 5, 1)
	ctx := context.Background()
	for _, sql := range []string{"SELECT encrypted_config FROM marketing_channels WHERE tenant_id=$1", "UPDATE marketing_recipients SET status='sent' WHERE tenant_id=$1", "UPDATE marketing_quota_windows SET used_count=0 WHERE tenant_id=$1", "DELETE FROM marketing_contacts WHERE tenant_id=$1"} {
		tx, err := f.app.UserTx(ctx, f.tenant, f.owner)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, sql, f.tenant)
		_ = tx.Rollback(ctx)
		if err == nil {
			t.Fatalf("private privilege exposed: %s", sql)
		}
	}
}

func TestDurableRevocationBeforeDataReleasesOriginalReservation(t *testing.T) {
	f := marketingFixture(t, 5, 1)
	ctx := context.Background()
	checks := 0
	worker := f.worker(t, func(c context.Context, _ SMTPConfig, d Delivery) error {
		_, err := store.New(f.platform.Pool).AddMarketingSuppression(c, store.AddMarketingSuppressionParams{TenantID: f.tenant, Email: d.Email, Reason: "unsubscribe"})
		if err != nil {
			return err
		}
		checks++
		return d.BeforeData(c)
	})
	if _, err := worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if checks != 1 {
		t.Fatal("fake pre-DATA window not reached")
	}
	var used int64
	if err := f.platform.QueryRow(ctx, "SELECT sum(used_count) FROM marketing_quota_windows WHERE tenant_id=$1", f.tenant).Scan(&used); err != nil || used != 0 {
		t.Fatalf("definite pre-DATA failure charged quota=%d %v", used, err)
	}
}

func TestPauseAndCancelDoNotAuthorizeNewAttempts(t *testing.T) {
	for _, state := range []string{"paused", "canceled"} {
		t.Run(state, func(t *testing.T) {
			f := marketingFixture(t, 5, 1)
			ctx := context.Background()
			tx, err := f.app.UserTx(ctx, f.tenant, f.owner)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.New(tx).ChangeMarketingCampaignState(ctx, store.ChangeMarketingCampaignStateParams{TenantID: f.tenant, ID: f.campaign, Status: state})
			if err != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			sends := 0
			_, err = f.worker(t, func(context.Context, SMTPConfig, Delivery) error { sends++; return nil }).Step(ctx)
			if err != nil || sends != 0 {
				t.Fatalf("%s sends=%d err=%v", state, sends, err)
			}
			if state == "canceled" {
				var recipientState string
				if err = f.platform.QueryRow(ctx, "SELECT status FROM marketing_recipients WHERE tenant_id=$1", f.tenant).Scan(&recipientState); err != nil || recipientState != "canceled" {
					t.Fatalf("cancel cleanup %s %v", recipientState, err)
				}
			}
		})
	}
}

// MKT-001: only authorized tenant roles may create or confirm a campaign,
// the confirmation freezes an immutable content snapshot, a repeated
// confirmation must not duplicate recipients, and a re-imported contact that
// is suppressed stays deactivated.
func TestCampaignConfirmAuthorizationSnapshotAndSuppression(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	err = m.Up(ctx)
	_ = m.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	a, err := db.OpenApp(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	var brand, tenant, owner, member int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('mkt-core','MKT') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('mkt-owner@example.test','unused') RETURNING id").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('mkt-member@example.test','unused') RETURNING id").Scan(&member); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) VALUES($1,'MKT','mkt-core','active',$2) RETURNING id", brand, owner).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'member')", owner, tenant, member, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',$1,'marketing_daily_recipient_limit','100'),('tenant',$1,'marketing_rate_per_minute','600')", tenant); err != nil {
		t.Fatal(err)
	}
	var domain int64
	if err = p.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'mkt-core.example.test','active','proof',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled) VALUES($1,$2,'sender','sender@mkt-core.example.test','unused',268435456,'active',true)", tenant, domain); err != nil {
		t.Fatal(err)
	}
	q := store.New(p)
	channel, err := q.PlatformCreateMarketingChannel(ctx, store.PlatformCreateMarketingChannelParams{TenantID: tenant, Name: "Dedicated", Kind: "dedicated_gateway", SenderAddress: "sender@mkt-core.example.test", EncryptedConfig: "enc:v1:fixture", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	template, err := q.CreateMarketingTemplate(ctx, store.CreateMarketingTemplateParams{TenantID: tenant, Name: "Core", Subject: "Frozen Subject", TextBody: "Original body"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := q.CreateMarketingList(ctx, store.CreateMarketingListParams{TenantID: tenant, Name: "Core"})
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"active@example.test", "returning@example.test"} {
		if _, err = q.UpsertMarketingContact(ctx, store.UpsertMarketingContactParams{TenantID: tenant, ListID: list.ID, Email: email, ConsentSource: "explicit test consent", OptedIn: true}); err != nil {
			t.Fatal(err)
		}
	}
	// The tenant unsubscribed; a later re-import must not reactivate them.
	if _, err = q.AddMarketingSuppression(ctx, store.AddMarketingSuppressionParams{TenantID: tenant, Email: "returning@example.test", Reason: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpsertMarketingContact(ctx, store.UpsertMarketingContactParams{TenantID: tenant, ListID: list.ID, Email: "returning@example.test", ConsentSource: "re-imported list", OptedIn: true}); err != nil {
		t.Fatal(err)
	}
	campaign, err := q.CreateMarketingCampaign(ctx, store.CreateMarketingCampaignParams{TenantID: tenant, Name: "Core", ChannelID: channel.ID, TemplateID: template.ID, ListID: list.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A member may neither create nor confirm a campaign. Each rejected
	// statement aborts its transaction, so every probe uses a fresh one.
	memberProbe := func(probe func(*store.Queries) error, label string) {
		t.Helper()
		tx, err := a.UserTx(ctx, tenant, member)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err = probe(store.New(tx)); err == nil || !strings.Contains(err.Error(), "MARKETING_FORBIDDEN") {
			t.Fatalf("member %s: err=%v", label, err)
		}
	}
	memberProbe(func(sq *store.Queries) error {
		_, err := sq.CreateMarketingCampaign(ctx, store.CreateMarketingCampaignParams{TenantID: tenant, Name: "Member", ChannelID: channel.ID, TemplateID: template.ID, ListID: list.ID})
		return err
	}, "created a campaign")
	memberProbe(func(sq *store.Queries) error {
		_, err := sq.ScheduleMarketingCampaign(ctx, store.ScheduleMarketingCampaignParams{TenantID: tenant, CampaignID: campaign.ID, ScheduledAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}})
		return err
	}, "confirmed a campaign")
	// The owner confirms; the suppressed contact stays out of the recipients.
	ownerTx, err := a.UserTx(ctx, tenant, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ownerTx.Rollback(ctx) }()
	result, err := store.New(ownerTx).ScheduleMarketingCampaign(ctx, store.ScheduleMarketingCampaignParams{TenantID: tenant, CampaignID: campaign.ID, ScheduledAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}})
	if err != nil || result != "scheduled" {
		_ = ownerTx.Rollback(ctx)
		t.Fatalf("owner schedule: %s %v", result, err)
	}
	if err = ownerTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	recipients, err := q.ListMarketingRecipients(ctx, store.ListMarketingRecipientsParams{TenantID: tenant, CampaignID: campaign.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0].Email != "active@example.test" {
		t.Fatalf("recipients = %+v, want only the unsuppressed contact", recipients)
	}
	// Editing the template after confirmation must not touch the frozen copy,
	// and a repeated confirmation must not duplicate recipients.
	if _, err = p.Exec(ctx, "UPDATE marketing_templates SET subject='Changed',text_body='Changed body' WHERE id=$1", template.ID); err != nil {
		t.Fatal(err)
	}
	ownerTx, err = a.UserTx(ctx, tenant, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ownerTx.Rollback(ctx) }()
	if result, err = store.New(ownerTx).ScheduleMarketingCampaign(ctx, store.ScheduleMarketingCampaignParams{TenantID: tenant, CampaignID: campaign.ID, ScheduledAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}}); err != nil || result != "scheduled" {
		_ = ownerTx.Rollback(ctx)
		t.Fatalf("duplicate schedule: %s %v", result, err)
	}
	if err = ownerTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	frozen, err := q.GetMarketingCampaign(ctx, store.GetMarketingCampaignParams{TenantID: tenant, ID: campaign.ID})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.SubjectSnapshot.String != "Frozen Subject" || frozen.BodySnapshot.String != "Original body" {
		t.Fatalf("snapshot was rewritten: %q %q", frozen.SubjectSnapshot.String, frozen.BodySnapshot.String)
	}
	if recipients, err = q.ListMarketingRecipients(ctx, store.ListMarketingRecipientsParams{TenantID: tenant, CampaignID: campaign.ID}); err != nil || len(recipients) != 1 {
		t.Fatalf("duplicate confirmation changed recipients: %d %v", len(recipients), err)
	}
}
