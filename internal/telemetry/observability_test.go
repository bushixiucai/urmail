package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/store"
)

func healthyObservation() Observation {
	now := time.Now().UTC()
	certificate := now.Add(60 * 24 * time.Hour)
	host := &agentd.HostObservation{ObservedAt: now, CPUTotal: 100, CPUIdle: 80, MemoryTotal: 100, MemoryAvailable: 50, CertificateExpiresAt: &certificate, Filesystems: map[string]agentd.FilesystemObservation{}, Daemons: map[string]bool{}}
	for _, path := range []string{"/var/mail", "/var/lib/urmail"} {
		host.Filesystems[path] = agentd.FilesystemObservation{Total: 100, Available: 50, Inodes: 100, AvailableInodes: 50}
	}
	for _, unit := range []string{"postfix", "dovecot", "nginx", "opendkim", "nsd"} {
		host.Daemons[unit] = true
	}
	success := now.Add(-time.Hour)
	return Observation{Now: now, Host: host, Configuration: &agentd.ApplyStatus{Desired: "same", Applied: "same", Ready: true}, Queue: &agentd.QueueSnapshot{ObservedAt: now}, CollectorObserved: &now,
		Backup: &BackupObservation{AvailableState: true, LatestState: "success", LastSuccessAt: &success, MaxAgeSeconds: 26 * 3600}}
}

func TestOperationalConditionsTriggerAndRecoverWithoutPIILabels(t *testing.T) {
	observation := healthyObservation()
	thresholds := DefaultThresholds()
	conditions, _ := Conditions(observation, thresholds)
	for kind, firing := range conditions {
		if firing {
			t.Fatalf("healthy fired %s", kind)
		}
		if !strings.HasPrefix(kind, "telemetry_") {
			t.Fatal(kind)
		}
	}
	observation.Queue.Entries = []agentd.QueueEntry{{QueueID: "not-a-label", ArrivalTime: observation.Now.Add(-2 * time.Hour).Unix()}}
	observation.Host.MemoryAvailable = 1
	observation.Host.Daemons["dovecot"] = false
	expiry := observation.Now.Add(time.Hour)
	observation.Host.CertificateExpiresAt = &expiry
	observation.CollectionGap = true
	conditions, values := Conditions(observation, thresholds)
	for _, kind := range []string{"telemetry_queue_pressure", "telemetry_resource_pressure", "telemetry_daemon_dovecot_down", "telemetry_certificate_expiring", "telemetry_collection_gap"} {
		if !conditions[kind] {
			t.Fatalf("failure did not fire %s", kind)
		}
	}
	if values["queue_oldest_age_seconds"] < 3600 {
		t.Fatal(values)
	}
	observation = healthyObservation()
	conditions, _ = Conditions(observation, thresholds)
	for kind, firing := range conditions {
		if firing {
			t.Fatalf("recovery stayed firing %s", kind)
		}
	}
	absent, _ := Conditions(Observation{Now: time.Now()}, thresholds)
	if !absent["telemetry_host_unavailable"] || !absent["telemetry_queue_unavailable"] || !absent["telemetry_collector_stale"] || !absent["telemetry_config_unready"] {
		t.Fatal("missing data became healthy")
	}
}

type fixedObserver struct{ Observation Observation }

func (f *fixedObserver) Observe(context.Context) (Observation, error) { return f.Observation, nil }

