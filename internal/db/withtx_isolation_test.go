package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	"github.com/bushixiucai/urmail/internal/store"
)

type spyDBTX struct {
	execs     int
	queries   int
	queryRows int
	begins    int
}

func (s *spyDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	s.execs++
	return pgconn.NewCommandTag("EXEC 1"), nil
}

func (s *spyDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	s.queries++
	return spyEmptyRows{}, nil
}

func (s *spyDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	s.queryRows++
	return spyErrRow{err: sql.ErrNoRows}
}

func (s *spyDBTX) Begin(context.Context) (pgx.Tx, error) {
	s.begins++
	return &spyTx{parent: s}, nil
}

func (s *spyDBTX) totalCalls() int {
	return s.execs + s.queries + s.queryRows
}

type spyTx struct {
	pgx.Tx
	parent *spyDBTX
}

func (t *spyTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.parent.Exec(ctx, sql, args...)
}

func (t *spyTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.parent.Query(ctx, sql, args...)
}

func (t *spyTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.parent.QueryRow(ctx, sql, args...)
}

func (t *spyTx) Commit(context.Context) error   { return nil }
func (t *spyTx) Rollback(context.Context) error { return nil }

type spyEmptyRows struct{ pgx.Rows }

func (spyEmptyRows) Close()        {}
func (spyEmptyRows) Err() error    { return nil }
func (spyEmptyRows) Next() bool    { return false }
func (spyEmptyRows) Scan(...any) error { return nil }

type spyErrRow struct{ err error }

func (r spyErrRow) Scan(...any) error { return r.err }

func TestQueriesWithTxPreservesIsolationBoundary(t *testing.T) {
	SetIsolationHook(isolation.DBTXScopeHook())
	t.Cleanup(func() { SetIsolationHook(isolation.DBTXScopeHook()) })

	spy := &spyDBTX{}
	tx := &spyTx{parent: spy}

	// Nil DBTX and nil pgx.Tx must both panic immediately.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected Queries(nil) to panic")
			}
		}()
		Queries(nil)
	}()

	q := Queries(spy)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected q.WithTx(nil) to panic")
			}
		}()
		q.WithTx(nil)
	}()

	tenantCtx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: 7, ActorID: 3})
	platformCtx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopePlatform, ActorID: 1})
	systemCtx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeSystem})

	// Chained WithTx retains the boundary hook from db.Queries.
	qtx := q.WithTx(tx).WithTx(tx)

	if _, err := qtx.GetMailboxByID(context.Background(), store.GetMailboxByIDParams{TenantID: 7, ID: 1}); !errors.Is(err, isolation.ErrMissingTenantScope) {
		t.Fatalf("qtx tenant query without scope err=%v, want ErrMissingTenantScope", err)
	}
	if _, err := qtx.GetMailboxByID(platformCtx, store.GetMailboxByIDParams{TenantID: 7, ID: 1}); !errors.Is(err, isolation.ErrMissingTenantScope) {
		t.Fatalf("qtx tenant query under platform scope err=%v, want ErrMissingTenantScope", err)
	}
	if err := qtx.DeleteSession(tenantCtx, "sess-1"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("qtx DeleteSession under tenant scope err=%v, want ErrPlatformScopeRequired", err)
	}
	if err := qtx.MarkSessionMFAVerified(tenantCtx, "sess-1"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("qtx MarkSessionMFAVerified under tenant scope err=%v, want ErrPlatformScopeRequired", err)
	}
	if err := qtx.DeleteExpiredSessions(tenantCtx); !errors.Is(err, isolation.ErrSystemScopeRequired) {
		t.Fatalf("qtx DeleteExpiredSessions under tenant scope err=%v, want ErrSystemScopeRequired", err)
	}
	if spy.totalCalls() != 0 {
		t.Fatalf("rejected qtx calls hit underlying DBTX %d times", spy.totalCalls())
	}

	// Even a direct store.New(...).WithTx(tx) (used only inside tests or hypothetical bypass)
	// cannot escape because SetIsolationHook also installs store.SetTxBoundaryHook.
	directTx := store.New(spy).WithTx(tx)
	if err := directTx.DeleteSession(tenantCtx, "sess-1"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("store.New.WithTx bypassed SetTxBoundaryHook: err=%v", err)
	}
	if spy.totalCalls() != 0 {
		t.Fatalf("directTx rejection hit DBTX %d times", spy.totalCalls())
	}

	// Valid scopes reach the transaction DBTX.
	if err := qtx.DeleteSession(platformCtx, "sess-1"); err != nil {
		t.Fatalf("qtx DeleteSession under platform scope failed: %v", err)
	}
	if err := qtx.DeleteExpiredSessions(systemCtx); err != nil {
		t.Fatalf("qtx DeleteExpiredSessions under system scope failed: %v", err)
	}
	if _, err := qtx.GetMailboxByID(tenantCtx, store.GetMailboxByIDParams{TenantID: 7, ID: 1}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("qtx GetMailboxByID under tenant scope got unexpected err=%v", err)
	}
	if spy.totalCalls() != 3 {
		t.Fatalf("expected 3 valid SQL calls through qtx, got %d", spy.totalCalls())
	}
}

