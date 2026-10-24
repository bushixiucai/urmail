package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestRetentionManagementRequiresIndependentOwnerGrantAndVersion(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tenant, owner, admin int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('retention-authority','Retention') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Retention','retention-authority','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		email, role string
		id          *int64
	}{{"retention-owner@example.test", "owner", &owner}, {"retention-admin@example.test", "admin", &admin}} {
		if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id", item.email).Scan(item.id); err != nil {
			t.Fatal(err)
		}
		if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles VALUES($1,$2,$3)", *item.id, tenant, item.role); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(scope string) int64 {
		t.Helper()
		tx, err := app.UserTx(ctx, tenant, owner)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		row, err := store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: admin, Scope: scope, Reason: "Explicit bounded authority for an administrator", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, GrantedBy: pgtype.Int8{Int64: owner, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	grant("full_content")
	grant("export")
	denied := func() {
		t.Helper()
		tx, err := app.UserTx(ctx, tenant, admin)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err = store.New(tx).PreviewArchiveRetention(ctx, store.PreviewArchiveRetentionParams{TenantID: tenant, RetentionDays: 30, CreatedBy: admin}); err == nil {
			t.Fatal("content/export access granted retention authority through raw SQL")
		}
	}
	denied()
	tx, err := app.UserTx(ctx, tenant, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).CreateComplianceGrant(ctx, store.CreateComplianceGrantParams{TenantID: tenant, UserID: admin, Scope: "retention_manage", Reason: "Admin cannot self-grant", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, GrantedBy: pgtype.Int8{Int64: admin, Valid: true}}); err == nil {
		t.Fatal("administrator self-granted destructive authority")
	}
	_ = tx.Rollback(ctx)
	management := grant("retention_manage")
	tx, err = app.UserTx(ctx, tenant, admin)
	if err != nil {
		t.Fatal(err)
	}
	queries := store.New(tx)
	preview, err := queries.PreviewArchiveRetention(ctx, store.PreviewArchiveRetentionParams{TenantID: tenant, RetentionDays: 30, CreatedBy: admin})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.AuthorizationGrantID.Valid || preview.AuthorizationGrantID.Int64 != management {
		t.Fatal("preview did not pin exact owner grant")
	}
	if err = queries.ConfirmArchiveRetentionPreview(ctx, store.ConfirmArchiveRetentionPreviewParams{TenantID: tenant, ID: preview.ID}); err != nil {
		t.Fatal(err)
	}
	if err = queries.ActivateArchiveRetention(ctx, store.ActivateArchiveRetentionParams{TenantID: tenant, RetentionDays: 30, CreatedBy: pgtype.Int8{Int64: admin, Valid: true}, ConfirmedPreview: preview.ID, AuthorizationGrantID: preview.AuthorizationGrantID}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = app.UserTx(ctx, tenant, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(tx).RevokeComplianceGrant(ctx, store.RevokeComplianceGrantParams{TenantID: tenant, ID: management, RevokedBy: pgtype.Int8{Int64: owner, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		allowed, err := store.New(platform).GetRetentionPolicyAuthority(ctx, store.GetRetentionPolicyAuthorityParams{TenantID: tenant, CreatedBy: admin, GrantID: management})
		if err != nil || allowed {
			t.Fatalf("revoked original policy grant still authorized: %v %v", allowed, err)
		}
	}
	check()
	replacement := grant("retention_manage")
	check()
	if allowed, err := store.New(platform).GetRetentionPolicyAuthority(ctx, store.GetRetentionPolicyAuthorityParams{TenantID: tenant, CreatedBy: admin, GrantID: replacement}); err != nil || !allowed {
		t.Fatalf("new independent grant unavailable: %v %v", allowed, err)
	}
	if _, err := platform.Exec(ctx, "UPDATE user_tenant_roles SET role='billing' WHERE tenant_id=$1 AND user_id=$2", tenant, admin); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.New(platform).GetRetentionPolicyAuthority(ctx, store.GetRetentionPolicyAuthorityParams{TenantID: tenant, CreatedBy: admin, GrantID: replacement}); err != nil || allowed {
		t.Fatalf("recipient downgrade retained destructive authority: %v %v", allowed, err)
	}
	if _, err := platform.Exec(ctx, "UPDATE user_tenant_roles SET role='admin' WHERE tenant_id=$1 AND user_id=$2", tenant, admin); err != nil {
		t.Fatal(err)
	}
	// Grant replacement does not reactivate a policy bound to a revoked grant.
	ptx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := store.New(ptx).PlatformLockRetentionPurgePolicy(ctx, store.PlatformLockRetentionPurgePolicyParams{TenantID: tenant, PreviewID: preview.ID})
	if err != nil || valid {
		t.Fatalf("purge accepted replacement authority for old intent: %v %v", valid, err)
	}
	_ = ptx.Rollback(ctx)
	// Stopping future cleanup remains possible after permission revocation.
	tx, err = app.UserTx(ctx, tenant, admin)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.New(tx).DisableTenantRetentionPolicy(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