func TestOperationalAlertsAndRecoveryNotificationsAreDurableAndDeduplicated(t *testing.T) {
	pool, _ := collectorDatabase(t)
	ctx := context.Background()
	q := store.New(pool.Pool)
	source := &fixedObserver{Observation: healthyObservation()}
	evaluator := &Evaluator{Pool: pool, Source: source, Stream: "observability"}
	if err := q.PlatformEnsureCollectorCheckpoint(ctx, "observability"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE collector_checkpoints SET observed_at=now() WHERE stream='observability'"); err != nil {
		t.Fatal(err)
	}
	source.Observation.Queue.Truncated = true
	for i := 0; i < 2; i++ {
		if err := evaluator.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var firing, notices int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM operational_alerts WHERE kind='telemetry_queue_pressure' AND state='firing'").Scan(&firing); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE kind='alert_firing' AND params->>'kind'='telemetry_queue_pressure' AND params->>'state'='firing'").Scan(&notices); err != nil || firing != 1 || notices != 1 {
		t.Fatalf("firing duplicate or lost: %d %d %v", firing, notices, err)
	}
	source.Observation.Queue.Truncated = false
	for i := 0; i < 2; i++ {
		if err := evaluator.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var recovered int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE kind='alert_resolved' AND params->>'kind'='telemetry_queue_pressure' AND params->>'state'='resolved'").Scan(&recovered); err != nil || recovered != 1 {
		t.Fatalf("recovery notification missing/duplicated: %d %v", recovered, err)
	}
	var resolved pgtype.Timestamptz
	if err := pool.QueryRow(ctx, "SELECT resolved_at FROM operational_alerts WHERE kind='telemetry_queue_pressure'").Scan(&resolved); err != nil || !resolved.Valid {
		t.Fatalf("recovery not durable: %v %v", resolved, err)
	}
}

func TestOperationalEvaluatorRaisesAndResolvesBackupAlertsWithNotifications(t *testing.T) {
	pool, _ := collectorDatabase(t)
	ctx := context.Background()
	q := store.New(pool.Pool)
	source := &fixedObserver{Observation: healthyObservation()}
	evaluator := &Evaluator{Pool: pool, Source: source, Stream: "observability"}
	if err := q.PlatformEnsureCollectorCheckpoint(ctx, "observability"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE collector_checkpoints SET observed_at=now() WHERE stream='observability'"); err != nil {
		t.Fatal(err)
	}
	// A failed latest backup run must raise telemetry_backup_failed and
	// enqueue exactly one alert_firing notice; a stale success raises stale.
	if _, err := pool.Exec(ctx, "INSERT INTO backup_runs(run_uid,state,started_at,finished_at) VALUES ('runfail0001','failed',now()-interval '2 hours',now()-interval '1 hour')"); err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	var failed int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM operational_alerts WHERE kind='telemetry_backup_failed' AND state='firing'").Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("backup failure not firing: %d %v", failed, err)
	}
	var notices int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE kind='alert_firing' AND params->>'kind'='telemetry_backup_failed'").Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("backup firing notice missing or duplicated: %d %v", notices, err)
	}
	// A second poll of the same firing episode must not re-notify.
	if err := evaluator.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE kind='alert_firing' AND params->>'kind'='telemetry_backup_failed'").Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("firing episode re-notified: %d %v", notices, err)
	}
	// Recovery: a fresh successful run clears both backup conditions.
	if _, err := pool.Exec(ctx, "INSERT INTO backup_runs(run_uid,state,started_at,finished_at) VALUES ('runok000001','success',now()-interval '2 hours',now())"); err != nil {
		t.Fatal(err)
	}
	source.Observation.Backup.LatestState = "success"
	source.Observation.Backup.LastSuccessAt = func() *time.Time { v := time.Now().UTC(); return &v }()
	if err := evaluator.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	var resolved, resolvedNotices int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM operational_alerts WHERE kind='telemetry_backup_failed' AND state='resolved' AND resolved_at IS NOT NULL").Scan(&resolved); err != nil || resolved != 1 {
		t.Fatalf("backup failure not resolved durably: %d %v", resolved, err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE kind='alert_resolved' AND params->>'kind'='telemetry_backup_failed'").Scan(&resolvedNotices); err != nil || resolvedNotices != 1 {
		t.Fatalf("backup resolution notice missing or duplicated: %d %v", resolvedNotices, err)
	}
	// The readiness report must be persisted as a read-only view and must
	// separate process-up from mail-ready and config convergence.
	if err := evaluator.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	report := evaluator.Readiness()
	if !report.ProcessUp || !report.MailReady || !report.ConfigConverged {
		t.Fatalf("healthy readiness report wrong: %+v", report)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT value FROM settings WHERE scope='platform' AND scope_id=0 AND key='observability_readiness'").Scan(&stored); err != nil {
		t.Fatalf("readiness view not persisted: %v", err)
	}
	var view ReadinessReport
	if err := json.Unmarshal(stored, &view); err != nil || !view.MailReady || !view.ConfigConverged {
		t.Fatalf("persisted readiness view invalid: %s %v %+v", stored, err, view)
	}
}
