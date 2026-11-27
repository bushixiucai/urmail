package telemetry

import "testing"

func TestAuthSnapshotConditionsAndAlerts(t *testing.T) {
	thresholds := DefaultThresholds()
	if thresholds.AuthSnapshotMaxStalenessSeconds != 60 {
		t.Fatalf("default AuthSnapshotMaxStalenessSeconds = %d, want 60", thresholds.AuthSnapshotMaxStalenessSeconds)
	}

	// Healthy snapshot: not dirty, staleness within 60s.
	conds, vals := Conditions(Observation{
		AuthSnapshot: &AuthSnapshotObservation{Dirty: false, Built: true, StalenessSeconds: 12},
	}, thresholds)
	if vals["dovecot_auth_snapshot_dirty"] != 0 || vals["dovecot_auth_snapshot_staleness_seconds"] != 12 {
		t.Fatalf("unexpected metric values: %+v", vals)
	}
	if conds["telemetry_auth_snapshot_dirty"] || conds["telemetry_auth_snapshot_stale"] {
		t.Fatalf("healthy snapshot must not fire alerts: %+v", conds)
	}

	// Dirty & stale snapshot: both alerts fire at warning severity.
	conds, vals = Conditions(Observation{
		AuthSnapshot: &AuthSnapshotObservation{Dirty: true, Built: true, StalenessSeconds: 75},
	}, thresholds)
	if vals["dovecot_auth_snapshot_dirty"] != 1 {
		t.Fatalf("dirty metric = %v, want 1", vals["dovecot_auth_snapshot_dirty"])
	}
	if !conds["telemetry_auth_snapshot_dirty"] || !conds["telemetry_auth_snapshot_stale"] {
		t.Fatalf("expected dirty and stale alerts to fire: %+v", conds)
	}
	if AlertSeverity("telemetry_auth_snapshot_dirty") != SeverityWarning || AlertSeverity("telemetry_auth_snapshot_stale") != SeverityWarning {
		t.Fatal("expected warning severity for auth snapshot alerts")
	}
}
