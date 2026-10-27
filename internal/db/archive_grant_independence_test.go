package db_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// ARC-001 / PD-112 (NEW-21): only an independently authorized subject may
// retrieve or export archive content. The grant creator must therefore never
// be the grant beneficiary, and the invariant holds at the DATABASE layer:
// even an app-role insert through the tenant transaction is refused, not
// only the HTTP handler.
func TestArchiveGrantSelfAuthorizationRejectedByDatabase(t *testing.T) {
	platform, app := testdb.OpenMigrated(t)
	ctx := context.Background()
	var brand, tenant, owner, grantor, admin int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('grant-ind','GrantInd') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'GI','grant-ind','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		email string
		id    *int64
	}{{"gi-owner@example.test", &owner}, {"gi-grantor@example.test", &grantor}, {"gi-admin@example.test", &admin}} {
		if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id", target.email).Scan(target.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner'),($3,$4,'owner'),($5,$6,'admin')", owner, tenant, grantor, tenant, admin, tenant); err != nil {
		t.Fatal(err)
	}
	expires := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}

	// The tenant owner cannot grant themself content retrieval or export
	// authority through the application path.
	for _, scope := range []string{"full_content", "export", "metadata_only"} {
		tx, err := app.UserTx(ctx, tenant, owner)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{
			TenantID: tenant, UserID: owner, Scope: scope, Reason: "self authorization attempt",
			ExpiresAt: expires, GrantedBy: pgtype.Int8{Int64: owner, Valid: true},
		})
		_ = tx.Rollback(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "COMPLIANCE_GRANT_SELF_AUTHORIZATION" {
			t.Fatalf("self-grant %s: err=%v, want COMPLIANCE_GRANT_SELF_AUTHORIZATION/23514", scope, err)
		}
	}

	// The same invariant holds on a platform connection: the trigger fires
	// regardless of the writing role, so no administrative path can mint a
	// self-authorization either.
	ptx, err := platform.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ptx.Rollback(ctx) }()
	if _, err = ptx.Exec(ctx,
		"SELECT set_config('urmail.tenant_id',id::text,true),set_config('urmail.user_id',$2,true) FROM tenants WHERE id=$1", tenant, strconv.FormatInt(owner, 10)); err != nil {
		t.Fatal(err)
	}
	_, err = ptx.Exec(ctx,
		"INSERT INTO compliance_grants(tenant_id,user_id,scope,expires_at,reason,granted_by,owner_confirmed_at)"+
			" VALUES($1,$1,'full_content',$2,'platform self grant',$1,now())", tenant, expires.Time)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Message != "COMPLIANCE_GRANT_SELF_AUTHORIZATION" {
		t.Fatalf("platform self-grant insert: err=%v, want COMPLIANCE_GRANT_SELF_AUTHORIZATION", err)
	}
	if err = ptx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// A normal two-user grant still works end to end: the grantor records the
	// beneficiary admin with full owner confirmation.
	gtx, err := app.UserTx(ctx, tenant, grantor)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.New(gtx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{
		TenantID: tenant, UserID: admin, Scope: "full_content", Reason: "independent investigation approval",
		ExpiresAt: expires, GrantedBy: pgtype.Int8{Int64: grantor, Valid: true},
	})
	if err != nil {
		_ = gtx.Rollback(ctx)
		t.Fatalf("two-user grant rejected: %v", err)
	}
	if err = gtx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var beneficiary, by int64
	var confirmed bool
	if err = platform.QueryRow(ctx,
		"SELECT user_id,granted_by::bigint,owner_confirmed_at IS NOT NULL FROM compliance_grants WHERE id=$1", grant.ID).Scan(&beneficiary, &by, &confirmed); err != nil {
		t.Fatal(err)
	}
	if beneficiary != admin || by != grantor || !confirmed {
		t.Fatalf("stored grant beneficiary=%d grantor=%d confirmed=%v", beneficiary, by, confirmed)
	}
}
