package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func notificationDatabase(t *testing.T) *db.Pool {
	t.Helper()
	dsn := testdb.New(t)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	params.Set("role", "urmail_migrate")
	parsed.RawQuery = params.Encode()
	m, err := db.NewMigrator(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Up(context.Background()); err != nil {
		_ = m.Close()
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestDurableNotificationDedupAndUnknownDelivery(t *testing.T) {
	p := notificationDatabase(t)
	ctx := context.Background()
	q := store.New(p)
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := SMTPConfig{Enabled: true, Host: "relay.example.test", Port: 465, From: "notice@example.test", ImplicitTLS: true}
	raw, _ := json.Marshal(cfg)
	encrypted, err := box.EncryptString(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(encrypted)
	if err = q.PlatformUpsertSetting(ctx, store.PlatformUpsertSettingParams{Key: "notification_channel", Value: value}); err != nil {
		t.Fatal(err)
	}
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	var brand, tenant int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('notice','Notice') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Notice','notice','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "INSERT INTO tenant_contacts(tenant_id,email,verified_at) VALUES($1,'verified@example.test',now())", tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, "UPDATE subscriptions SET period_end=now()+interval '3 days' WHERE tenant_id=$1 AND status='active'", tenant); err != nil {
		t.Fatal(err)
	}
	if err = Evaluate(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = Evaluate(ctx, p); err != nil {
		t.Fatal(err)
	}
	var notices int
	if err = p.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE tenant_id=$1", tenant).Scan(&notices); err != nil || notices != 2 {
		t.Fatalf("duplicate/missing reminders: %d %v", notices, err)
	}
	calls := 0
	worker := Worker{Pool: p, Secrets: box, Messages: messages, BaseURL: "https://panel.example.test", Enabled: true, Send: func(_ context.Context, _ SMTPConfig, m Message) error {
		calls++
		if !strings.Contains(m.Body, "free") {
			t.Fatal("missing template parameters")
		}
		return ErrSubmissionUnknown
	}}
	if worked, err := worker.Step(ctx); !worked || !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("uncertain delivery %v %v", worked, err)
	}
	if worked, err := worker.Step(ctx); worked || err != nil {
		t.Fatalf("blind replay %v %v", worked, err)
	}
	if calls != 1 {
		t.Fatalf("delivery repeated %d", calls)
	}
	worker.DevMode = true
	if worked, _ := worker.Step(ctx); worked {
		t.Fatal("development mode sent mail")
	}
	worker.DevMode = false
	worker.Quarantined = true
	if worked, _ := worker.Step(ctx); worked {
		t.Fatal("quarantined worker sent mail")
	}
	// A restore or expired lease cannot make an uncertain physical submission retryable.
	if _, err = p.Exec(ctx, "UPDATE notification_outbox SET lease_until=$1 WHERE channel='email'", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	worker.Quarantined = false
	if worked, _ := worker.Step(ctx); worked || calls != 1 {
		t.Fatal("expired uncertain lease replayed")
	}
}

func TestAIUnknownUsageAlertsPersistAndResolveWithoutDuplicateNotifications(t *testing.T) {
	p := notificationDatabase(t)
	ctx := context.Background()
	var brand, tenant, domain, mailbox int64
	for _, step := range []struct {
		sql    string
		args   []any
		target *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('ai-notice','AI Notice') RETURNING id", nil, &brand},
		{"INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'AI Notice','ai-notice','active') RETURNING id", []any{&brand}, &tenant},
		{"INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'ai-notice.test','active',now(),'fixture') RETURNING id", []any{&tenant}, &domain},
		{"INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'owner','owner@ai-notice.test','unused',1024,'active') RETURNING id", []any{&tenant, &domain}, &mailbox},
	} {
		args := make([]any, 0, len(step.args))
		for _, arg := range step.args {
			args = append(args, *(arg.(*int64)))
		}
		if err := p.QueryRow(ctx, step.sql, args...).Scan(step.target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Exec(ctx, "INSERT INTO ai_usage_requests(tenant_id,mailbox_id,request_key,input_digest,period_start,period_end,mode,provider,model,cost_currency,input_rate,output_rate,status,reserved_tokens,reserved_cost_micros) VALUES($1,$2,'notification-unknown-request',repeat('a',64),now()-interval '1 day',now()+interval '1 day','platform','fixture','fixture','CNY',1,1,'uncertain',100,100)", tenant, mailbox); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Evaluate(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	var firing, notices int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM operational_alerts WHERE tenant_id=$1 AND kind='ai_requires_review' AND state='firing'", tenant).Scan(&firing); err != nil || firing != 1 {
		t.Fatalf("firing=%d err=%v", firing, err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE tenant_id=$1 AND kind='ai_requires_review'", tenant).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("notices=%d err=%v", notices, err)
	}
	if _, err := p.Exec(ctx, "UPDATE ai_usage_requests SET status='resolved',actual_tokens=100,actual_cost_micros=100 WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM operational_alerts WHERE tenant_id=$1 AND kind='ai_requires_review' AND state='resolved'", tenant).Scan(&firing); err != nil || firing != 1 {
		t.Fatalf("resolved=%d err=%v", firing, err)
	}
}

func TestNotificationWorkerUsesSMTPFactsWithoutBlindReplay(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(ambiguous), func(t *testing.T) {
			p := notificationDatabase(t)
			ctx := context.Background()
			q := store.New(p)
			cfg, roots, submitted := localSMTPRelay(t, ambiguous)
			box, err := secrets.New([]byte("01234567890123456789012345678901"))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			encrypted, err := box.EncryptString(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			setting, err := json.Marshal(encrypted)
			if err != nil {
				t.Fatal(err)
			}
			if err = q.PlatformUpsertSetting(ctx, store.PlatformUpsertSettingParams{Key: "notification_channel", Value: setting}); err != nil {
				t.Fatal(err)
			}
			if _, err = p.Exec(ctx, "INSERT INTO notification_outbox(kind,message_key,params,dedupe_key,channel,state,recipient,recipient_verified) VALUES('payment_manual_review','notification.payment_manual_review','{}','smtp-fixture','email','pending','verified@example.test',true)"); err != nil {
				t.Fatal(err)
			}
			messages, err := i18n.NewStore()
			if err != nil {
				t.Fatal(err)
			}
			worker := Worker{Pool: p, Secrets: box, Messages: messages, BaseURL: "https://panel.example.test", Enabled: true, Send: func(ctx context.Context, cfg SMTPConfig, message Message) error {
				return sendSMTP(ctx, cfg, message, roots)
			}}
			worked, err := worker.Step(ctx)
			if !worked || (!ambiguous && err != nil) || (ambiguous && !errors.Is(err, ErrSubmissionUnknown)) {
				t.Fatalf("SMTP worker result %v %v", worked, err)
			}
			var state string
			if err = p.QueryRow(ctx, "SELECT state FROM notification_outbox WHERE dedupe_key='smtp-fixture'").Scan(&state); err != nil {
				t.Fatal(err)
			}
			want := "delivered"
			if ambiguous {
				want = "uncertain"
			}
			if state != want {
				t.Fatalf("state=%s want=%s", state, want)
			}
			if worked, err = worker.Step(ctx); worked || err != nil {
				t.Fatalf("blind replay %v %v", worked, err)
			}
			if submitted.Load() != 1 {
				t.Fatalf("physical submissions=%d", submitted.Load())
			}
		})
	}
}
