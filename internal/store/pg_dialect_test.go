package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type dialectProbe struct {
	query string
	args  []any
}

func (p *dialectProbe) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	p.query, p.args = query, append([]any(nil), args...)
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (*dialectProbe) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}
func (*dialectProbe) QueryRow(context.Context, string, ...any) pgx.Row {
	return errorProbeRow{err: errors.New("unexpected QueryRow")}
}

type sqliteDialectProbe struct{ dialectProbe }

func (*sqliteDialectProbe) IsSQLite() bool { return true }

type errorProbeRow struct{ err error }

func (r errorProbeRow) Scan(...any) error { return r.err }

func TestPostgresCompatibleDBTXSelectsNativeQueryAndTimestampArgs(t *testing.T) {
	ctx := context.Background()
	probe := &dialectProbe{}
	db := PostgresCompatibleDBTX(probe)
	query := "-- name: GetUserRoleForTenant :one\nSELECT role WHERE user_id=?1 AND tenant_id=?2"
	if _, err := db.Exec(ctx, query, int64(7), int64(9)); err != nil {
		t.Fatal(err)
	}
	if probe.query != "-- name: GetUserRoleForTenant :one\nSELECT r.role\nFROM user_tenant_roles r\nJOIN users u ON u.id=r.user_id AND u.status='active'\nWHERE r.user_id = $1 AND r.tenant_id = $2\n" {
		t.Fatalf("PG query = %q", probe.query)
	}

	micros := time.Date(2026, time.November, 21, 12, 0, 0, 123000, time.UTC).UnixMicro()
	if _, err := db.Exec(ctx, "-- name: CreateSession :one\nINSERT INTO sessions VALUES(?1)", "id", int64(7), "csrf", nil, nil, micros, "host", int64(1), "hash", int64(1)); err != nil {
		t.Fatal(err)
	}
	got, ok := probe.args[5].(time.Time)
	if !ok || !got.Equal(time.UnixMicro(micros).UTC()) {
		t.Fatalf("PG timestamp arg = %#v, want time.Time for %d micros", probe.args[5], micros)
	}
}

func TestPostgresCompatibleDBTXFailsClosedOnUnlabelledSQLiteSQL(t *testing.T) {
	probe := &dialectProbe{}
	db := PostgresCompatibleDBTX(probe)
	_, err := db.Exec(context.Background(), "UPDATE sessions SET expires_at=?1 WHERE id=?2", int64(1), "s")
	if !errors.Is(err, ErrPostgreSQLQueryUnavailable) {
		t.Fatalf("unlabelled SQLite SQL error = %v, want ErrPostgreSQLQueryUnavailable", err)
	}
}

func TestPostgresCompatibleDBTXLeavesSQLiteQueriesNative(t *testing.T) {
	probe := &sqliteDialectProbe{}
	db := PostgresCompatibleDBTX(probe)
	query := "-- name: GetUserRoleForTenant :one\nSELECT role WHERE user_id=?1 AND tenant_id=?2"
	if _, err := db.Exec(context.Background(), query, int64(7), int64(9)); err != nil {
		t.Fatal(err)
	}
	if probe.query != query {
		t.Fatalf("SQLite query changed: got %q want %q", probe.query, query)
	}
}
