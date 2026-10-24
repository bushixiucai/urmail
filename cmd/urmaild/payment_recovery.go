package main

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
)

// startPaymentRecovery classifies expired side-effect leases as uncertain.
func startPaymentRecovery(ctx context.Context, cfg *config.Config, pool *db.Pool, box handlers.SecretsBox, baseURL string, legacyStripe *payment.Stripe, logger *zap.Logger) {
	if cfg.DevMode || cfg.Quarantined {
		return
	}
	gateway := &handlers.PaymentGatewayHandler{LegacyStripe: legacyStripe, Pool: pool, Platform: store.New(pool), Secrets: box, Registry: &payment.Registry{Store: store.New(pool), Secrets: box, BaseURL: baseURL}}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if err := gateway.Recover(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("payment recovery requires attention")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
