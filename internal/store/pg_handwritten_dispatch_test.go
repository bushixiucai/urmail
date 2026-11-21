package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sqlitedb"
	store "github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// End-to-end dispatch proof for the QA final-review remediation: every
// formerly unmapped handwritten method must execute native SQL on PostgreSQL
// (never ErrPostgreSQLQueryUnavailable) and keep the native SQLite path.

const (
	dispatchTokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dispatchTokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// dispatchShim adapts *sql.DB (modernc SQLite) to the pgx-shaped DBTX with the
// SQLite dialect marker set, so store.New keeps the native handwritten path.
type dispatchShim struct{ db *sql.DB }

func (dispatchShim) IsSQLite() bool { return true }

func (s dispatchShim) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return pgconn.CommandTag{}, err
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (s dispatchShim) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return dispatchRowShim{row: s.db.QueryRowContext(ctx, query, args...)}
}

func (s dispatchShim) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &dispatchRowsShim{rows: rows}, nil
}

type dispatchRowsShim struct{ rows *sql.Rows }

func (r *dispatchRowsShim) Next() bool                                   { return r.rows.Next() }
func (r *dispatchRowsShim) Scan(dest ...any) error                       { return r.rows.Scan(dest...) }
func (r *dispatchRowsShim) Err() error                                   { return r.rows.Err() }
func (r *dispatchRowsShim) Close()                                       { _ = r.rows.Close() }
func (r *dispatchRowsShim) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *dispatchRowsShim) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *dispatchRowsShim) Conn() *pgx.Conn                              { return nil }
func (r *dispatchRowsShim) RawValues() [][]byte                          { return nil }
func (r *dispatchRowsShim) TypeMap() *pgtype.Map                         { return nil }
func (r *dispatchRowsShim) Values() ([]any, error)                       { return nil, nil }

type dispatchRowShim struct{ row *sql.Row }

func (r dispatchRowShim) Scan(dest ...any) error { return r.row.Scan(dest...) }

