package telemetry

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
)

// readinessSettingKey is the platform settings row where the evaluator
// publishes its last structured readiness report for read-only consumption
// (proposed internal API path: GET /api/v1/internal/status/ready).
const readinessSettingKey = "observability_readiness"

// ReadinessReport separates the three facts AGENTS.md requires operators to
// distinguish: process liveness, mail readiness and configuration
// convergence. It never reuses healthz semantics for mail state.
type ReadinessReport struct {
	ProcessUp       bool      `json:"process_up"`
	MailReady       bool      `json:"mail_ready"`
	ConfigConverged bool      `json:"config_converged"`
	GeneratedAt     time.Time `json:"generated_at"`
	FiringAlerts    []string  `json:"firing_alerts"`
}

// Readiness returns the most recent evaluation's report; a zero report means
// the evaluator has not completed a poll yet and must not imply readiness.
func (e *Evaluator) Readiness() ReadinessReport {
	e.readinessMu.Lock()
	defer e.readinessMu.Unlock()
	return e.lastReadiness
}

// recordReadiness derives the report from firing conditions and mirrors it
// into platform settings as a read-only view. Persistence of the report is
// best effort for the view; a settings failure is surfaced to the caller but
// never rolls back the already committed alert state.
func (e *Evaluator) recordReadiness(ctx context.Context, conditions map[string]bool) error {
	report := ReadinessReport{ProcessUp: true, GeneratedAt: time.Now().UTC(), FiringAlerts: []string{}}
	mailReady := true
	for kind, firing := range conditions {
		if !firing {
			continue
		}
		report.FiringAlerts = append(report.FiringAlerts, kind)
		switch kind {
		case "telemetry_config_unready", "telemetry_daemon_postfix_down", "telemetry_daemon_dovecot_down",
			"telemetry_collector_stale", "telemetry_collection_gap", "telemetry_queue_unavailable":
			mailReady = false
		}
	}
	sort.Strings(report.FiringAlerts)
	report.MailReady = mailReady
	report.ConfigConverged = !conditions["telemetry_config_unready"]
	e.readinessMu.Lock()
	e.lastReadiness = report
	e.readinessMu.Unlock()
	if e.Pool == nil {
		return nil
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return store.New(e.Pool.Pool).PlatformUpsertSetting(ctx, store.PlatformUpsertSettingParams{Key: readinessSettingKey, Value: raw})
}
