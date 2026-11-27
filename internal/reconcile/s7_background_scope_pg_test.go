package reconcile_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// TestAssembleConfigContextOwnsBackgroundScope proves the real configuration
// assembly works from an unscoped background context (the urmaild pipeline
// call site) and that entry contexts still cannot run the same platform query.
func TestAssembleConfigContextOwnsBackgroundScope(t *testing.T) {
	ctx := context.Background()
	pool, _ := testdb.OpenMigrated(t)
	var brand, tenant int64
	if err := pool.QueryRow(ctx, `INSERT INTO brands(slug,name) VALUES('scope','Scope') RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Scope','scope','active') RETURNING id`, brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES($1,'scope.example.test','active','fixture',now(),'ready')`, tenant); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{MyHostname: "box.scope.example.test", PrimaryDomain: "scope.example.test", StorageRoot: "/var/mail"}
	mailCtx, err := reconcile.AssembleConfigContext(ctx, cfg, &reconcile.VersionedStore{StoreQuerier: db.Queries(pool.Pool), DB: pool.Pool}, "192.0.2.1", []string{"ns1.scope.example.test"})
	if err != nil {
		t.Fatalf("background assembly failed: %v", err)
	}
	found := false
	for _, d := range mailCtx.PostfixSnap.Domains {
		found = found || d.FQDN == "scope.example.test"
	}
	if !found {
		t.Fatalf("assembled domains missing scope.example.test: %v", mailCtx.PostfixSnap.Domains)
	}
	tenantCtx := isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: tenant})
	if _, err := db.Querier(pool.Pool).PlatformListDomains(tenantCtx); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("tenant entry context ran a platform query: %v", err)
	}
}