// sqliteDBTX adapts *sql.DB (modernc SQLite) to store.DBTX + Begin(ctx) (pgx.Tx, error).
type sqliteDBTX struct {
	db *sql.DB
}

func (s *sqliteDBTX) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	n, _ := res.RowsAffected()
	return pgconn.NewCommandTag(formatRowsTag(n)), nil
}

func (s *sqliteDBTX) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &sqliteRows{rows: rows}, nil
}

func (s *sqliteDBTX) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return sqliteRow{row: s.db.QueryRowContext(ctx, query, args...)}
}

func (s *sqliteDBTX) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqliteTx{tx: tx}, nil
}

type sqliteTx struct {
	pgx.Tx
	tx *sql.Tx
}

func (t *sqliteTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	res, err := t.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	n, _ := res.RowsAffected()
	return pgconn.NewCommandTag(formatRowsTag(n)), nil
}

func (t *sqliteTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &sqliteRows{rows: rows}, nil
}

func (t *sqliteTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return sqliteRow{row: t.tx.QueryRowContext(ctx, query, args...)}
}

func (t *sqliteTx) Commit(context.Context) error   { return t.tx.Commit() }
func (t *sqliteTx) Rollback(context.Context) error { return t.tx.Rollback() }

type sqliteRows struct {
	pgx.Rows
	rows *sql.Rows
}

func (r *sqliteRows) Close()             { _ = r.rows.Close() }
func (r *sqliteRows) Err() error         { return r.rows.Err() }
func (r *sqliteRows) Next() bool         { return r.rows.Next() }
func (r *sqliteRows) Scan(d ...any) error { return r.rows.Scan(d...) }

type sqliteRow struct{ row *sql.Row }

func (r sqliteRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return pgx.ErrNoRows
	}
	return err
}

func formatRowsTag(n int64) string {
	return fmt.Sprintf("UPDATE %d", n)
}

