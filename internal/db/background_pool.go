package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenBackgroundPlatform reserves a bounded pool for optional marketing work.
// Advisory-lock waiters cannot occupy the foreground authentication/mail pool.
// The database and role remain the same authoritative PostgreSQL instance.
func OpenBackgroundPlatform(ctx context.Context, connString string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse background database url: %w", err)
	}
	cfg.MaxConns = 2
	if cfg.MinConns > cfg.MaxConns {
		cfg.MinConns = cfg.MaxConns
	}
	cfg.ConnConfig.RuntimeParams["role"] = RolePlatform
	cfg.ConnConfig.RuntimeParams["application_name"] = "urmaild-marketing"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open background database pool: %w", err)
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping background database: %w", err)
	}
	return &Pool{Pool: pool, role: RolePlatform}, nil
}
