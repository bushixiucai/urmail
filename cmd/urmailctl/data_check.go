package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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
	rows, err := store.New(pool.Pool).PlatformDataIssues(ctx)
	if err != nil {
		return err
	}
	billingIssues, err := store.New(pool.Pool).PlatformBillingLegacyIssues(ctx)
	if err != nil {
		return err
	}
	billingReport := make([]map[string]any, 0, len(billingIssues))
	for _, issue := range billingIssues {
		billingReport = append(billingReport, map[string]any{"issue": issue.Issue, "tenant_id": issue.TenantID, "row_id": issue.RowID, "detail": json.RawMessage(issue.Detail)})
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"reference_and_allocation_issues": rows, "legacy_billing_issues": billingReport, "changed": false}); err != nil {
		return err
	}
	if len(rows)+len(billingIssues) > 0 {
		return fmt.Errorf("%d data issues require explicit operator review; no data changed", len(rows)+len(billingIssues))
	}
	return nil
}
