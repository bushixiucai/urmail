package datamigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/jackc/pgx/v5"
)

// NegativeIsolation opens the migrated SQLite target through the production
// scoped-querier assembly and runs the §2.3 negative case set against
// migrated rows: a tenant context may read its own rows and must receive
// cross-tenant refusal (ErrCrossTenantAccess) and missing-scope refusal
// (ErrMissingScope). This proves the migrated store still enforces the
// isolation contract (§4 gate five).
func NegativeIsolation(ctx context.Context, targetPath string, tenantA, tenantB int64, settingsKey string) error {
	raw, err := sql.Open("sqlite", targetPath)
	if err != nil {
		return fmt.Errorf("open migrated target: %w", err)
	}
	defer raw.Close()
	pool, err := db.NewSQLitePool(raw, isolation.ScopeTenant)
	if err != nil {
		return fmt.Errorf("tenant pool: %w", err)
	}
	tx, err := pool.UserTx(ctx, tenantA, 1)
	if err != nil {
		return fmt.Errorf("tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.Querier(tx)
	tenantACtx := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: tenantA, ActorID: 1})

	// 1. Own-tenant read works over migrated rows (the key may legitimately
	// be absent; any non-NoRows error is a failure).
	if _, err := q.GetTenantSetting(tenantACtx,
		store.GetTenantSettingParams{ScopeID: tenantA, Key: settingsKey}); err != nil && !errors.Is(err, pgxNoRows()) {
		return fmt.Errorf("own-tenant read failed: %w", err)
	}

	// 2. Cross-tenant parameter under tenant A context: refused.
	if _, err := q.GetTenantSetting(tenantACtx,
		store.GetTenantSettingParams{ScopeID: tenantB, Key: settingsKey}); !errors.Is(err, isolation.ErrCrossTenantAccess) {
		return fmt.Errorf("cross-tenant setting read err=%v, want ErrCrossTenantAccess", err)
	}

	// 3. Missing scope context: refused.
	if _, err := q.GetTenantSetting(ctx,
		store.GetTenantSettingParams{ScopeID: tenantA, Key: settingsKey}); !errors.Is(err, isolation.ErrMissingScope) {
		return fmt.Errorf("missing-scope setting read err=%v, want ErrMissingScope", err)
	}
	return nil
}

func pgxNoRows() error {
	return pgx.ErrNoRows
}