func openMigratedTestSQLite(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	sqliteDB, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "isolation.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqliteDB.Close() })
	repoRoot := filepath.Join("..", "..")
	if err := sqlitedb.Migrate(ctx, sqliteDB, os.DirFS(repoRoot), "migrations_sqlite"); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}

	stmts := []string{
		`INSERT INTO brands(id,slug,name) VALUES(1,'default','Default')`,
		`INSERT INTO users(id,email,password_hash,status) VALUES(101,'u10@example.test','x','active'),(201,'u20@example.test','x','active')`,
		`INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(10,1,'Ten10','ten10','active',101),(20,1,'Ten20','ten20','active',201)`,
		`INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES(100,10,'t10.test','active','tok10',1700000000000000,'pending'),(200,20,'t20.test','active','tok20',1700000000000000,'pending')`,
		`UPDATE domains SET provisioning_state='ready' WHERE id IN (100,200)`,
		`INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled) VALUES(1000,10,100,'alice','alice@t10.test','x',-1,'active',1),(2000,20,200,'bob','bob@t20.test','x',-1,'active',1)`,
		`INSERT INTO sessions(id,user_id,tenant_id,csrf_token,origin_host,expires_at) VALUES('sess-active',101,10,'csrf','t10.test',9999999999000000),('sess-expired',101,10,'csrf2','t10.test',1)`,
		`INSERT INTO compliance_grants(id,tenant_id,user_id,scope,reason,granted_by,owner_confirmed_at,expires_at) VALUES(501,10,101,'full_content','audit',201,1700000000000001,(CAST(strftime('%s','now') AS INTEGER)*1000000)+7200000000)`,
		`INSERT INTO user_tenant_roles(tenant_id,user_id,role) VALUES(10,101,'owner')`,
	}
	for _, stmt := range stmts {
		if _, err := sqliteDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	return sqliteDB
}

