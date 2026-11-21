package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db/isolation"
)

// NewSQLitePool wraps an already-opened SQLite *sql.DB handle in a scoped
// *db.Pool so single-engine SQLite handlers and middleware (RequireTenantTx /
// RequirePlatformTx) share the same transactional contract without a PG role.
// The caller retains ownership of sqliteDB and closes it independently.
func NewSQLitePool(sqliteDB *sql.DB, scope isolation.Scope) (*Pool, error) {
	if sqliteDB == nil {
		return nil, errors.New("db: nil sqlite handle")
	}
	if _, err := pgRoleForScope(scope); err != nil {
		return nil, err
	}
	return &Pool{sqlite: sqliteDB, scope: scope}, nil
}

// IsSQLite reports whether this pool is backed by the single-engine SQLite store.
func (p *Pool) IsSQLite() bool {
	return p != nil && p.sqlite != nil
}

func (p *Pool) sqliteExec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	res, err := p.sqlite.ExecContext(ctx, query, sqlitePoolArgs(args)...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	n, _ := res.RowsAffected()
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", n)), nil
}

func (p *Pool) sqliteQuery(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := p.sqlite.QueryContext(ctx, query, sqlitePoolArgs(args)...)
	if err != nil {
		return nil, err
	}
	return &sqlitePoolRows{rows: rows}, nil
}

func (p *Pool) sqliteQueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return sqlitePoolRow{row: p.sqlite.QueryRowContext(ctx, query, sqlitePoolArgs(args)...)}
}

func (p *Pool) sqliteBeginTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.sqlite.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlitePoolTx{tx: tx}, nil
}

type sqlitePoolTx struct {
	pgx.Tx
	tx     *sql.Tx
	spSeq  atomic.Int64
	spName string
	done   bool
	parent *sqlitePoolTx
}

// IsSQLite identifies transaction handles backed by SQLite so middleware can
// rely on SQLite's BEGIN IMMEDIATE single-writer lock instead of PG advisory locks.
func (t *sqlitePoolTx) IsSQLite() bool { return true }

func (t *sqlitePoolTx) Begin(ctx context.Context) (pgx.Tx, error) {
	root := t
	if t.parent != nil {
		root = t.parent
	}
	name := fmt.Sprintf("urmail_sp_%d", root.spSeq.Add(1))
	if _, err := root.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return nil, err
	}
	return &sqlitePoolTx{tx: root.tx, spName: name, parent: root}, nil
}

func (t *sqlitePoolTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	res, err := t.tx.ExecContext(ctx, query, sqlitePoolArgs(args)...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	n, _ := res.RowsAffected()
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", n)), nil
}

func (t *sqlitePoolTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := t.tx.QueryContext(ctx, query, sqlitePoolArgs(args)...)
	if err != nil {
		return nil, err
	}
	return &sqlitePoolRows{rows: rows}, nil
}

func (t *sqlitePoolTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return sqlitePoolRow{row: t.tx.QueryRowContext(ctx, query, sqlitePoolArgs(args)...)}
}

func (t *sqlitePoolTx) Commit(ctx context.Context) error {
	if t.spName != "" {
		if t.done {
			return pgx.ErrTxClosed
		}
		t.done = true
		_, err := t.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+t.spName)
		return err
	}
	err := t.tx.Commit()
	if errors.Is(err, sql.ErrTxDone) {
		return pgx.ErrTxClosed
	}
	return err
}

func (t *sqlitePoolTx) Rollback(ctx context.Context) error {
	if t.spName != "" {
		if t.done {
			return pgx.ErrTxClosed
		}
		t.done = true
		if _, err := t.tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+t.spName); err != nil {
			return err
		}
		_, err := t.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+t.spName)
		return err
	}
	err := t.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return pgx.ErrTxClosed
	}
	return err
}

type sqlitePoolRows struct {
	pgx.Rows
	rows *sql.Rows
}

func (r *sqlitePoolRows) Close() { _ = r.rows.Close() }
func (r *sqlitePoolRows) Err() error {
	return r.rows.Err()
}
func (r *sqlitePoolRows) Next() bool { return r.rows.Next() }
func (r *sqlitePoolRows) Scan(dest ...any) error {
	return sqlitePoolScan(r.rows.Scan, dest...)
}

type sqlitePoolRow struct{ row *sql.Row }

func (r sqlitePoolRow) Scan(dest ...any) error {
	err := sqlitePoolScan(r.row.Scan, dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return pgx.ErrNoRows
	}
	return err
}

func sqlitePoolArgs(args []any) []any {
	if len(args) == 0 {
		return args
	}
	out := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case pgtype.Int8:
			if v.Valid {
				out[i] = v.Int64
			}
		case pgtype.Text:
			if v.Valid {
				out[i] = v.String
			}
		case pgtype.Bool:
			if v.Valid {
				out[i] = v.Bool
			}
		case *netip.Addr:
			if v != nil && v.IsValid() {
				out[i] = v.String()
			}
		case netip.Addr:
			if v.IsValid() {
				out[i] = v.String()
			}
		default:
			out[i] = a
		}
	}
	return out
}

func sqlitePoolScan(scanFn func(...any) error, dest ...any) error {
	proxies := make([]any, len(dest))
	var fixups []func() error
	for i, d := range dest {
		switch target := d.(type) {
		case **netip.Addr:
			var ns sql.NullString
			proxies[i] = &ns
			fixups = append(fixups, func() error {
				if !ns.Valid || ns.String == "" {
					*target = nil
					return nil
				}
				addr, err := netip.ParseAddr(ns.String)
				if err != nil {
					return err
				}
				*target = &addr
				return nil
			})
		case *pgtype.Int8:
			var ni sql.NullInt64
			proxies[i] = &ni
			fixups = append(fixups, func() error {
				*target = pgtype.Int8{Int64: ni.Int64, Valid: ni.Valid}
				return nil
			})
		case *pgtype.Text:
			var ns sql.NullString
			proxies[i] = &ns
			fixups = append(fixups, func() error {
				*target = pgtype.Text{String: ns.String, Valid: ns.Valid}
				return nil
			})
		case *pgtype.Bool:
			var nb sql.NullBool
			proxies[i] = &nb
			fixups = append(fixups, func() error {
				*target = pgtype.Bool{Bool: nb.Bool, Valid: nb.Valid}
				return nil
			})
		default:
			proxies[i] = d
		}
	}
	if err := scanFn(proxies...); err != nil {
		return err
	}
	for _, fn := range fixups {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}
