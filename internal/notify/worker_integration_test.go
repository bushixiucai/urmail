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

	pgtype "github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus"

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
	if _, err = p.Exec(ctx, "INSERT INTO notification_outbox(tenant_id,kind,message_key,dedupe_key,channel,state,recipient,recipient_verified) VALUES($1,'dns_requires_review','notification.dns_requires_review','dns-fixture','email','pending','verified@example.test',true)", tenant); err != nil {
		t.Fatal(err)
	}
	if err = Evaluate(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = Evaluate(ctx, p); err != nil {
		t.Fatal(err)
	}
	var notices int
	if err = p.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE tenant_id=$1", tenant).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("duplicate/missing reminders: %d %v", notices, err)
	}
	calls := 0
	worker := Worker{Pool: p, Secrets: box, Messages: messages, BaseURL: "https://panel.example.test", Enabled: true, Send: func(_ context.Context, _ SMTPConfig, m Message) error {
		calls++
		if !strings.Contains(m.Body, "DNS") {
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

// enqueueLifecycleRow appends one due email notification with a stable dedupe
// key and returns nothing; duplicates collapse on dedupe_key by contract.
func enqueueLifecycleRow(ctx context.Context, t *testing.T, q *store.Queries, tenant int64, dedupe string) {
	t.Helper()
	if err := q.PlatformEnqueueNotification(ctx, store.PlatformEnqueueNotificationParams{TenantID: pgtype.Int8{Int64: tenant, Valid: true}, Kind: "subscription_expiry", MessageKey: "notification.subscription_expiry", Params: []byte("{}"), DedupeKey: dedupe, Channel: "email", State: "pending", Recipient: pgtype.Text{String: "verified@example.test", Valid: true}, RecipientVerified: true}); err != nil {
		t.Fatal(err)
	}
}

func setNotificationChannel(ctx context.Context, t *testing.T, q *store.Queries, box *secrets.Box) {
	t.Helper()
	raw, err := json.Marshal(SMTPConfig{Enabled: true, Host: "relay.example.test", Port: 465, From: "notice@example.test", ImplicitTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := box.EncryptString(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(encrypted)
	if err = q.PlatformUpsertSetting(ctx, store.PlatformUpsertSettingParams{Key: "notification_channel", Value: value}); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationDuplicateEnqueueDeliversOnce(t *testing.T) {
	p := notificationDatabase(t)
	ctx := context.Background()
	q := store.New(p)
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	setNotificationChannel(ctx, t, q, box)
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	var brand, tenant int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('dup','Dup') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status,owner_user_id) VALUES($1,'Dup','dup','active',NULL) RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	enqueueLifecycleRow(ctx, t, q, tenant, "ops103-duplicate")
	enqueueLifecycleRow(ctx, t, q, tenant, "ops103-duplicate")
	var rows int
	if err = p.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE dedupe_key='ops103-duplicate'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("dedupe rows=%d err=%v", rows, err)
	}
	calls := 0
	worker := Worker{Pool: p, Secrets: box, Messages: messages, BaseURL: "https://panel.example.test", Enabled: true, Send: func(context.Context, SMTPConfig, Message) error {
		calls++
		return nil
	}}
	if worked, err := worker.Step(ctx); !worked || err != nil {
		t.Fatalf("first step %v %v", worked, err)
	}
	if worked, err := worker.Step(ctx); worked || err != nil {
		t.Fatalf("second step replayed %v %v", worked, err)
	}
	if calls != 1 {
		t.Fatalf("deliveries=%d want 1", calls)
	}
	var state string
	if err = p.QueryRow(ctx, "SELECT state FROM notification_outbox WHERE dedupe_key='ops103-duplicate'").Scan(&state); err != nil || state != "delivered" {
		t.Fatalf("state=%s err=%v", state, err)
	}
}

func TestNotificationLeaseRecoveryAfterCrashedWorker(t *testing.T) {
	p := notificationDatabase(t)
	ctx := context.Background()
	q := store.New(p)
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	setNotificationChannel(ctx, t, q, box)
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	var brand, tenant int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('lease','Lease') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Lease','lease','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	enqueueLifecycleRow(ctx, t, q, tenant, "ops103-lease")
	// Simulate a worker that claimed a lease and crashed before delivering.
	if _, err = q.PlatformClaimEmailNotification(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempts int
	if err = p.QueryRow(ctx, "SELECT state,attempts FROM notification_outbox WHERE dedupe_key='ops103-lease'").Scan(&state, &attempts); err != nil || state != "pending" || attempts != 1 {
		t.Fatalf("leased row state=%s attempts=%d err=%v", state, attempts, err)
	}
	calls := 0
	worker := Worker{Pool: p, Secrets: box, Messages: messages, BaseURL: "https://panel.example.test", Enabled: true, Send: func(context.Context, SMTPConfig, Message) error {
		calls++
		return nil
	}}
	// While the lease is live no other worker may claim the row.
	if worked, err := worker.Step(ctx); worked || err != nil {
		t.Fatalf("claim under live lease %v %v", worked, err)
	}
	if calls != 0 {
		t.Fatalf("premature deliveries=%d", calls)
	}
	// The crashed worker's lease expires; recovery re-delivers at-least-once.
	if _, err = p.Exec(ctx, "UPDATE notification_outbox SET lease_until=now()-interval '1 second' WHERE dedupe_key='ops103-lease'"); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.Step(ctx); !worked || err != nil {
		t.Fatalf("recovery step %v %v", worked, err)
	}
	if calls != 1 {
		t.Fatalf("recovered deliveries=%d want 1", calls)
	}
	var leaseUntil pgtype.Timestamptz
	if err = p.QueryRow(ctx, "SELECT state,lease_until FROM notification_outbox WHERE dedupe_key='ops103-lease'").Scan(&state, &leaseUntil); err != nil || state != "delivered" || leaseUntil.Valid {
		t.Fatalf("final state=%s lease=%v err=%v", state, leaseUntil, err)
	}
}

func TestNotificationBackoffScheduleAndPoisonCap(t *testing.T) {
	p := notificationDatabase(t)
	ctx := context.Background()
	q := store.New(p)
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	setNotificationChannel(ctx, t, q, box)
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	var brand, tenant int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('backoff','Backoff') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Backoff','backoff','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	enqueueLifecycleRow(ctx, t, q, tenant, "ops103-backoff")
	failures := 0
	worker := Worker{Pool: p, Secrets: box, Messages: messages, BaseURL: "https://panel.example.test", Enabled: true, Send: func(context.Context, SMTPConfig, Message) error {
		failures++
		return errors.New("relay unavailable")
	}}
	wantBackoffSeconds := []float64{60, 120, 240}
	for i, want := range wantBackoffSeconds {
		if worked, err := worker.Step(ctx); !worked || err == nil {
			t.Fatalf("step %d %v %v", i, worked, err)
		}
		var attempts int
		var next time.Time
		var lastErr pgtype.Text
		if err = p.QueryRow(ctx, "SELECT attempts,next_attempt_at,last_error FROM notification_outbox WHERE dedupe_key='ops103-backoff'").Scan(&attempts, &next, &lastErr); err != nil {
			t.Fatal(err)
		}
		if attempts != i+1 {
			t.Fatalf("step %d attempts=%d", i, attempts)
		}
		delay := time.Until(next).Seconds()
		if delay < want-10 || delay > want+10 {
			t.Fatalf("step %d backoff=%.0fs want %.0fs", i, delay, want)
		}
		if !lastErr.Valid || lastErr.String != "relay unavailable" {
			t.Fatalf("step %d last_error=%v", i, lastErr)
		}
		// Simulate the backoff window elapsing before the next retry.
		if _, err = p.Exec(ctx, "UPDATE notification_outbox SET next_attempt_at=now()-interval '1 second' WHERE dedupe_key='ops103-backoff'"); err != nil {
			t.Fatal(err)
		}
	}
	// Exhaust the attempt budget: the poison sweep finalizes the row as failed
	// and surfaced, never retried again.
	if _, err = p.Exec(ctx, "UPDATE notification_outbox SET attempts=8 WHERE dedupe_key='ops103-backoff'"); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.Step(ctx); worked || err != nil {
		t.Fatalf("poisoned row stepped %v %v", worked, err)
	}
	var state string
	var lastErr pgtype.Text
	if err = p.QueryRow(ctx, "SELECT state,last_error FROM notification_outbox WHERE dedupe_key='ops103-backoff'").Scan(&state, &lastErr); err != nil || state != "failed" || !lastErr.Valid {
		t.Fatalf("poisoned state=%s last_error=%v err=%v", state, lastErr, err)
	}
	if failures != len(wantBackoffSeconds) {
		t.Fatalf("unexpected extra deliveries=%d", failures)
	}
}

func TestNotificationTelemetryCountersTrackEnqueueAndDelivery(t *testing.T) {
	p := notificationDatabase(t)
	ctx := context.Background()
	q := store.New(p)
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	setNotificationChannel(ctx, t, q, box)
	messages, err := i18n.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	var brand, tenant int64
	if err = p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('counter','Counter') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Counter','counter','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	sentBefore := counterValue(t, "urmail_notifications_delivery_total", "email", "subscription_expiry", "sent")
	// The outbox watermark is process-global while every test builds a fresh
	// database whose ids restart below it; reset it so this database's new row
	// is what the counter observes.
	lastEnqueuedID = 0
	enqueueLifecycleRow(ctx, t, q, tenant, "ops103-counter")
	// The watermark-based enqueue counter must count the new row exactly once.
	if err = observeNotifications(ctx, p); err != nil {
		t.Fatal(err)
	}
	enqueued := counterValue(t, "urmail_notifications_enqueued_total", "email", "subscription_expiry")
	if enqueued < 1 {
		t.Fatalf("enqueued counter=%v", enqueued)
	}
	depth := counterValue(t, "urmail_notification_outbox_items", "email", "subscription_expiry", "pending")
	if depth != 1 {
		t.Fatalf("outbox depth gauge=%v", depth)
	}
	worker := Worker{Pool: p, Secrets: box, Messages: messages, BaseURL: "https://panel.example.test", Enabled: true, Send: func(context.Context, SMTPConfig, Message) error { return nil }}
	if worked, err := worker.Step(ctx); !worked || err != nil {
		t.Fatalf("step %v %v", worked, err)
	}
	if got := counterValue(t, "urmail_notifications_delivery_total", "email", "subscription_expiry", "sent"); got != sentBefore+1 {
		t.Fatalf("sent counter delta=%v", got-sentBefore)
	}
	if err = observeNotifications(ctx, p); err != nil {
		t.Fatal(err)
	}
	if depth := counterValue(t, "urmail_notification_outbox_items", "email", "subscription_expiry", "pending"); depth != 0 {
		t.Fatalf("post-delivery depth=%v", depth)
	}
}

// counterValue reads one sample from the default registry without pulling a
// test-only assertion dependency into the module graph.
func counterValue(t *testing.T, name string, labels ...string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			current := metric
			labelMatch := len(current.GetLabel()) == len(labels)
			if labelMatch {
				for i, label := range current.GetLabel() {
					if label.GetValue() != labels[i] {
						labelMatch = false
						break
					}
				}
			}
			if !labelMatch {
				continue
			}
			switch {
			case current.GetCounter() != nil:
				return current.GetCounter().GetValue()
			case current.GetGauge() != nil:
				return current.GetGauge().GetValue()
			}
		}
	}
	return 0
}