func TestSQLiteQuerierAndWithTxIsolationMatrix(t *testing.T) {
	sqliteDB := openMigratedTestSQLite(t)
	shim := &sqliteDBTX{db: sqliteDB}

	// Default db.Querier(tx) returns *isolation.ScopedQuerier with all S3 checks enabled.
	querier := Querier(shim)
	if _, ok := querier.(*isolation.ScopedQuerier); !ok {
		t.Fatalf("db.Querier returned %T, want *isolation.ScopedQuerier", querier)
	}

	tenant10 := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: 10, ActorID: 101})
	platformCtx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopePlatform, ActorID: 1})
	systemCtx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeSystem})

	// 1. Valid tenant 10 call reads own mailbox on SQLite.
	mb, err := querier.GetMailboxByID(tenant10, store.GetMailboxByIDParams{TenantID: 10, ID: 1000})
	if err != nil {
		t.Fatalf("tenant 10 GetMailboxByID failed: %v", err)
	}
	if mb.TenantID != 10 || mb.Address != "alice@t10.test" {
		t.Fatalf("unexpected mailbox row: %+v", mb)
	}

	// 2. Missing scope & cross-tenant parameter rejected before SQLite runs.
	if _, err := querier.GetMailboxByID(context.Background(), store.GetMailboxByIDParams{TenantID: 10, ID: 1000}); !errors.Is(err, isolation.ErrMissingTenantScope) {
		t.Fatalf("missing scope err=%v, want ErrMissingTenantScope", err)
	}
	if _, err := querier.GetMailboxByID(tenant10, store.GetMailboxByIDParams{TenantID: 20, ID: 2000}); !errors.Is(err, isolation.ErrCrossTenantAccess) {
		t.Fatalf("cross-tenant GetMailboxByID err=%v, want ErrCrossTenantAccess", err)
	}
	if _, err := querier.GetDomainByID(tenant10, store.GetDomainByIDParams{TenantID: 20, ID: 200}); !errors.Is(err, isolation.ErrCrossTenantAccess) {
		t.Fatalf("cross-tenant GetDomainByID err=%v, want ErrCrossTenantAccess", err)
	}

	// 3. Reclassified platform session methods reject tenant 10 context; session stays intact.
	if err := querier.MarkSessionMFAVerified(tenant10, "sess-active"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("MarkSessionMFAVerified tenant ctx err=%v", err)
	}
	if err := querier.DeleteSession(tenant10, "sess-active"); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("DeleteSession tenant ctx err=%v", err)
	}
	var mfaVerified sql.NullInt64
	if err := sqliteDB.QueryRow(`SELECT mfa_verified_at FROM sessions WHERE id='sess-active'`).Scan(&mfaVerified); err != nil || mfaVerified.Valid {
		t.Fatalf("session unexpectedly modified after rejected tenant call: valid=%v err=%v", mfaVerified.Valid, err)
	}

	// 4. Platform context can mark MFA verified and delete the session.
	if err := querier.MarkSessionMFAVerified(platformCtx, "sess-active"); err != nil {
		t.Fatalf("platform MarkSessionMFAVerified failed: %v", err)
	}
	if err := sqliteDB.QueryRow(`SELECT mfa_verified_at FROM sessions WHERE id='sess-active'`).Scan(&mfaVerified); err != nil || !mfaVerified.Valid {
		t.Fatalf("expected mfa_verified_at set: valid=%v err=%v", mfaVerified.Valid, err)
	}
	if err := querier.DeleteSession(platformCtx, "sess-active"); err != nil {
		t.Fatalf("platform DeleteSession failed: %v", err)
	}

	// 5. System cleanup rejects tenant/platform, succeeds under system scope.
	if err := querier.DeleteExpiredSessions(tenant10); !errors.Is(err, isolation.ErrSystemScopeRequired) {
		t.Fatalf("DeleteExpiredSessions tenant ctx err=%v", err)
	}
	if err := querier.DeleteExpiredSessions(platformCtx); !errors.Is(err, isolation.ErrSystemScopeRequired) {
		t.Fatalf("DeleteExpiredSessions platform ctx err=%v", err)
	}
	if err := querier.DeleteExpiredSessions(systemCtx); err != nil {
		t.Fatalf("DeleteExpiredSessions system ctx failed: %v", err)
	}
	var remainingSessions int
	if err := sqliteDB.QueryRow(`SELECT count(*) FROM sessions`).Scan(&remainingSessions); err != nil || remainingSessions != 0 {
		t.Fatalf("expected 0 sessions remaining, got %d (err=%v)", remainingSessions, err)
	}

	// 6. Transaction + WithTx isolation enforcement against real SQLite with DBTXScopeHook.
	expiresAt := time.Now().Add(time.Hour).UnixMicro()
	// ArchiveCasesCreate is platform-scoped: default db.Querier rejects tenant10 before touching SQLite.
	if _, err := querier.ArchiveCasesCreate(tenant10, store.ArchiveCasesCreateParams{
		TenantID:   10,
		ActorID:    101,
		Title:      "Case A",
		Purpose:    "Audit",
		ExpiresAt:  expiresAt,
		RequestKey: "11111111-1111-1111-1111-111111111111",
	}); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("Querier ArchiveCasesCreate under tenant10 ctx err=%v, want ErrPlatformScopeRequired", err)
	}

	SetIsolationHook(isolation.DBTXScopeHook())
	t.Cleanup(func() { SetIsolationHook(isolation.DBTXScopeHook()) })

	q := Queries(shim)
	if _, err := q.ArchiveCasesCreate(platformCtx, store.ArchiveCasesCreateParams{
		TenantID:   10,
		ActorID:    101,
		Title:      "Case A",
		Purpose:    "Audit",
		ExpiresAt:  expiresAt,
		RequestKey: "11111111-1111-1111-1111-111111111111",
	}); err != nil {
		t.Fatalf("runTx/WithTx ArchiveCasesCreate under valid platformCtx failed: %v", err)
	}
	var caseCount int
	if err := sqliteDB.QueryRow(`SELECT count(*) FROM archive_investigations WHERE tenant_id=10`).Scan(&caseCount); err != nil || caseCount != 1 {
		t.Fatalf("expected 1 archive case committed through WithTx, got %d (err=%v)", caseCount, err)
	}

	tx, err := shim.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin sqlite tx: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	qtx := q.WithTx(tx)
	if _, err := qtx.GetMailboxByID(context.Background(), store.GetMailboxByIDParams{TenantID: 10, ID: 1000}); !errors.Is(err, isolation.ErrMissingTenantScope) {
		t.Fatalf("sqlite q.WithTx without scope err=%v, want ErrMissingTenantScope", err)
	}
	gotMB, err := qtx.GetMailboxByID(tenant10, store.GetMailboxByIDParams{TenantID: 10, ID: 1000})
	if err != nil || gotMB.ID != 1000 {
		t.Fatalf("sqlite q.WithTx valid tenant read got mb=%+v err=%v", gotMB, err)
	}
}
