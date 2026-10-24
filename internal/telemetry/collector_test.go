package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func collectorDatabase(t *testing.T) (*db.Pool, int64) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.New(t)
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer migrator.Close()
	if err = migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var brand, tenant, domain int64
	if err = pool.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('collector','Collector') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Collector','collector','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,'example.test','active',now(),'verified') RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,'alice','alice@example.test','hash',1024,'active')", tenant, domain); err != nil {
		t.Fatal(err)
	}
	return pool, tenant
}

func sourceBatch(data string) agentd.MailLogBatch {
	return agentd.MailLogBatch{Identity: "source-one", Data: data, StartOffset: 0, EndOffset: int64(len(data)), Reset: true, ObservedAt: time.Date(2026, 10, 24, 10, 10, 0, 0, time.UTC)}
}

func TestCollectorCorrelatesAuthenticatedLifecycleAndNeverGuessesFrom(t *testing.T) {
	pool, tenant := collectorDatabase(t)
	ctx := context.Background()
	collector := &Collector{Pool: pool, Stream: "node"}
	data := strings.Join([]string{
		"2026-10-24T10:00:00Z host postfix/smtpd[1]: ABC123: client=client.test[127.0.0.1], sasl_method=PLAIN, sasl_username=alice@example.test",
		"2026-10-24T10:00:00Z host postfix/cleanup[2]: ABC123: message-id=<first@example.test>",
		"2026-10-24T10:00:00Z host postfix/qmgr[3]: ABC123: from=<alice@example.test>, size=10, nrcpt=1 (queue active)",
		"2026-10-24T10:00:01Z host postfix/smtp[4]: ABC123: to=<external@remote.test>, relay=mx.remote.test[127.0.0.2]:25, delay=1, dsn=2.0.0, status=sent (250 accepted)",
		"2026-10-24T10:00:01Z host postfix/qmgr[3]: ABC123: removed",
		"2026-10-24T10:01:00Z host postfix/cleanup[2]: ABC123: message-id=<second@example.test>",
		"2026-10-24T10:01:00Z host postfix/qmgr[3]: ABC123: from=<alice@example.test>, size=10, nrcpt=1 (queue active)",
		"2026-10-24T10:01:01Z host postfix/smtp[4]: ABC123: to=<external@remote.test>, relay=mx.remote.test[127.0.0.2]:25, delay=1, dsn=5.0.0, status=bounced (550 rejected)",
	}, "\n") + "\n"
	batch := sourceBatch(data)
	if err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "node"}, batch); err != nil {
		t.Fatal(err)
	}
	var owned, unknown, lifecycles int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM delivery_events WHERE tenant_id=$1 AND message_id='first@example.test' AND recipient='external@remote.test' AND status='sent'", tenant).Scan(&owned); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM delivery_events WHERE tenant_id IS NULL AND message_id='second@example.test'").Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(DISTINCT lifecycle_id) FROM delivery_events WHERE queue_id='ABC123'").Scan(&lifecycles); err != nil {
		t.Fatal(err)
	}
	if owned != 1 || unknown != 3 || lifecycles != 2 {
		t.Fatalf("wrong ownership/lifecycle: owned=%d unknown=%d lifecycles=%d", owned, unknown, lifecycles)
	}
	if err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "node"}, batch); !errors.Is(err, ErrCheckpointMoved) {
		t.Fatalf("stale worker replay: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM delivery_events").Scan(&count); err != nil || count != 8 {
		t.Fatalf("source replay duplicated events: %d %v", count, err)
	}
	q := store.New(pool.Pool)
	params := store.PlatformRecordCollectedDeliveryEventParams{Instance: "unknown/postfix", QueueID: "UNKNOWN", Sender: "spoof@example.test", SenderDomain: "example.test", Status: "unknown", OccurredAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, SourceEventID: "same-unknown-source", LifecycleID: "unknown-life"}
	for i := 0; i < 2; i++ {
		inserted, err := q.PlatformRecordCollectedDeliveryEvent(ctx, params)
		if err != nil || inserted != int64(1-i) {
			t.Fatalf("NULL tenant dedupe failed: %d %v", inserted, err)
		}
	}
}