func openDispatchSQLite(t *testing.T) (dispatchShim, *store.Queries) {
	t.Helper()
	handle, err := sqlitedb.Open(context.Background(), filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	for _, name := range []string{"0001_init.sql", "0002_write_side_effects.sql"} {
		body, readErr := os.ReadFile(filepath.Join("..", "..", "migrations_sqlite", name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if _, execErr := handle.ExecContext(context.Background(), string(body)); execErr != nil {
			t.Fatalf("apply %s: %v", name, execErr)
		}
	}
	shim := dispatchShim{db: handle}
	return shim, store.New(shim)
}

const dispatchSeeds = `
INSERT INTO brands(id,slug,name) VALUES(1,'dispatch','Dispatch');
INSERT INTO users(id,email,password_hash,status) VALUES(3,'d3@example.test','x','active');
INSERT INTO users(id,email,password_hash,status) VALUES(4,'d4@example.test','x','active');
INSERT INTO tenants(id,brand_id,name,slug,status,owner_user_id) VALUES(7,1,'Dispatch','dispatch','active',3);
INSERT INTO domains(id,tenant_id,fqdn,status,verification_token,verified_at,provisioning_state,mode)
  VALUES(31,7,'dispatch.test','active','tok',1737412800123456,'pending','managed');
UPDATE domains SET provisioning_state='ready' WHERE id=31;
INSERT INTO mailboxes(id,tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,send_enabled)
  VALUES(11,7,31,'disp','disp@dispatch.test','x',-1,'active',1);
INSERT INTO mailbox_sessions(token_hash,tenant_id,mailbox_id,password_version,expires_at)
  VALUES('aaa…TOKEN_A…',7,11,'1',1737416400123456);
INSERT INTO mailbox_operation_tickets(token_hash,tenant_id,mailbox_id,session_hash,scope,expires_at)
  VALUES('bbb…TOKEN_B…',7,11,'aaa…TOKEN_A…','ai_context',1737416400123456);
INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(3,7,'owner');
INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES(4,7,'owner');
INSERT INTO compliance_grants(id,tenant_id,user_id,scope,reason,granted_by,owner_confirmed_at,expires_at)
  VALUES(201,7,3,'retention_manage','dispatch retention',4,1737412800123456,1737499200123456);
INSERT INTO retention_previews(id,tenant_id,retention_days,candidate_count,candidate_digest,created_by,confirmed_at,authorization_grant_id)
  VALUES('6ba7b810-9dad-11d1-80b4-00c04fd430c8',7,30,1,'digest',3,1737412800123456,201);
INSERT INTO retention_policies(tenant_id,retention_days,action,active,created_by,confirmed_preview,authorization_grant_id)
  VALUES(7,30,'soft_delete',1,3,'6ba7b810-9dad-11d1-80b4-00c04fd430c8',201);
INSERT INTO archive_entries(id,tenant_id,mailbox_id,message_id,path,received_at)
  VALUES(101,7,11,'mid-dispatch','cur/dispatch',1737412800123456);
INSERT INTO archive_content(entry_id,tenant_id) VALUES(101,7);
INSERT INTO marketing_flow_definitions(id,tenant_id,name,draft) VALUES(1,7,'DispatchDef','{}');
INSERT INTO marketing_flows(id,tenant_id,name,status) VALUES(501,7,'DispatchFlow','active');
INSERT INTO marketing_flow_versions(id,tenant_id,definition_id,version,draft_revision,runtime_flow_id,definition,published_by)
  VALUES(1,7,1,1,1,501,'{}',3);
INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,lease_owner,lease_until,lease_fence,event_facts)
  VALUES(7,501,'disp@dispatch.test','send','worker-1',1737416400123456,1,'{}');
`

// dispatchSeedStatements renders the fixture set for one engine. SQLite stores
// timestamps as unix micros; PostgreSQL uses native timestamptz.
func dispatchSeedStatements(pg bool) []string {
	replace := func(s, sqlite, postgres string) string {
		if pg {
			return strings.ReplaceAll(s, sqlite, postgres)
		}
		return s
	}
	var out []string
	for _, stmt := range strings.Split(dispatchSeeds, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		stmt = replace(stmt, "aaa…TOKEN_A…", dispatchTokenA)
		stmt = replace(stmt, "bbb…TOKEN_B…", dispatchTokenB)
		stmt = replace(stmt, "1737416400123456", "now()+interval '1 hour'")
		stmt = replace(stmt, "1737499200123456", "now()+interval '24 hours'")
		stmt = replace(stmt, "1737412800123456", "now()")
		if pg && (strings.Contains(stmt, "INSERT INTO compliance_grants") ||
			strings.Contains(stmt, "INSERT INTO retention_previews") ||
			strings.Contains(stmt, "INSERT INTO retention_policies") ||
			strings.Contains(stmt, "INSERT INTO marketing_flow_definitions") ||
			strings.Contains(stmt, "INSERT INTO marketing_flows(") ||
			strings.Contains(stmt, "INSERT INTO marketing_flow_versions") ||
			strings.Contains(stmt, "INSERT INTO marketing_flow_positions")) {
			continue // PG inserts need base roles first + session GUCs; see pgSeedGrant
		}
		if pg {
			stmt = strings.ReplaceAll(stmt, "-1,'active',1)", "-1,'active',true)")
			stmt = strings.ReplaceAll(stmt, ",1,'{}')", ",true,'{}')")
			if strings.Contains(stmt, "INSERT INTO archive_entries") {
				// Retention candidacy requires received_at older than the policy horizon.
				stmt = strings.ReplaceAll(stmt, "'cur/dispatch',now()", "'cur/dispatch',now()-interval '40 days'")
			}
		}
		out = append(out, stmt)
	}
	return out
}

// pgSeedGrant inserts the retention_manage compliance grant under the session
// GUCs the urmail_require_content_owner trigger enforces (actor=granted_by,
// owner on the tenant).
func pgSeedGrant(t *testing.T, ctx context.Context, pool *db.Pool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []string{
		`SET LOCAL urmail.tenant_id=7`,
		`SET LOCAL urmail.user_id=4`,
		`INSERT INTO compliance_grants(id,tenant_id,user_id,scope,reason,granted_by,owner_confirmed_at,expires_at)
		  VALUES(201,7,3,'retention_manage','dispatch retention',4,now(),now()+interval '24 hours')`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("pgSeedGrant %q: %v", stmt, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func seedDispatch(t *testing.T, ctx context.Context, exec func(context.Context, string) error, pg bool) {
	t.Helper()
	for _, stmt := range dispatchSeedStatements(pg) {
		if err := exec(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
}

// runDispatchAssertions drives the seven formerly unmapped runtime methods and
// asserts their real effects.
func runDispatchAssertions(t *testing.T, ctx context.Context, q *store.Queries, flowID int64) {
	t.Helper()
	n, err := q.PlatformRevokeMailboxSessions(ctx, store.PlatformRevokeMailboxSessionsParams{TenantID: 7, MailboxID: 11})
	if err != nil || n != 1 {
		t.Fatalf("PlatformRevokeMailboxSessions n=%d err=%v", n, err)
	}
	n, err = q.PlatformConsumeMailboxOperationTickets(ctx, store.PlatformConsumeMailboxOperationTicketsParams{TenantID: 7, MailboxID: 11})
	if err != nil || n != 1 {
		t.Fatalf("PlatformConsumeMailboxOperationTickets n=%d err=%v", n, err)
	}
	candidates, err := q.PlatformApplyArchiveRetentionCandidates(ctx)
	if err != nil || len(candidates) != 1 || candidates[0].ID != 101 || candidates[0].TenantID != 7 {
		t.Fatalf("PlatformApplyArchiveRetentionCandidates rows=%+v err=%v", candidates, err)
	}
	if n, err = q.PlatformApplyArchiveRetentionMarkContent(ctx); err != nil || n != 1 {
		t.Fatalf("PlatformApplyArchiveRetentionMarkContent n=%d err=%v", n, err)
	}
	if n, err = q.PlatformApplyArchiveRetentionMarkDeleted(ctx); err != nil || n != 1 {
		t.Fatalf("PlatformApplyArchiveRetentionMarkDeleted n=%d err=%v", n, err)
	}
	if n, err = q.PlatformRecordMarketingFlowNodeExecution(ctx, store.PlatformRecordMarketingFlowNodeExecutionParams{
		TenantID: 7, FlowID: flowID, Email: "disp@dispatch.test", NodeKey: "send", Status: "advanced",
		ErrorCode: "", LeaseOwner: "worker-1", LeaseFence: 1,
	}); err != nil || n != 1 {
		t.Fatalf("PlatformRecordMarketingFlowNodeExecution n=%d err=%v", n, err)
	}
	if n, err = q.PlatformBumpMarketingFlowAttempt(ctx, store.PlatformBumpMarketingFlowAttemptParams{
		TenantID: 7, FlowID: flowID, Email: "disp@dispatch.test",
		LeaseOwner: "worker-1", LeaseFence: 1, ErrorCode: "probe", BackoffSeconds: 30,
	}); err != nil || n != 1 {
		t.Fatalf("PlatformBumpMarketingFlowAttempt n=%d err=%v", n, err)
	}
}

func TestSQLiteHandwrittenDispatch(t *testing.T) {
	shim, q := openDispatchSQLite(t)
	ctx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopePlatform})
	seedDispatch(t, ctx, func(ctx context.Context, stmt string) error {
		_, err := shim.Exec(ctx, stmt)
		return err
	}, false)
	runDispatchAssertions(t, ctx, q, 501)
}

func TestPGHandwrittenDispatchRealEffects(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := db.Queries(pool)
	platformCtx := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopePlatform})
	seedDispatch(t, platformCtx, func(ctx context.Context, stmt string) error {
		_, err := pool.Exec(ctx, stmt)
		return err
	}, true)
	pgSeedGrant(t, ctx, pool)
	// Mirror the real confirmation flow: the management guard nulls
	// confirmed_at on INSERT, then an identical-row UPDATE confirms it.
	if _, err := pool.Exec(platformCtx,
		`INSERT INTO retention_previews(id,tenant_id,retention_days,candidate_count,candidate_digest,created_by,authorization_grant_id)
		  VALUES('6ba7b810-9dad-11d1-80b4-00c04fd430c8',7,30,1,'digest',3,201)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(platformCtx,
		`UPDATE retention_previews SET confirmed_at=now()
		  WHERE id='6ba7b810-9dad-11d1-80b4-00c04fd430c8' AND tenant_id=7`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(platformCtx,
		`INSERT INTO retention_policies(tenant_id,retention_days,action,active,created_by,confirmed_preview,authorization_grant_id)
		  VALUES(7,30,'soft_delete',true,3,'6ba7b810-9dad-11d1-80b4-00c04fd430c8',201)`); err != nil {
		t.Fatal(err)
	}
	var defID, flowID int64
	if err := pool.QueryRow(platformCtx,
		`INSERT INTO marketing_flow_definitions(tenant_id,name,draft) VALUES(7,'DispatchDef','{}') RETURNING id`).Scan(&defID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(platformCtx,
		`INSERT INTO marketing_flows(tenant_id,name,status) VALUES(7,'DispatchFlow','active') RETURNING id`).Scan(&flowID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(platformCtx,
		`INSERT INTO marketing_flow_versions(tenant_id,definition_id,version,draft_revision,runtime_flow_id,definition,published_by)
		  VALUES(7,$1,1,1,$2,'{}',3)`, defID, flowID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(platformCtx,
		`INSERT INTO marketing_flow_positions(tenant_id,flow_id,email,step_key,lease_owner,lease_until,lease_fence,event_facts)
		  VALUES(7,$1,'disp@dispatch.test','send','worker-1',now()+interval '1 hour',1,'{}')`, flowID); err != nil {
		t.Fatal(err)
	}
	runDispatchAssertions(t, platformCtx, q, flowID)

	// Effect spot checks (PostgreSQL-native time markers).
	var revoked, consumed, deleted, attempts int
	if err := pool.QueryRow(platformCtx,
		`SELECT count(*) FROM mailbox_sessions WHERE token_hash=$1 AND revoked_at IS NOT NULL`, dispatchTokenA,
	).Scan(&revoked); err != nil || revoked != 1 {
		t.Fatalf("revoked marker=%d err=%v", revoked, err)
	}
	if err := pool.QueryRow(platformCtx,
		`SELECT count(*) FROM mailbox_operation_tickets WHERE token_hash=$1 AND consumed_at IS NOT NULL`, dispatchTokenB,
	).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("consumed marker=%d err=%v", consumed, err)
	}
	if err := pool.QueryRow(platformCtx,
		`SELECT count(*) FROM archive_entries WHERE id=101 AND tenant_id=7 AND deleted_at IS NOT NULL`,
	).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("soft-delete marker=%d err=%v", deleted, err)
	}
	if err := pool.QueryRow(platformCtx,
		`SELECT attempt_count FROM marketing_flow_positions WHERE tenant_id=7 AND flow_id=$1 AND email='disp@dispatch.test'`, flowID,
	).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempt_count=%d err=%v", attempts, err)
	}
	var nodeExec int
	if err := pool.QueryRow(platformCtx,
		`SELECT count(*) FROM marketing_flow_node_executions e JOIN marketing_flow_positions p ON p.id=e.position_id AND p.tenant_id=e.tenant_id WHERE e.tenant_id=7 AND p.flow_id=$1 AND e.node_key='send' AND e.status='advanced'`, flowID,
	).Scan(&nodeExec); err != nil || nodeExec != 1 {
		t.Fatalf("node_exec marker=%d err=%v", nodeExec, err)
	}
	var preview string
	if err := pool.QueryRow(platformCtx,
		`SELECT retention_preview FROM archive_content WHERE entry_id=101 AND tenant_id=7`,
	).Scan(&preview); err != nil || preview == "" {
		t.Fatalf("retention preview stamp=%q err=%v", preview, err)
	}
}

func TestPGInterfaceMethodsNoDialectError(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	m, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := db.Queries(pool)
	tenantCtx := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: 7, ActorID: 3})

	dialectErr := func(err error) bool { return errors.Is(err, store.ErrPostgreSQLQueryUnavailable) }
	if _, err := q.ArchiveCasesGetPendingApproval(tenantCtx, store.ArchiveCasesGetPendingApprovalParams{TenantID: 7, CaseID: 999, ActorID: 3, Digest: "d"}); dialectErr(err) {
		t.Fatalf("ArchiveCasesGetPendingApproval dialect error: %v", err)
	}
	if _, err := q.ArchiveCasesInsertApproval(tenantCtx, store.ArchiveCasesInsertApprovalParams{TenantID: 7, ActorID: 3, Digest: "d", CaseID: 999, Version: 1}); dialectErr(err) {
		t.Fatalf("ArchiveCasesInsertApproval dialect error: %v", err)
	}
	if _, err := q.GetRecallByEntry(tenantCtx, store.GetRecallByEntryParams{TenantID: 7, ArchiveEntryID: pgtype.Int8{Int64: 999, Valid: true}}); dialectErr(err) {
		t.Fatalf("GetRecallByEntry dialect error: %v", err)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetRecallByEntry expected row-absent business error, got %v", err)
	}
	if _, err := q.GetRecallReviewPreview(tenantCtx, store.GetRecallReviewPreviewParams{TenantID: 7, RecallID: 999, ActorID: pgtype.Int8{}, Token: pgtype.Text{}, Digest: pgtype.Text{}}); dialectErr(err) {
		t.Fatalf("GetRecallReviewPreview dialect error: %v", err)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetRecallReviewPreview expected row-absent business error, got %v", err)
	}
}
