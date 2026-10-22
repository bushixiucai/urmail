package db

import (
	"context"
	"fmt"

	_ "embed"

	"github.com/jackc/pgx/v5"
)

//go:embed bootstrap.sql
var bootstrapSQL string

// Bootstrap creates the cluster-level roles and grants that migrations assume.
// It must run as a PostgreSQL superuser (or a role with CREATEROLE/BYPASSRLS)
// and is intentionally separate from the versioned schema migrations.
func Bootstrap(ctx context.Context, connString string) error {
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return fmt.Errorf("connect for bootstrap: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, bootstrapSQL); err != nil {
		return fmt.Errorf("apply bootstrap SQL: %w", err)
	}
	return nil
}
