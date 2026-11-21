package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	pgstore "github.com/bushixiucai/urmail/internal/store/generated/postgres"
)

var ErrPostgreSQLQueryUnavailable = errors.New("store: PostgreSQL query variant unavailable")

var sqliteOnlySQL = regexp.MustCompile(`(?i)(\?[0-9]+|\bstrftime\s*\(|\bjson_each\s*\(|\bjson_object\s*\()`)

// Parameter positions whose PostgreSQL generated signature takes a native
// timestamptz while the SQLite generated signature stores unix microseconds.
// This explicit bridge is B2 adapter metadata, not a SQL text rewrite.
var postgresTimestampArgIndexes = map[string][]int{
	"CreateSession": {5},
}

func adaptPostgresArgs(method string, args []any) []any {
	indexes := postgresTimestampArgIndexes[method]
	if len(indexes) == 0 {
		return args
	}
	out := append([]any(nil), args...)
	for _, index := range indexes {
		if index >= len(out) {
			continue
		}
		if micros, ok := out[index].(int64); ok {
			out[index] = time.UnixMicro(micros).UTC()
		}
	}
	return out
}

type postgresDialectMarker interface{ isPostgresDialect() }
type sqliteDialectMarker interface{ IsSQLite() bool }

// PostgresCompatibleDBTX selects the frozen PostgreSQL sqlc statement for
// named methods while leaving SQLite DBTX handles on their native generated SQL.
// It never rewrites placeholders or translates SQL text.
func PostgresCompatibleDBTX(tx DBTX) DBTX {
	if tx == nil {
		panic("store.PostgresCompatibleDBTX: nil DBTX")
	}
	if marker, ok := tx.(sqliteDialectMarker); ok && marker.IsSQLite() {
		return tx
	}
	if _, ok := tx.(postgresDialectMarker); ok {
		return tx
	}
	return &pgDialectDBTX{inner: tx}
}

func isSQLiteDBTX(tx DBTX) bool {
	marker, ok := tx.(sqliteDialectMarker)
	return ok && marker.IsSQLite()
}

type pgDialectDBTX struct{ inner DBTX }

func (*pgDialectDBTX) isPostgresDialect() {}

func (p *pgDialectDBTX) statement(query string) (string, error) {
	method, named := sqlcQueryName(query)
	if !named {
		if sqliteOnlySQL.MatchString(query) {
			return "", fmt.Errorf("%w: unlabelled SQLite-only SQL refused on PostgreSQL", ErrPostgreSQLQueryUnavailable)
		}
		return query, nil
	}
	statement, ok := pgstore.QuerySQL(method)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrPostgreSQLQueryUnavailable, method)
	}
	return statement, nil
}

func sqlcQueryName(query string) (string, bool) {
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

func (p *pgDialectDBTX) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	statement, err := p.statement(query)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	method, _ := sqlcQueryName(query)
	return p.inner.Exec(ctx, statement, adaptPostgresArgs(method, args)...)
}

func (p *pgDialectDBTX) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	statement, err := p.statement(query)
	if err != nil {
		return nil, err
	}
	method, _ := sqlcQueryName(query)
	rows, err := p.inner.Query(ctx, statement, adaptPostgresArgs(method, args)...)
	if err != nil {
		return nil, err
	}
	return pgDialectRows{Rows: rows}, nil
}

func (p *pgDialectDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	statement, err := p.statement(query)
	if err != nil {
		return pgErrorRow{err: err}
	}
	method, _ := sqlcQueryName(query)
	rows, err := p.inner.Query(ctx, statement, adaptPostgresArgs(method, args)...)
	if err != nil {
		return pgErrorRow{err: err}
	}
	return pgDialectRow{rows: pgDialectRows{Rows: rows}}
}

// Begin preserves pgx transaction ownership while retaining the dialect adapter.
func (p *pgDialectDBTX) Begin(ctx context.Context) (pgx.Tx, error) {
	beginner, ok := p.inner.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return nil, errors.New("store: underlying PostgreSQL DBTX does not support Begin")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgDialectTx{Tx: tx, dbtx: &pgDialectDBTX{inner: tx}}, nil
}

type pgDialectTx struct {
	pgx.Tx
	dbtx *pgDialectDBTX
}

func (*pgDialectTx) isPostgresDialect() {}

func (t *pgDialectTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return t.dbtx.Exec(ctx, query, args...)
}
func (t *pgDialectTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return t.dbtx.Query(ctx, query, args...)
}
func (t *pgDialectTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return t.dbtx.QueryRow(ctx, query, args...)
}
func (t *pgDialectTx) Begin(ctx context.Context) (pgx.Tx, error) {
	return t.dbtx.Begin(ctx)
}

// pgDialectRows converts PostgreSQL timestamps to the SQLite-model unix-micro
// representation only where the generated consumer asks for int64.
type pgDialectRows struct{ pgx.Rows }

func (r pgDialectRows) Scan(dest ...any) error {
	fields := r.FieldDescriptions()
	fixups := make([]func(), 0)
	for i, field := range fields {
		if i >= len(dest) {
			break
		}
		target, ok := dest[i].(*int64)
		if !ok {
			continue
		}
		switch field.DataTypeOID {
		case 1184:
			var value pgtype.Timestamptz
			dest[i] = &value
			fixups = append(fixups, func() {
				if value.Valid {
					*target = value.Time.UnixMicro()
				} else {
					*target = 0
				}
			})
		case 1114:
			var value pgtype.Timestamp
			dest[i] = &value
			fixups = append(fixups, func() {
				if value.Valid {
					*target = value.Time.UnixMicro()
				} else {
					*target = 0
				}
			})
		}
	}
	if err := r.Rows.Scan(dest...); err != nil {
		return err
	}
	for _, fixup := range fixups {
		fixup()
	}
	return nil
}

type pgDialectRow struct{ rows pgDialectRows }
type pgErrorRow struct{ err error }

func (r pgErrorRow) Scan(...any) error { return r.err }
func (r pgDialectRow) Scan(dest ...any) error {
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	return r.rows.Scan(dest...)
}