func TestCollectorCheckpointAndEventsRollbackTogether(t *testing.T) {
	pool, _ := collectorDatabase(t)
	ctx := context.Background()
	collector := &Collector{Pool: pool, Stream: "rollback"}
	if _, err := pool.Exec(ctx, "ALTER TABLE collector_checkpoints ADD CONSTRAINT injected_checkpoint_failure CHECK(byte_offset<10)"); err != nil {
		t.Fatal(err)
	}
	batch := sourceBatch("2026-10-24T10:00:00Z host postfix/cleanup[2]: ABC123: message-id=<atomic@example.test>\n")
	if err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "rollback"}, batch); err == nil {
		t.Fatal("checkpoint fault was swallowed")
	}
	var events, checkpoints, contexts int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM delivery_events),(SELECT count(*) FROM collector_checkpoints),(SELECT count(*) FROM delivery_queue_contexts)").Scan(&events, &checkpoints, &contexts); err != nil {
		t.Fatal(err)
	}
	if events != 0 || checkpoints != 0 || contexts != 0 {
		t.Fatalf("partial batch escaped rollback: %d %d %d", events, checkpoints, contexts)
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE collector_checkpoints DROP CONSTRAINT injected_checkpoint_failure"); err != nil {
		t.Fatal(err)
	}
	if err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "rollback"}, batch); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.New(pool.Pool).PlatformGetCollectorCheckpoint(ctx, "rollback")
	if err != nil || checkpoint.ByteOffset != batch.EndOffset {
		t.Fatalf("retry did not advance atomically: %+v %v", checkpoint, err)
	}
}

func TestCollectorsFenceConcurrentCursorAndTenantCannotReadSource(t *testing.T) {
	pool, _ := collectorDatabase(t)
	ctx := context.Background()
	collector := &Collector{Pool: pool, Stream: "concurrent"}
	batch := sourceBatch("2026-10-24T10:00:00Z host postfix/cleanup[2]: ABC123: message-id=<parallel@example.test>\n")
	var group sync.WaitGroup
	var mutex sync.Mutex
	successes := 0
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "concurrent"}, batch)
			mutex.Lock()
			defer mutex.Unlock()
			if err == nil {
				successes++
			} else if !errors.Is(err, ErrCheckpointMoved) {
				t.Errorf("unexpected cursor competitor: %v", err)
			}
		}()
	}
	group.Wait()
	if successes != 1 {
		t.Fatalf("concurrent commits=%d", successes)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET LOCAL ROLE urmail_app"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM collector_checkpoints").Scan(&count); err == nil {
		t.Fatal("tenant role read platform source cursor")
	}
}

func TestCollectorRejectsInvalidBatchBeforeWrite(t *testing.T) {
	collector := &Collector{Stream: "test"}
	err := collector.Apply(context.Background(), store.CollectorCheckpoints{}, sourceBatch("partial"))
	if err == nil {
		t.Fatal("misconfigured collector accepted batch")
	}
	if _, err := canonicalTenant(context.Background(), nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorDoesNotTrustSASLTextInMessageHeaders(t *testing.T) {
	pool, _ := collectorDatabase(t)
	ctx := context.Background()
	collector := &Collector{Pool: pool, Stream: "spoof"}
	batch := sourceBatch("2026-10-24T10:00:00Z host postfix/cleanup[2]: ABC123: message-id=<forged, sasl_method=PLAIN, sasl_username=alice@example.test,spoof>\n")
	if err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "spoof"}, batch); err != nil {
		t.Fatal(err)
	}
	var owned int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM delivery_events WHERE tenant_id IS NOT NULL").Scan(&owned); err != nil || owned != 0 {
		t.Fatalf("untrusted header gained ownership: %d %v", owned, err)
	}
}

