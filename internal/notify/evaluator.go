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

func init() { prometheus.MustRegister(openAlerts, workStates) }

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
	if err = q.PlatformAdvanceScheduledPlanChanges(ctx); err != nil {
		return err
	}
	if err = q.PlatformRefreshRefundAlerts(ctx); err != nil {
		return err
	}
	if err = q.PlatformResolveRefundAlerts(ctx); err != nil {
		return err
	}
	if err = q.PlatformEnqueueRefundAlerts(ctx); err != nil {
		return err
	}
	if err = q.PlatformEnqueueSubscriptionReminders(ctx); err != nil {
		return err
	}
	if err = q.PlatformRefreshPaymentAlerts(ctx); err != nil {
		return err
	}
	if err = q.PlatformResolvePaymentAlerts(ctx); err != nil {
		return err
	}
	if err = q.PlatformEnqueuePaymentAlerts(ctx); err != nil {
		return err
	}
	count, err := q.PlatformCountOpenPaymentReviews(ctx)
	if err != nil {
		return err
	}
	states, err := q.PlatformOperationalStateCounts(ctx)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	openAlerts.Set(float64(count))
	workStates.Reset()
	for _, row := range states {
		workStates.WithLabelValues(row.Kind, row.Status).Set(float64(row.Count))
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
