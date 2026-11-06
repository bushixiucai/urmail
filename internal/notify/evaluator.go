// Package notify evaluates durable operational conditions and notification work.
package notify

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

var openAlerts = prometheus.NewGauge(prometheus.GaugeOpts{Name: "urmail_payment_manual_reviews_open", Help: "Verified receipts requiring manual resolution; never contains payment or tenant identifiers."})

var workStates = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "urmail_durable_work_items", Help: "Durable work count by bounded workflow and state; no tenant, content or provider identifiers."}, []string{"workflow", "state"})

// OPS-103 notification observability: monotonic enqueue and delivery counters
// per channel and kind, plus the durable outbox depth per state machine state.
var (
	notificationEnqueued   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_notifications_enqueued_total", Help: "Lifecycle notifications appended to the durable outbox by channel and kind."}, []string{"channel", "kind"})
	notificationDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_notifications_delivery_total", Help: "Email notification delivery attempts by channel, kind and outcome (sent, retrying, uncertain, unfinished)."}, []string{"channel", "kind", "outcome"})
	notificationOutbox     = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "urmail_notification_outbox_items", Help: "Durable notification count per channel, kind and delivery state; no tenant or content identifiers."}, []string{"channel", "kind", "state"})
)

// lastEnqueuedID tracks the outbox watermark so idempotent enqueues are
// counted exactly once even though the underlying rows are deduplicated.
var lastEnqueuedID int64

func init() {
	prometheus.MustRegister(openAlerts, workStates, notificationEnqueued, notificationDeliveries, notificationOutbox)
}

// Evaluate commits condition state and deduplicated notifications together.
func Evaluate(ctx context.Context, pool *db.Pool) error {
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformRefreshDNSOnboardingAlerts(ctx); err != nil {
		return err
	}
	if err = q.PlatformEnqueueDNSOnboardingAlerts(ctx); err != nil {
		return err
	}
	if err = q.PlatformRefreshAIAlerts(ctx); err != nil {
		return err
	}
	if err = q.PlatformEnqueueAIAlerts(ctx); err != nil {
		return err
	}
	states, err := q.PlatformOperationalStateCounts(ctx)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	openAlerts.Set(0)
	workStates.Reset()
	for _, row := range states {
		workStates.WithLabelValues(row.Kind, row.Status).Set(float64(row.Count))
	}
	if err = observeNotifications(ctx, pool); err != nil {
		return err
	}
	return nil
}

// observeNotifications records the outbox watermark, monotonic enqueue counts
// and the durable per-state depth after the evaluation transaction commits.
func observeNotifications(ctx context.Context, pool *db.Pool) error {
	queries := store.New(pool)
	maxID, err := queries.PlatformMaxNotificationID(ctx)
	if err != nil {
		return err
	}
	if maxID > lastEnqueuedID {
		counts, err := queries.PlatformCountNewNotifications(ctx, lastEnqueuedID)
		if err != nil {
			return err
		}
		for _, entry := range counts {
			notificationEnqueued.WithLabelValues(entry.Channel, entry.Kind).Add(float64(entry.Count))
		}
		lastEnqueuedID = maxID
	}
	outbox, err := queries.PlatformNotificationOutboxCounts(ctx)
	if err != nil {
		return err
	}
	notificationOutbox.Reset()
	for _, entry := range outbox {
		notificationOutbox.WithLabelValues(entry.Channel, entry.Kind, entry.State).Set(float64(entry.Count))
	}
	return nil
}

// Run retains firing alerts until their source facts have been resolved.
func Run(ctx context.Context, pool *db.Pool, logger *zap.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := Evaluate(ctx, pool); err != nil {
			logger.Warn("operational alert evaluation failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
