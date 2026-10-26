package payment

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func rejectionMerchant(t *testing.T, pool *db.Pool, account string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `INSERT INTO payment_merchants(provider,environment,account_id,name,encrypted_config) VALUES('stripe','sandbox',$1,'Rejection fixture','enc:v1:fixture') RETURNING id`, account).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRejectedWebhookGlobalBudgetAndNoFinancialEvents(t *testing.T) {
	p, a := testdb.OpenMigrated(t)
	ctx := context.Background()
	id := rejectionMerchant(t, p, "rejected_budget")
	q := store.New(p)
	// A single SQL statement pins statement_timestamp across the budget boundary.
	// Distinct body fingerprints still consume at most 120 global writes.
	var admitted int64
	if err := p.QueryRow(ctx, `SELECT count(*) FILTER(WHERE recorded) FROM (SELECT urmail_record_rejected_webhook($1,'verification_rejected',md5(n::text)||md5(n::text),12000) AS recorded FROM generate_series(1,121) n) attempts`, id).Scan(&admitted); err != nil || admitted != 120 {
		t.Fatalf("global write budget admitted %d: %v", admitted, err)
	}
	var rows, occurrences, writes, inbox int64
	if err := p.QueryRow(ctx, `SELECT count(*),COALESCE(sum(occurrences),0) FROM payment_webhook_rejections`).Scan(&rows, &occurrences); err != nil || rows != 1 || occurrences != 120 {
		t.Fatalf("unbounded rejection aggregates: rows=%d count=%d err=%v", rows, occurrences, err)
	}
	if err := p.QueryRow(ctx, `SELECT writes FROM payment_webhook_rejection_budget`).Scan(&writes); err != nil || writes != 120 {
		t.Fatalf("unexpected write budget: %d %v", writes, err)
	}
	if err := p.QueryRow(ctx, `SELECT count(*) FROM payment_webhook_inbox`).Scan(&inbox); err != nil || inbox != 0 {
		t.Fatalf("unverified body entered financial inbox: %d %v", inbox, err)
	}
	if _, err := a.Exec(ctx, `SELECT urmail_record_rejected_webhook($1,'verification_rejected',$2,1)`, id, strings.Repeat("a", 64)); err == nil {
		t.Fatal("application role acquired cross-merchant diagnostic write access")
	}
	// Time-bucket rollover is bounded by the same singleton, not process memory.
	if _, err := p.Exec(ctx, `UPDATE payment_webhook_rejection_budget SET minute_start=now()-interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	RecordRejectedWebhook(ctx, q, id, "merchant_rejected", []byte("different category"))
	if err := p.QueryRow(ctx, `SELECT writes FROM payment_webhook_rejection_budget`).Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("budget rollover failed: %d %v", writes, err)
	}
}

func TestRejectedWebhookBucketCapacityIsGlobal(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	first := rejectionMerchant(t, p, "capacity_a")
	second := rejectionMerchant(t, p, "capacity_b")
	if _, err := p.Exec(ctx, `INSERT INTO payment_webhook_rejections SELECT $1,date_trunc('hour',now())-n*interval '1 hour','verification_rejected',1,repeat('a',64),1,now() FROM generate_series(0,9999) n`, first); err != nil {
		t.Fatal(err)
	}
	q := store.New(p)
	RecordRejectedWebhook(ctx, q, second, "verification_rejected", []byte("new merchant"))
	var rows int64
	if err := p.QueryRow(ctx, `SELECT count(*) FROM payment_webhook_rejections`).Scan(&rows); err != nil || rows != 10000 {
		t.Fatalf("global bucket cap exceeded: %d %v", rows, err)
	}
	RecordRejectedWebhook(ctx, q, first, "verification_rejected", []byte("existing bucket"))
	var count int64
	if err := p.QueryRow(ctx, `SELECT occurrences FROM payment_webhook_rejections WHERE merchant_id=$1 AND bucket_start=date_trunc('hour',now())`, first).Scan(&count); err != nil || count != 2 {
		t.Fatalf("existing bounded bucket rejected at capacity: %d %v", count, err)
	}
}

func TestWebhookMaintenanceDryRunAndLegacyBodyRedaction(t *testing.T) {
	p, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	id := rejectionMerchant(t, p, "legacy_rejected")
	for i, event := range []struct{ state, code, eventID string }{
		{"rejected", "verification_rejected", "rejected:" + strings.Repeat("a", 64)},
		{"completed", "settled", "verified-completed"},
		{"uncertain", "requires_review", "verified-uncertain"},
		{"rejected", "verification_rejected", "rejected:" + strings.Repeat("b", 64)},
		{"rejected", "amount_mismatch", "verified-rejected"},
	} {
		// The attacker-controlled redacted property cannot bypass the trusted marker.
		if _, err := p.Exec(ctx, `INSERT INTO payment_webhook_inbox(merchant_id,event_id,payload_hash,payload,state,result_code,created_at) VALUES($1,$2,repeat('c',64),$3,$4,$5,now()-interval '8 days')`, id, event.eventID, fmt.Sprintf(`{"redacted":true,"sensitive":"legacy-body-%d"}`, i), event.state, event.code); err != nil {
			t.Fatal(err)
		}
	}
	dry, err := MaintainWebhookDiagnostics(ctx, p, false)
	if err != nil || dry.Before.LegacyBodies != 2 || dry.CompactedBodies != 0 || dry.PurgedLegacy != 0 {
		t.Fatalf("dry run changed data or lost legacy count: %+v %v", dry, err)
	}
	var before int64
	if err = p.QueryRow(ctx, `SELECT count(*) FROM payment_webhook_inbox`).Scan(&before); err != nil || before != 5 {
		t.Fatal("dry run mutated the inbox")
	}
	result, err := MaintainWebhookDiagnostics(ctx, p, true)
	if err != nil || result.CompactedBodies != 2 || result.PurgedLegacy != 2 {
		t.Fatalf("legacy maintenance: %+v %v", result, err)
	}
	var remaining int64
	if err = p.QueryRow(ctx, `SELECT count(*) FROM payment_webhook_inbox WHERE event_id LIKE 'verified-%'`).Scan(&remaining); err != nil || remaining != 3 {
		t.Fatalf("verified financial evidence was removed: %d %v", remaining, err)
	}
	result, err = MaintainWebhookDiagnostics(ctx, p, true)
	if err != nil || result.CompactedBodies != 0 || result.PurgedLegacy != 0 {
		t.Fatalf("maintenance was not idempotent: %+v %v", result, err)
	}
}
