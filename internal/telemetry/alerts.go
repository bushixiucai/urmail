package telemetry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// alertActive exposes firing alerts with an explicit bounded severity label so
// operators can route pages without parsing alert kinds.
var alertActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "urmail_alert_active",
	Help: "Firing operational alerts by bounded kind and severity; one means firing.",
}, []string{"kind", "severity"})

// evaluationDuration bounds evaluator cost visibility; evaluation must stay
// far below its thirty second poll period.
var evaluationDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "urmail_alert_evaluation_seconds",
	Help:    "Duration of one alert evaluation cycle including probes and commit.",
	Buckets: prometheus.DefBuckets,
})

// Severity labels are fixed strings; they never carry tenant or address data.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// AlertSeverity maps a bounded operational alert kind to its fixed severity.
// Unknown kinds stay visible at info instead of being dropped silently.
func AlertSeverity(kind string) string {
	switch kind {
	case "telemetry_host_unavailable", "telemetry_queue_unavailable", "telemetry_config_unready",
		"telemetry_collection_gap", "telemetry_daemon_postfix_down", "telemetry_daemon_dovecot_down",
		"telemetry_daemon_nginx_down", "telemetry_daemon_opendkim_down", "telemetry_daemon_nsd_down",
		"telemetry_backup_failed":
		return SeverityCritical
	case "telemetry_queue_pressure", "telemetry_resource_pressure", "telemetry_collector_stale",
		"telemetry_backup_stale", "archive_reconciliation", "archive_orphan_original",
		"spam_milter_unavailable":
		return SeverityWarning
	case "telemetry_certificate_expiring":
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

// PublishAlert records a condition transition on both the legacy fixed-kind
// condition gauge and the severity-labeled alert gauge.
func PublishAlert(kind string, firing bool) {
	PublishAlertSeverity(kind, firing, "")
}

// PublishAlertSeverity is PublishAlert with an explicit severity override so
// tiered conditions such as certificate expiry can escalate in place. An
// empty severity falls back to the fixed kind mapping.
func PublishAlertSeverity(kind string, firing bool, severity string) {
	PublishCondition(kind, firing)
	if severity == "" {
		severity = AlertSeverity(kind)
	}
	if firing {
		alertActive.WithLabelValues(kind, severity).Set(1)
	} else {
		alertActive.DeleteLabelValues(kind, severity)
	}
}

// CertificateDaysRemaining parses the first PEM certificate block and returns
// the fractional days between now and its NotAfter. It is a pure function so
// tests can drive it with generated short-lived certificates.
func CertificateDaysRemaining(now time.Time, pemBytes []byte) (float64, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return 0, errors.New("no PEM certificate block")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return 0, fmt.Errorf("parse certificate: %w", err)
	}
	return leaf.NotAfter.Sub(now).Hours() / 24, nil
}

// CertificateExpirySeverity escalates T-30/T-14/T-3 warning tiers. The alert
// kind stays telemetry_certificate_expiring; only the severity label moves.
func CertificateExpirySeverity(daysRemaining float64) string {
	switch {
	case daysRemaining <= 3:
		return SeverityCritical
	case daysRemaining <= 14:
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

// ProbeCertificateDaysRemaining dials address over TLS, ignores chain trust
// (expiry is a time fact, not a trust fact) and returns the endpoint
// certificate's days remaining. Failures return an error; they never imply a
// healthy long-lived certificate.
func ProbeCertificateDaysRemaining(ctx context.Context, address string, now time.Time) (float64, error) {
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true}} // #nosec G402 -- expiry probe must observe untrusted chains too.
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, err
	}
	defer func() { _ = raw.Close() }()
	connection, ok := raw.(*tls.Conn)
	if !ok {
		return 0, errors.New("endpoint did not negotiate TLS")
	}
	state := connection.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return 0, errors.New("no peer certificate")
	}
	return state.PeerCertificates[0].NotAfter.Sub(now).Hours() / 24, nil
}

// BackupObservation carries only fixed backup pipeline facts from backup_runs.
type BackupObservation struct {
	LatestState    string
	LatestStart    *time.Time
	LastSuccessAt  *time.Time
	MaxAgeSeconds  float64
	Now            time.Time
	AvailableState bool
}

// BackupConditions derives the two backup alert kinds. Missing history or a
// missing successful run both mean stale: absence of evidence is not health.
func BackupConditions(o BackupObservation) (stale, failed bool) {
	if !o.AvailableState {
		return true, false
	}
	failed = o.LatestState == "failed"
	maxAge := o.MaxAgeSeconds
	if maxAge <= 0 {
		maxAge = float64(DefaultThresholds().BackupMaxAgeSeconds)
	}
	if o.LastSuccessAt == nil {
		return true, failed
	}
	stale = o.Now.Sub(*o.LastSuccessAt).Seconds() > maxAge
	return stale, failed
}
