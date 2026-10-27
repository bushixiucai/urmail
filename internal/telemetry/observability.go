package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

var databaseReachable = promauto.NewGauge(prometheus.GaugeOpts{Name: "urmail_database_reachable", Help: "Last operational platform database probe result; zero is unavailable."})

var observationMetric = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "urmail_observation_value", Help: "Fixed-name host, queue and readiness measurements; missing measurements are NaN."}, []string{"measurement"})
var observationFreshness = promauto.NewGauge(prometheus.GaugeOpts{Name: "urmail_observation_last_success_timestamp_seconds", Help: "Last committed operational observation; zero means not started."})

var conditionMetric = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "urmail_operational_condition", Help: "Bounded operational alert conditions: 1 firing, 0 resolved."}, []string{"kind"})

// Thresholds contains configurable operational defaults, never tenant billing limits.
type Thresholds struct {
	QueueCount            int     `json:"queue_count"`
	QueueAgeSeconds       int64   `json:"queue_age_seconds"`
	ResourceUsedFraction  float64 `json:"resource_used_fraction"`
	CertificateDays       int     `json:"certificate_days"`
	CollectorStaleSeconds int     `json:"collector_stale_seconds"`
	// OPS-003: a successful backup older than this (default 26h for a daily
	// schedule with margin) or a failed latest run raises a backup alert.
	BackupMaxAgeSeconds int64 `json:"backup_max_age_seconds"`
}

// DefaultThresholds returns conservative initial operator thresholds.
func DefaultThresholds() Thresholds {
	// CertificateDays fires at T-30 so the T-14 and T-3 severity escalations
	// are reachable before expiry.
	return Thresholds{QueueCount: 1000, QueueAgeSeconds: 3600, ResourceUsedFraction: 0.90, CertificateDays: 30, CollectorStaleSeconds: 120, BackupMaxAgeSeconds: 26 * 3600}
}
func (t Thresholds) validate() error {
	if t.QueueCount < 1 || t.QueueCount > 1000000 || t.QueueAgeSeconds < 1 || t.ResourceUsedFraction <= 0 || t.ResourceUsedFraction >= 1 || t.CertificateDays < 1 || t.BackupMaxAgeSeconds < 3600 || t.CollectorStaleSeconds < 10 || t.CollectorStaleSeconds > 3600 {
		return errors.New("invalid operational thresholds")
	}
	return nil
}

// Observation has explicit unavailable states; absent probes cannot imply health.
type Observation struct {
	Now               time.Time
	Host              *agentd.HostObservation
	Configuration     *agentd.ApplyStatus
	Queue             *agentd.QueueSnapshot
	CollectorObserved *time.Time
	CollectionGap     bool
	CPUUsed           *float64
	// OPS-003 additions: backup pipeline facts and an endpoint certificate
	// probe fallback when the host observer could not read a certificate.
	Backup                *BackupObservation
	CertificateProbedDays *float64
}

// Observer returns only fixed, typed root-agent observations.
type Observer interface {
	Observe(context.Context) (Observation, error)
}

// RuntimeObserver reads the host, generation and queue capabilities independently.
type RuntimeObserver struct{ Client *agent.Client }

