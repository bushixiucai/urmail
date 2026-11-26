package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
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
		case []string:
			// SQLite stores text[] columns as a JSON array. A raw Go slice
			// would be written as a Go literal, which cannot scan back.
			encoded, err := json.Marshal(v)
			if err != nil {
				encoded = []byte("[]")
			}
			out[i] = string(encoded)
		case []int64:
			// SQLite stores bigint[] columns (mailbox_import_items.baseline_uids)
			// as a JSON array.
			encoded, err := json.Marshal(v)
			if err != nil {
				encoded = []byte("[]")
			}
			out[i] = string(encoded)
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
		case *[]string:
			// modernc.org/sqlite has no PostgreSQL array codec. text[] columns
			// (approved_endpoints, scopes, policy_mx, sender_logins) are TEXT
			// holding a JSON array, or a leftover PostgreSQL '{a,b}' literal.
			var ns sql.NullString
			proxies[i] = &ns
			fixups = append(fixups, func() error {
				decoded, err := decodeSQLiteStringSlice(ns)
				if err != nil {
					return err
				}
				*target = decoded
				return nil
			})
		case *[]int64:
			// bigint[] columns (mailbox_import_items.baseline_uids) are TEXT
			// holding a JSON array, or a leftover PostgreSQL '{1,2}' literal.
			var ns sql.NullString
			proxies[i] = &ns
			fixups = append(fixups, func() error {
				decoded, err := decodeSQLiteInt64Slice(ns)
				if err != nil {
					return err
				}
				*target = decoded
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

// decodeSQLiteStringSlice reads one SQLite text[] cell into []string.
// NULL and empty text are an empty slice. A JSON array is the native encoding.
// A PostgreSQL text-array literal is accepted so a row imported before the JSON
// rewrite still scans. Anything else fails closed.
func decodeSQLiteStringSlice(ns sql.NullString) ([]string, error) {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" || strings.EqualFold(strings.TrimSpace(ns.String), "NULL") {
		return []string{}, nil
	}
	raw := strings.TrimSpace(ns.String)
	if strings.HasPrefix(raw, "[") {
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("sqlite text array: %w", err)
		}
		if out == nil {
			return []string{}, nil
		}
		return out, nil
	}
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		return splitPGTextArray(raw), nil
	}
	return nil, fmt.Errorf("sqlite text array: unsupported encoding %q", raw)
}

func splitPGTextArray(raw string) []string {
	inner := raw[1 : len(raw)-1]
	if strings.TrimSpace(inner) == "" {
		return []string{}
	}
	var items []string
	var cur strings.Builder
	inQuotes := false
	for i := 0; i < len(inner); i++ {
		ch := inner[i]
		if inQuotes {
			cur.WriteByte(ch)
			if ch == '"' && inner[i-1] != '\\' {
				inQuotes = false
			}
			continue
		}
		switch ch {
		case '"':
			inQuotes = true
			cur.WriteByte(ch)
		case ',':
			items = append(items, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	items = append(items, cur.String())
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if len(item) >= 2 && item[0] == '"' && item[len(item)-1] == '"' {
			item = item[1 : len(item)-1]
			item = strings.ReplaceAll(item, "\\\"", "\"")
			item = strings.ReplaceAll(item, "\\\\", "\\")
		}
		out = append(out, item)
	}
	return out
}

// decodeSQLiteInt64Slice reads one SQLite bigint[] cell into []int64.
// NULL and empty text are an empty slice. JSON array is the native encoding;
// a PostgreSQL '{1,2}' literal is also accepted. Anything else fails closed.
func decodeSQLiteInt64Slice(ns sql.NullString) ([]int64, error) {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" || strings.EqualFold(strings.TrimSpace(ns.String), "NULL") {
		return []int64{}, nil
	}
	raw := strings.TrimSpace(ns.String)
	if strings.HasPrefix(raw, "[") {
		var out []int64
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			// The importer writes every PostgreSQL array as JSON strings,
			// including bigint[]. Decimal strings in that cell still scan.
			var text []string
			if jerr := json.Unmarshal([]byte(raw), &text); jerr != nil {
				return nil, fmt.Errorf("sqlite bigint array: %w", err)
			}
			out = make([]int64, 0, len(text))
			for _, item := range text {
				v, perr := strconv.ParseInt(item, 10, 64)
				if perr != nil {
					return nil, fmt.Errorf("sqlite bigint array item %q: %w", item, perr)
				}
				out = append(out, v)
			}
		}
		if out == nil {
			return []int64{}, nil
		}
		return out, nil
	}
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		parts := splitPGTextArray(raw)
		out := make([]int64, 0, len(parts))
		for _, p := range parts {
			v, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("sqlite bigint array item %q: %w", p, err)
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("sqlite bigint array: unsupported encoding %q", raw)
}
