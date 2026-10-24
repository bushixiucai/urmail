package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

type legacyPeriodRepair struct {
	TenantID, SubscriptionID, OrderID, NominalBasis int64
	ExpectedStart, ExpectedEnd                      time.Time
	Reason                                          string
}

func runBillingRepair(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("billing repair requires a root operator")
	}
	if len(args) == 0 || args[0] != "repair-legacy-period" {
		return errors.New("billing requires repair-legacy-period; data-check provides a read-only inventory")
	}
	fs := flag.NewFlagSet("billing repair-legacy-period", flag.ContinueOnError)
	var request legacyPeriodRepair
	fs.Int64Var(&request.TenantID, "tenant-id", 0, "canonical tenant ID")
	fs.Int64Var(&request.SubscriptionID, "subscription-id", 0, "exact observed subscription ID")
	fs.Int64Var(&request.OrderID, "paid-order-id", 0, "single historical paid order")
	fs.Int64Var(&request.NominalBasis, "nominal-basis-cents", 0, "operator-confirmed undiscounted historical cycle price")
	start := fs.String("expected-start", "", "observed incorrect period start in RFC3339")
	end := fs.String("expected-end", "", "observed incorrect period end in RFC3339")
	fs.StringVar(&request.Reason, "reason", "", "evidence and explicit correction justification (at least 20 characters)")
	apply := fs.Bool("apply", false, "commit the previewed correction and mandatory audit")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected billing repair arguments")
	}
	var err error
	request.ExpectedStart, err = time.Parse(time.RFC3339Nano, *start)
	if err != nil {
		return errors.New("valid --expected-start is required")
	}
	request.ExpectedEnd, err = time.Parse(time.RFC3339Nano, *end)
	if err != nil {
		return errors.New("valid --expected-end is required")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Quarantined {
		return errors.New("restore quarantine must be reconciled before granting repaired entitlements")
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	result, err := repairLegacyPeriod(ctx, pool, request, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func repairLegacyPeriod(ctx context.Context, pool *db.Pool, request legacyPeriodRepair, apply bool) (map[string]any, error) {
	if request.TenantID <= 0 || request.SubscriptionID <= 0 || request.OrderID <= 0 || request.NominalBasis <= 0 || request.NominalBasis > 9007199254740991 || len(strings.TrimSpace(request.Reason)) < 20 || len(request.Reason) > 2000 {
		return nil, errors.New("positive IDs, a confirmed nominal basis, and a bounded evidence reason are required")
	}
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, request.TenantID); err != nil {
		return nil, err
	}
	row, err := q.PlatformLegacyPeriodRepairCandidate(ctx, store.PlatformLegacyPeriodRepairCandidateParams{TenantID: request.TenantID, SubscriptionID: request.SubscriptionID, OrderID: request.OrderID, ExpectedStart: pgtype.Timestamptz{Time: request.ExpectedStart, Valid: true}, ExpectedEnd: pgtype.Timestamptz{Time: request.ExpectedEnd, Valid: true}})
	if err != nil {
		return nil, errors.New("legacy period is ambiguous, changed, already repaired, or lacks a single matching confirmed receipt; no rights changed")
	}
	if request.NominalBasis < row.AmountCents || !row.PaidAt.Valid || len(row.PlanSnapshot) == 0 || string(row.PlanSnapshot) == "{}" {
		return nil, errors.New("historical nominal basis or recognized plan snapshot requires operator investigation")
	}
	calendar, err := q.GetBillingCalendar(ctx, request.TenantID)
	if err != nil {
		return nil, errors.New("legacy period repair requires an audited platform calendar determination; no historical timezone is inferred")
	}
	if calendar.Source != "manual_legacy" || !calendar.CreatedBy.Valid || len(strings.TrimSpace(calendar.ReviewReason)) < 20 || calendar.Version != 1 || !calendar.FirstPaidAt.Valid || !calendar.FirstPaidAt.Time.Equal(row.PaidAt.Time) || !calendar.AnchorAt.Valid || !calendar.AnchorAt.Time.Equal(row.PaidAt.Time) {
		return nil, errors.New("calendar determination does not prove the unique original payment anchor; review continuity before repair")
	}
	var wall, firstWall billing.CalendarAnchor
	if len(calendar.AnchorWall) == 0 || len(calendar.FirstAnchorWall) == 0 || json.Unmarshal(calendar.AnchorWall, &wall) != nil || json.Unmarshal(calendar.FirstAnchorWall, &firstWall) != nil || wall != firstWall {
		return nil, errors.New("persisted original calendar wall clock is missing or inconsistent")
	}
	spec := billing.CalendarSpec{Wall: &wall, Timezone: calendar.Timezone, AnchorAt: calendar.AnchorAt.Time, Version: calendar.Version}
	anchor, err := spec.Anchor()
	if err != nil {
		return nil, fmt.Errorf("validate reviewed billing calendar: %w", err)
	}
	calendarSnapshot, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	correctedEnd, err := billing.AddAnchoredCycle(row.PaidAt.Time, row.BillingCycle, anchor)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"actor_type": "root_operator", "source": "legacy_period_repair", "reason": strings.TrimSpace(request.Reason), "subscription_id": row.SubscriptionID, "source_order_id": row.OrderID, "source_payment_id": row.PaymentID, "old_start": row.PeriodStart.Time, "old_end": row.PeriodEnd.Time, "corrected_start": row.PaidAt.Time, "corrected_end": correctedEnd, "nominal_basis_cents": request.NominalBasis, "snapshot_provenance": "observed_existing_snapshot_operator_confirmed", "historical_invoice_unchanged": true, "calendar_snapshot": spec, "status": 200, "outcome": "succeeded"}
	result := map[string]any{"changed": apply, "tenant_id": row.TenantID, "correction": detail, "already_expired": !correctedEnd.After(time.Now())}
	if !apply {
		return result, nil
	}
	start := row.PaidAt
	end := pgtype.Timestamptz{Time: correctedEnd, Valid: true}
	changed, err := q.PlatformRepairLegacySubscriptionPeriod(ctx, store.PlatformRepairLegacySubscriptionPeriodParams{TenantID: row.TenantID, SubscriptionID: row.SubscriptionID, ExpectedStart: row.PeriodStart, ExpectedEnd: row.PeriodEnd, CorrectedStart: start, CorrectedEnd: end})
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("subscription changed during repair")
	}
	if err = q.PlatformRecordSubscriptionTerm(ctx, store.PlatformRecordSubscriptionTermParams{TenantID: row.TenantID, OrderID: row.OrderID, PeriodStart: start, PeriodEnd: end, PlanSnapshot: row.PlanSnapshot, BaseAmountCents: request.NominalBasis, Currency: row.Currency, BillingCycle: row.BillingCycle, BillingPeriodStart: start, CalendarSnapshot: calendarSnapshot}); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return nil, err
	}
	if _, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: row.TenantID, Valid: true}, Action: "REPAIR subscription.legacy_period", TargetType: pgtype.Text{String: "subscription", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(row.SubscriptionID, 10), Valid: true}, Detail: raw}); err != nil {
		return nil, fmt.Errorf("mandatory repair audit: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