// Observe retains successful probes and reports any unavailable source.
func (r RuntimeObserver) Observe(ctx context.Context) (Observation, error) {
	result := Observation{Now: time.Now().UTC()}
	if r.Client == nil {
		return result, errors.New("observation agent unavailable")
	}
	var failures []error
	for _, probe := range []struct {
		op     string
		assign func([]byte) error
	}{
		{agentd.OpHostObserve, func(raw []byte) error {
			var value agentd.HostObservation
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			result.Host = &value
			return nil
		}},
		{agentd.OpConfigStatus, func(raw []byte) error {
			var value agentd.ApplyStatus
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			result.Configuration = &value
			return nil
		}},
		{agentd.OpPostfixQueue, func(raw []byte) error {
			var value agentd.QueueSnapshot
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			result.Queue = &value
			return nil
		}},
	} {
		response, err := r.Client.Do(ctx, probe.op, struct{}{})
		if err == nil {
			err = probe.assign([]byte(response.Stdout))
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return result, errors.Join(failures...)
}

// Conditions derives only bounded alert kinds and explicit numeric measurements.
func Conditions(o Observation, t Thresholds) (map[string]bool, map[string]float64) {
	conditions := map[string]bool{"telemetry_host_unavailable": o.Host == nil, "telemetry_queue_unavailable": o.Queue == nil, "telemetry_config_unready": o.Configuration == nil || !o.Configuration.Ready || o.Configuration.Degraded || o.Configuration.Desired == "" || o.Configuration.Desired != o.Configuration.Applied, "telemetry_collector_stale": o.CollectorObserved == nil, "telemetry_collection_gap": o.CollectionGap}
	values := map[string]float64{}
	if o.CollectorObserved != nil {
		age := o.Now.Sub(*o.CollectorObserved).Seconds()
		values["collector_age_seconds"] = age
		conditions["telemetry_collector_stale"] = age > float64(t.CollectorStaleSeconds) || age < -60
	}
	if o.Queue != nil {
		oldest := float64(0)
		for _, entry := range o.Queue.Entries {
			age := o.Now.Sub(time.Unix(entry.ArrivalTime, 0)).Seconds()
			if age > oldest {
				oldest = age
			}
		}
		values["queue_count_lower_bound"] = float64(len(o.Queue.Entries))
		values["queue_oldest_age_seconds"] = oldest
		conditions["telemetry_queue_pressure"] = o.Queue.Truncated || len(o.Queue.Entries) >= t.QueueCount || oldest >= float64(t.QueueAgeSeconds)
	}
	if o.Host == nil && o.CertificateProbedDays != nil {
		values["certificate_remaining_seconds"] = *o.CertificateProbedDays * 86400
		conditions["telemetry_certificate_expiring"] = *o.CertificateProbedDays <= float64(t.CertificateDays)
	}
	if o.Backup != nil {
		stale, failed := BackupConditions(*o.Backup)
		conditions["telemetry_backup_stale"] = stale
		conditions["telemetry_backup_failed"] = failed
	} else {
		conditions["telemetry_backup_stale"] = true
		conditions["telemetry_backup_failed"] = false
	}
	if o.Host != nil {
		conditions["telemetry_resource_pressure"] = false
		if o.Host.MemoryTotal == 0 {
			conditions["telemetry_host_unavailable"] = true
		} else {
			used := 1 - float64(o.Host.MemoryAvailable)/float64(o.Host.MemoryTotal)
			values["memory_used_fraction"] = used
			conditions["telemetry_resource_pressure"] = used >= t.ResourceUsedFraction
		}
		if o.CPUUsed != nil {
			values["cpu_used_fraction"] = *o.CPUUsed
			if *o.CPUUsed >= t.ResourceUsedFraction {
				conditions["telemetry_resource_pressure"] = true
			}
		}
		for _, path := range []string{"/var/mail", "/var/lib/urmail"} {
			fs, ok := o.Host.Filesystems[path]
			if !ok || fs.Total == 0 || fs.Inodes == 0 {
				conditions["telemetry_host_unavailable"] = true
				continue
			}
			key := "archive"
			if path == "/var/mail" {
				key = "mail"
			}
			disk := 1 - float64(fs.Available)/float64(fs.Total)
			inodes := 1 - float64(fs.AvailableInodes)/float64(fs.Inodes)
			values[key+"_disk_used_fraction"] = disk
			values[key+"_inode_used_fraction"] = inodes
			if disk >= t.ResourceUsedFraction || inodes >= t.ResourceUsedFraction {
				conditions["telemetry_resource_pressure"] = true
			}
		}
		for _, unit := range []string{"postfix", "dovecot", "nginx", "opendkim", "nsd"} {
			active, ok := o.Host.Daemons[unit]
			conditions["telemetry_daemon_"+unit+"_down"] = !ok || !active
			if active {
				values["daemon_"+unit+"_active"] = 1
			} else {
				values["daemon_"+unit+"_active"] = 0
			}
		}
		conditions["telemetry_certificate_expiring"] = o.Host.CertificateUnavailable || o.Host.CertificateExpiresAt == nil
		if o.Host.CertificateExpiresAt != nil {
			remaining := o.Host.CertificateExpiresAt.Sub(o.Now).Seconds()
			values["certificate_remaining_seconds"] = remaining
			conditions["telemetry_certificate_expiring"] = remaining <= float64(t.CertificateDays)*86400
		}
	}
	return conditions, values
}

// Evaluator persists firing/recovery facts and notifications in one transaction.
type Evaluator struct {
	Pool   *db.Pool
	Source Observer
	Stream string
	// CertificateAddress optionally enables the OPS-003 TLS endpoint expiry
	// probe when the host observer cannot read the certificate file, for
	// example "mail.example.cn:443". Empty disables the probe.
	CertificateAddress string
	previousHost       *agentd.HostObservation
	readinessMu        sync.Mutex
	lastReadiness      ReadinessReport
}

// Poll evaluates the configured stream; collection failures remain visible conditions.
func (e *Evaluator) Poll(ctx context.Context) error {
	if e.Pool == nil || e.Source == nil || e.Stream == "" {
		return errors.New("observability requires configured source and stream")
	}
	q := store.New(e.Pool.Pool)
	thresholds := DefaultThresholds()
	started := time.Now()
	settings, err := q.PlatformGetObservabilityThresholds(ctx)
	if err != nil {
		databaseReachable.Set(0)
		return err
	}
	databaseReachable.Set(1)
	for _, setting := range settings {
		if setting.Key == "observability_thresholds" {
			if json.Unmarshal(setting.Value, &thresholds) != nil || thresholds.validate() != nil {
				return errors.New("invalid operational thresholds")
			}
		}
	}
	backup, backupErr := q.PlatformBackupFreshness(ctx)
	if backupErr != nil && !errors.Is(backupErr, pgx.ErrNoRows) {
		return backupErr
	}
	observation, probeErr := e.Source.Observe(ctx)
	if observation.Now.IsZero() {
		observation.Now = time.Now().UTC()
	}
	health, err := q.PlatformCollectorHealth(ctx)
	if err != nil {
		return err
	}
	for _, source := range health {
		if source.Stream == e.Stream {
			if source.ObservedAt.Valid {
				timeValue := source.ObservedAt.Time
				observation.CollectorObserved = &timeValue
			}
			observation.CollectionGap = source.GapReason.Valid
		}
	}
	if observation.Host != nil {
		if previous := e.previousHost; previous != nil && observation.Host.CPUTotal > previous.CPUTotal && observation.Host.CPUIdle >= previous.CPUIdle {
			used := 1 - float64(observation.Host.CPUIdle-previous.CPUIdle)/float64(observation.Host.CPUTotal-previous.CPUTotal)
			if used >= 0 && used <= 1 {
				observation.CPUUsed = &used
			}
		}
		e.previousHost = observation.Host
	}
	if backupErr == nil {
		observation.Backup = &BackupObservation{LatestState: backup.LatestState, AvailableState: true, MaxAgeSeconds: float64(thresholds.BackupMaxAgeSeconds)}
		if backup.LastSuccessAt.Valid {
			success := backup.LastSuccessAt.Time
			observation.Backup.LastSuccessAt = &success
		}
	}
	if e.CertificateAddress != "" && (observation.Host == nil || (observation.Host.CertificateExpiresAt == nil && observation.Host.CertificateUnavailable)) {
		probeCtx, probeCancel := context.WithTimeout(ctx, 6*time.Second)
		if days, probeErr := ProbeCertificateDaysRemaining(probeCtx, e.CertificateAddress, time.Now().UTC()); probeErr == nil {
			observation.CertificateProbedDays = &days
		}
		probeCancel()
	}
	conditions, values := Conditions(observation, thresholds)
	stats := e.Pool.Stat()
	values["db_platform_connections"] = float64(stats.TotalConns())
	values["db_platform_acquired_connections"] = float64(stats.AcquiredConns())
	values["db_platform_idle_connections"] = float64(stats.IdleConns())
	values["db_platform_acquire_waits"] = float64(stats.EmptyAcquireCount())
	tx, err := e.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := store.New(tx)
	for kind, firing := range conditions {
		if err = queries.PlatformSetOperationalCondition(ctx, store.PlatformSetOperationalConditionParams{AlertKey: e.Stream + ":" + kind, Kind: kind, Firing: firing}); err != nil {
			return err
		}
	}
	if err = queries.PlatformEnqueueObservabilityAlerts(ctx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	observationFreshness.Set(float64(time.Now().Unix()))
	for _, key := range []string{"collector_age_seconds", "queue_count_lower_bound", "queue_oldest_age_seconds", "memory_used_fraction", "cpu_used_fraction", "mail_disk_used_fraction", "archive_disk_used_fraction", "mail_inode_used_fraction", "archive_inode_used_fraction", "certificate_remaining_seconds"} {
		observationMetric.WithLabelValues(key).Set(math.NaN())
	}
	for key, value := range values {
		observationMetric.WithLabelValues(key).Set(value)
	}
	for kind, firing := range conditions {
		if kind == "telemetry_certificate_expiring" {
			PublishAlertSeverity(kind, firing, e.certificateSeverity(values))
		} else {
			PublishAlert(kind, firing)
		}
	}
	if err = e.recordReadiness(ctx, conditions); err != nil {
		probeErr = errors.Join(probeErr, err)
	}
	evaluationDuration.Observe(time.Since(started).Seconds())
	return probeErr
}

// certificateSeverity maps the observed certificate remaining seconds onto
// the T-30/T-14/T-3 severity tiers; an empty result keeps the default label.
func (e *Evaluator) certificateSeverity(values map[string]float64) string {
	if seconds, ok := values["certificate_remaining_seconds"]; ok {
		return CertificateExpirySeverity(seconds / 86400)
	}
	return ""
}

// Start runs bounded evaluation without emitting mail or unscoped raw log contents.
func (e *Evaluator) Start(ctx context.Context, logger *zap.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if config.RestoreQuarantined() {
			return
		}
		pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := e.Poll(pollCtx)
		cancel()
		if err != nil {
			logger.Warn("operational observation incomplete", zap.String("source", strings.TrimSpace(e.Stream)))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
