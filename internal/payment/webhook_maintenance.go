package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

var webhookRejections = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "urmail_payment_rejection_diagnostics_total",
	Help: "Unverified callback diagnostic outcomes with bounded labels.",
}, []string{"result"})

type rejectionRecorder interface {
	PlatformRecordRejectedWebhook(context.Context, store.PlatformRecordRejectedWebhookParams) (bool, error)
}

// RecordRejectedWebhook keeps only bounded metadata. Diagnostics cannot turn an
// invalid callback into a retryable payment failure or a financial inbox event.
func RecordRejectedWebhook(ctx context.Context, recorder rejectionRecorder, merchantID int64, reason string, raw []byte) {
	if recorder == nil || merchantID < 1 || len(raw) > 1<<20 || (reason != "verification_rejected" && reason != "merchant_rejected") {
		webhookRejections.WithLabelValues("invalid_metadata").Inc()
		return
	}
	digest := sha256.Sum256(raw)
	budgetCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	recorded, err := recorder.PlatformRecordRejectedWebhook(budgetCtx, store.PlatformRecordRejectedWebhookParams{
		MerchantID: merchantID, Reason: reason, PayloadHash: hex.EncodeToString(digest[:]), PayloadBytes: int32(len(raw)), // #nosec G115 -- raw is bounded to 1 MiB above.
	})
	switch {
	case err != nil:
		webhookRejections.WithLabelValues("unavailable").Inc()
	case recorded:
		webhookRejections.WithLabelValues("recorded").Inc()
	default:
		webhookRejections.WithLabelValues("budget_exhausted").Inc()
	}
}

// WebhookMaintenanceReport contains counts only, never callback bodies or keys.
type WebhookMaintenanceReport struct {
	Before          store.PlatformWebhookMaintenanceStatsRow `json:"before"`
	CompactedBodies int64                                    `json:"compacted_bodies"`
	PurgedMetadata  int64                                    `json:"purged_metadata"`
	PurgedLegacy    int64                                    `json:"purged_legacy"`
}

// MaintainWebhookDiagnostics provides a read-only dry run or one bounded batch.
// Only legacy unverified events with their trusted result marker are compacted;
// verified financial event evidence and uncertain work are not selected.
func MaintainWebhookDiagnostics(ctx context.Context, pool *db.Pool, apply bool) (WebhookMaintenanceReport, error) {
	var report WebhookMaintenanceReport
	if pool == nil {
		return report, errors.New("webhook maintenance requires a platform pool")
	}
	q := store.New(pool)
	var err error
	if report.Before, err = q.PlatformWebhookMaintenanceStats(ctx); err != nil || !apply {
		return report, err
	}
	if report.CompactedBodies, err = q.PlatformCompactLegacyRejectedWebhooks(ctx); err != nil {
		return report, err
	}
	if report.PurgedMetadata, err = q.PlatformPurgeRejectedWebhookMetadata(ctx); err != nil {
		return report, err
	}
	report.PurgedLegacy, err = q.PlatformPurgeLegacyRejectedWebhooks(ctx)
	return report, err
}

// RunWebhookMaintenance bounds retained diagnostics while the caller's service
// lifecycle is active. The production caller must suppress it in quarantine.
func RunWebhookMaintenance(ctx context.Context, pool *db.Pool, logger *zap.Logger) {
	if logger == nil {
		logger = zap.NewNop()
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := store.New(pool).PlatformPurgeRejectedWebhookMetadata(batchCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("webhook diagnostic maintenance requires attention")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
