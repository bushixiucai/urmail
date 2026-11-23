package handlers

import (
	"context"
	"testing"
	"time"

	"errors"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"

	"github.com/bushixiucai/urmail/internal/testdb"
)

type recordingSieveActivator struct{ called chan struct{} }

func (a recordingSieveActivator) ActivateSieve(context.Context, int64, int64, string) error {
	select {
	case a.called <- struct{}{}:
	default:
	}
	return nil
}

// TestRunSieveReconcilerOwnsBackgroundScope proves persisted desired Sieve
// scripts are replayed from an unscoped background context, while the same
// platform query still refuses a tenant-scoped entry context.
func TestRunSieveReconcilerOwnsBackgroundScope(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, _ := testdb.OpenMigrated(t)
	var brand, tenant, domain, mailbox int64
	if err := pool.QueryRow(ctx, `INSERT INTO brands(slug,name) VALUES('sieve-scope','Sieve scope') RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Sieve','sieve-scope','active') RETURNING id`, brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at,provisioning_state) VALUES($1,'sieve.example.test','active','fixture',now(),'ready') RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status,sieve_mode,sieve_script) VALUES($1,$2,'alice','alice@sieve.example.test','fixture',1048576,'active','managed','require "fileinto";') RETURNING id`, tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	activator := recordingSieveActivator{called: make(chan struct{}, 1)}
	go RunSieveReconciler(ctx, pool, activator, zap.NewNop())
	select {
	case <-activator.called:
	case <-time.After(10 * time.Second):
		t.Fatal("desired Sieve script was not replayed")
	}
	cancel()
	tenantCtx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: tenant})
	if _, err := db.Querier(pool.Pool).PlatformListSieveScripts(tenantCtx); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
		t.Fatalf("tenant entry context listed platform Sieve scripts: %v", err)
	}
}
