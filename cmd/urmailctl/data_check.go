package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func runDataCheck(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	return writeDataCheck(ctx, pool, os.Stdout)
}

func writeDataCheck(ctx context.Context, pool *db.Pool, output io.Writer) error {
	// Count, bounded IDs and existing diagnostics describe one read-only snapshot.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	rows, err := q.PlatformDataIssues(ctx)
	if err != nil {
		return err
	}
	billingIssues, err := q.PlatformBillingLegacyIssues(ctx)
	if err != nil {
		return err
	}
	calendarCount, err := q.PlatformCountBillingCalendarReviewTenants(ctx)
	if err != nil {
		return err
	}
	calendarIDs, err := q.PlatformListBillingCalendarReviewTenants(ctx)
	if err != nil {
		return err
	}
	billingReport := make([]map[string]any, 0, len(billingIssues))
	for _, issue := range billingIssues {
		billingReport = append(billingReport, map[string]any{"issue": issue.Issue, "tenant_id": issue.TenantID, "row_id": issue.RowID, "detail": json.RawMessage(issue.Detail)})
	}
	if err = json.NewEncoder(output).Encode(map[string]any{"reference_and_allocation_issues": rows, "legacy_billing_issues": billingReport, "billing_calendar_review_tenant_count": calendarCount, "billing_calendar_review_tenant_ids": calendarIDs, "billing_calendar_review_ids_truncated": calendarCount > int64(len(calendarIDs)), "changed": false}); err != nil {
		return err
	}
	if len(rows)+len(billingIssues) > 0 || calendarCount > 0 {
		return fmt.Errorf("%d reference/allocation and legacy billing issues; %d tenants require explicit billing calendar review (first %d IDs shown); no data changed", len(rows)+len(billingIssues), calendarCount, len(calendarIDs))
	}
	return nil
}
