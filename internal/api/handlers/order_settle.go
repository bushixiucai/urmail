package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
)

var ErrOrderNotFound = errors.New("order not found")

// SettleOrder marks a pending order paid, replaces the tenant subscription
// when the order carries a plan, and issues a paid invoice. It is shared by
// the platform console and the Stripe webhook.
func SettleOrder(ctx context.Context, pool *db.Pool, id int64) error {
	if pool == nil {
		return errors.New("database pool is not configured")
	}
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var tenantID, amount int64
	var planCode, currency string
	err = tx.QueryRow(ctx, `
		SELECT tenant_id, amount_cents, COALESCE(plan_code, ''), currency
		FROM orders WHERE id = $1 AND status = 'pending' FOR UPDATE`, id).
		Scan(&tenantID, &amount, &planCode, &currency)
	if err != nil {
		return ErrOrderNotFound
	}
	if _, err := tx.Exec(ctx, "UPDATE orders SET status = 'paid', paid_at = now() WHERE id = $1", id); err != nil {
		return err
	}
	if planCode != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE subscriptions SET status = 'canceled'
			WHERE tenant_id = $1 AND status IN ('active', 'trialing', 'past_due')`, tenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO subscriptions (tenant_id, plan_id, status, period_start, period_end)
			VALUES ($1, $2, 'active', now(), now() + interval '1 month')`, tenantID, planCode); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invoices (tenant_id, number, period_start, period_end, amount_cents, currency, status, issued_at)
		VALUES ($1, $2, now(), now() + interval '1 month', $3, $4, 'paid', now())`,
		tenantID, fmt.Sprintf("INV-%d-%d", tenantID, time.Now().UnixNano()), amount, currency); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
