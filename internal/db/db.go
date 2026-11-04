// Package db owns the PostgreSQL connections used by urMail.cn.
//
// Two roles are used:
//
//   - urmail_app       tenant-scoped; subject to row-level security
//   - urmail_platform  platform-scoped; bypasses row-level security
//
// Every tenant-scoped transaction must carry a tenant id, which is published
// to PostgreSQL as the `urmail.tenant_id` setting that the RLS policies read.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Role names used by the RLS policies.
const (
	// RoleApp is the tenant-scoped role.
	RoleApp = "urmail_app"
	// RolePlatform is the platform-scoped role.
	RolePlatform = "urmail_platform"
)

// tenantSetting is the PostgreSQL setting that carries the current tenant id.
const tenantSetting = "urmail.tenant_id"

// userSetting is the PostgreSQL setting that carries the current user id.
const userSetting = "urmail.user_id"

// mailboxSetting is the PostgreSQL setting that carries the current mailbox id.
// Collaboration policies require it together with the tenant id and fail closed
// when either value is absent.
const mailboxSetting = "urmail.mailbox_id"

// ErrNoTenant is returned when a tenant-scoped operation is attempted
// without a tenant context.
var ErrNoTenant = errors.New("no tenant in context")

// ErrNoMailbox is returned when a mailbox-scoped operation is attempted
// without a mailbox context.
var ErrNoMailbox = errors.New("no mailbox in context")

// Pool is a connection pool bound to one database role.
type Pool struct {
	*pgxpool.Pool
	role      string
	admission *admission
}

// Open creates a pool for the given connection string and pins every
// connection to the given role.
func Open(ctx context.Context, connString, role string) (*Pool, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	config.ConnConfig.RuntimeParams["role"] = role
	config.ConnConfig.RuntimeParams["application_name"] = "urmaild"

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}

	pooled := &Pool{Pool: pool, role: role}
	if err := pooled.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pooled, nil
}

// OpenApp opens the tenant-scoped pool.
func OpenApp(ctx context.Context, connString string) (*Pool, error) {
	return Open(ctx, connString, RoleApp)
}

// OpenPlatform opens the platform-scoped pool.
func OpenPlatform(ctx context.Context, connString string) (*Pool, error) {
	return Open(ctx, connString, RolePlatform)
}

// Role returns the PostgreSQL role this pool is pinned to.
func (p *Pool) Role() string { return p.role }

// TenantTx starts a transaction scoped to a tenant. The tenant id is set
// with SET LOCAL so it is discarded when the transaction ends, which keeps a
// pooled connection from leaking one tenant's context into the next request.
func (p *Pool) TenantTx(ctx context.Context, tenantID int64) (pgx.Tx, error) {
	return p.scopedTx(ctx, tenantID, 0, 0)
}

// UserTx starts a transaction scoped to a tenant and a user. Both settings
// are published so that the row-level security policies can distinguish
// "the tenant" from "the person making the request".
func (p *Pool) UserTx(ctx context.Context, tenantID, userID int64) (pgx.Tx, error) {
	return p.scopedTx(ctx, tenantID, userID, 0)
}

// MailboxTx starts a transaction scoped to one mailbox inside one tenant.
// Collaboration tables fail closed unless both settings are present, so a
// panel user context cannot read contacts or calendars by tenant id alone.
func (p *Pool) MailboxTx(ctx context.Context, tenantID, mailboxID int64) (pgx.Tx, error) {
	if mailboxID <= 0 {
		return nil, ErrNoMailbox
	}
	return p.scopedTx(ctx, tenantID, 0, mailboxID)
}

// scopedTx begins a transaction and publishes the request scope.
func (p *Pool) scopedTx(ctx context.Context, tenantID, userID, mailboxID int64) (pgx.Tx, error) {
	if tenantID <= 0 {
		return nil, ErrNoTenant
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tenant transaction: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", tenantSetting, fmt.Sprintf("%d", tenantID)); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("set tenant context: %w", err)
	}
	if userID > 0 {
		if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", userSetting, fmt.Sprintf("%d", userID)); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("set user context: %w", err)
		}
	}
	if mailboxID > 0 {
		if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", mailboxSetting, fmt.Sprintf("%d", mailboxID)); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("set mailbox context: %w", err)
		}
	}
	return tx, nil
}

// PlatformTx starts a transaction without a tenant scope.
func (p *Pool) PlatformTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	return tx, nil
}

// CurrentTenant returns the tenant id visible to the current transaction.
// It returns 0 when no tenant is set.
func CurrentTenant(ctx context.Context, tx pgx.Tx) (int64, error) {
	var tenantID int64
	err := tx.QueryRow(ctx, "SELECT coalesce(nullif(current_setting($1, true), '')::bigint, 0)", tenantSetting).Scan(&tenantID)
	if err != nil {
		return 0, fmt.Errorf("read tenant context: %w", err)
	}
	return tenantID, nil
}

// CurrentUser returns the user id visible to the current transaction.
// It returns 0 when no user is set.
func CurrentUser(ctx context.Context, tx pgx.Tx) (int64, error) {
	var userID int64
	err := tx.QueryRow(ctx, "SELECT coalesce(nullif(current_setting($1, true), '')::bigint, 0)", userSetting).Scan(&userID)
	if err != nil {
		return 0, fmt.Errorf("read user context: %w", err)
	}
	return userID, nil
}