func TestCollectorCannotAuthenticateFromEnvelopeTextOnSMTPDLine(t *testing.T) {
	pool, _ := collectorDatabase(t)
	ctx := context.Background()
	collector := &Collector{Pool: pool, Stream: "smtpd-spoof"}
	batch := sourceBatch("2026-10-24T10:00:00Z host postfix/smtpd[2]: ABC123: reject: from=<forged, sasl_method=PLAIN, sasl_username=alice@example.test>\n2026-10-24T10:00:01Z host postfix/smtp[3]: ABC123: to=<external@remote.test>, relay=remote.test[127.0.0.2], dsn=2.0.0, status=sent (250 accepted)\n")
	if err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "smtpd-spoof"}, batch); err != nil {
		t.Fatal(err)
	}
	var attributed int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM delivery_events WHERE tenant_id IS NOT NULL").Scan(&attributed); err != nil || attributed != 0 {
		t.Fatalf("forged SASL text authenticated: %d %v", attributed, err)
	}
}

func TestNewUnauthenticatedClientResetsReusedQueueEvenWithSameMessageID(t *testing.T) {
	pool, _ := collectorDatabase(t)
	ctx := context.Background()
	collector := &Collector{Pool: pool, Stream: "reuse"}
	data := "2026-10-24T10:00:00Z host postfix/smtpd[1]: ABC123: client=old.test[127.0.0.1], sasl_method=PLAIN, sasl_username=alice@example.test\n2026-10-24T10:00:00Z host postfix/cleanup[2]: ABC123: message-id=<same@example.test>\n2026-10-24T10:00:01Z host postfix/smtp[3]: ABC123: to=<first@external.test>, dsn=2.0.0, status=sent (250 accepted)\n2026-10-24T10:05:00Z host postfix/smtpd[1]: ABC123: client=new.test[127.0.0.2]\n2026-10-24T10:05:00Z host postfix/cleanup[2]: ABC123: message-id=<same@example.test>\n2026-10-24T10:05:01Z host postfix/smtp[3]: ABC123: to=<private@external.test>, dsn=2.0.0, status=sent (250 accepted)\n"
	if err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "reuse"}, sourceBatch(data)); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM delivery_events WHERE recipient='private@external.test' AND tenant_id IS NOT NULL").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("new unauthenticated queue inherited old principal: %d %v", leaked, err)
	}
	var lifecycles int
	if err := pool.QueryRow(ctx, "SELECT count(DISTINCT lifecycle_id) FROM delivery_events WHERE queue_id='ABC123'").Scan(&lifecycles); err != nil || lifecycles != 2 {
		t.Fatalf("queue lifecycle not reset: %d %v", lifecycles, err)
	}
}

func TestCollectorBacklogDoesNotPretendObservationIsCurrent(t *testing.T) {
	pool, _ := collectorDatabase(t)
	ctx := context.Background()
	collector := &Collector{Pool: pool, Stream: "backlog"}
	batch := sourceBatch("2026-10-24T10:00:00Z host postfix/cleanup[2]: ABC123: message-id=<backlog@example.test>\n")
	batch.MoreAvailable = true
	if err := collector.Apply(ctx, store.CollectorCheckpoints{Stream: "backlog"}, batch); err != nil {
		t.Fatal(err)
	}
	q := store.New(pool.Pool)
	checkpoint, err := q.PlatformGetCollectorCheckpoint(ctx, "backlog")
	if err != nil || checkpoint.ObservedAt.Valid {
		t.Fatalf("partial source claimed freshness: %+v %v", checkpoint, err)
	}
	caughtUp := agentd.MailLogBatch{Identity: batch.Identity, StartOffset: batch.EndOffset, EndOffset: batch.EndOffset, ObservedAt: time.Now().UTC()}
	if err = collector.Apply(ctx, checkpoint, caughtUp); err != nil {
		t.Fatal(err)
	}
	checkpoint, err = q.PlatformGetCollectorCheckpoint(ctx, "backlog")
	if err != nil || !checkpoint.ObservedAt.Valid {
		t.Fatalf("caught-up source stayed unknown: %+v %v", checkpoint, err)
	}
}
