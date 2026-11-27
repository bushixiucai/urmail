package isolation

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/store"
)

type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type scopedDBTX struct {
	inner store.DBTX
}

type errRow struct {
	err error
}

func (r errRow) Scan(...any) error {
	return r.err
}

func sqlcMethodName(query string) (string, bool) {
	const prefix = "-- name: "
	if !strings.HasPrefix(query, prefix) {
		return "", false
	}
	rest := query[len(prefix):]
	idx := strings.IndexAny(rest, " \t\r\n")
	if idx <= 0 {
		return "", false
	}
	return rest[:idx], true
}

func checkSQLContext(ctx context.Context, query string) error {
	if method, ok := sqlcMethodName(query); ok {
		return CheckContext(ctx, method)
	}
	sc, has := ScopeFromContext(ctx)
	if !has {
		return fmt.Errorf("%w: missing scope context", ErrMissingScope)
	}
	if sc.Scope == ScopeTenant && sc.TenantID <= 0 {
		return fmt.Errorf("%w: %w: missing positive tenant_id", ErrMissingTenantScope, ErrMissingScope)
	}
	return nil
}

func (s *scopedDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := checkSQLContext(ctx, sql); err != nil {
		return pgconn.CommandTag{}, err
	}
	return s.inner.Exec(ctx, sql, args...)
}

func (s *scopedDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := checkSQLContext(ctx, sql); err != nil {
		return nil, err
	}
	return s.inner.Query(ctx, sql, args...)
}

func (s *scopedDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := checkSQLContext(ctx, sql); err != nil {
		return errRow{err: err}
	}
	return s.inner.QueryRow(ctx, sql, args...)
}

// Begin preserves transaction initiation when the underlying DBTX supports it
// (such as pgxpool/SQLite txBeginner in internal/store/sqlite_methods.go).
func (s *scopedDBTX) Begin(ctx context.Context) (pgx.Tx, error) {
	b, ok := s.inner.(txBeginner)
	if !ok {
		return nil, fmt.Errorf("db isolation: underlying DBTX does not support Begin")
	}
	return b.Begin(ctx)
}

// IsSQLite preserves dialect selection through the isolation wrapper so
// handwritten SQLite methods keep their native SQL implementation.
func (s *scopedDBTX) IsSQLite() bool {
	marker, ok := s.inner.(interface{ IsSQLite() bool })
	return ok && marker.IsSQLite()
}

// WrapDBTX wraps tx in a scope-enforcing DBTX without instantiating a raw
// store.Queries outside db.Queries.
func WrapDBTX(tx store.DBTX) store.DBTX {
	if tx == nil {
		panic("isolation.WrapDBTX: nil DBTX")
	}
	if already, ok := tx.(*scopedDBTX); ok {
		return already
	}
	return &scopedDBTX{inner: tx}
}

// DBTXScopeHook returns a Hook for db.SetIsolationHook that enforces context
// scope checks on every *store.Queries and (*store.Queries).WithTx call.
func DBTXScopeHook() Hook {
	return func(tx store.DBTX, q *store.Queries) *store.Queries {
		if tx == nil {
			panic("isolation.DBTXScopeHook: nil DBTX")
		}
		if q == nil {
			panic("isolation.DBTXScopeHook: nil *store.Queries")
		}
		return q.WrapDB(WrapDBTX)
	}
}
