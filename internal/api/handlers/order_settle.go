package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/distribution"
	"github.com/bushixiucai/urmail/internal/store"
)

// ErrOrderNotFound means an order does not exist or cannot be settled.
var ErrOrderNotFound = errors.New("order not found")

// SettleOrder marks a pending order paid, replaces the tenant subscription
// when the order carries a plan, and issues a paid invoice. It is shared by
// the platform console and the Stripe webhook.
func SettleOrder(ctx context.Context, pool *db.Pool, id int64) error {
	if pool == nil {
		return errors.New("database pool is not configured")
	}
	var tenantID int64
	if err := pool.QueryRow(ctx, "SELECT tenant_id FROM orders WHERE id=$1", id).Scan(&tenantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		return err
	}
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", tenantID); err != nil {
		return err
	}
	var amount int64
	var channelID pgtype.Int8
	var status string
	var planCode, currency string
	err = tx.QueryRow(ctx, `
		SELECT amount_cents, COALESCE(plan_code, ''), currency,status,channel_id
		FROM orders WHERE id = $1 AND tenant_id=$2 FOR UPDATE`, id, tenantID).
		Scan(&amount, &planCode, &currency, &status, &channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOrderNotFound
		}
		return err
	}
	if status == "paid" {
		return tx.Commit(ctx)
	}
	if status != "pending" {
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
	if channelID.Valid {
		queries := store.New(tx)
		bps, err := queries.PlatformCommissionRate(ctx, channelID.Int64)
		if err != nil {
			return err
		}
		commission, err := distribution.CalculateCommission(amount, bps)
		if err != nil {
			return err
		}
		if _, err = queries.CreateCommission(ctx, store.CreateCommissionParams{ChannelID: channelID.Int64, TenantID: tenantID, OrderID: id, AmountCents: commission, Bps: bps, Status: "pending"}); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}
