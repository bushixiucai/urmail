package db_test

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
)

// TestAliasTargetSyntaxConstraint pins the storage-boundary half of audit
// finding MAIL-01: the database itself must reject generator-unsafe alias
// targets on new writes, even if a future code path bypasses the API
// validators. The constraint is NOT VALID, so legacy rows are untouched.
func TestAliasTargetSyntaxConstraint(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	applyMigrations(t, url)
	platform, err := db.OpenPlatform(ctx, url)
	if err != nil {
		t.Fatalf("open platform pool: %v", err)
	}
	defer platform.Close()

	var brandID int64
	if err := platform.QueryRow(ctx, `INSERT INTO brands(slug,name) VALUES('alias-guard','Alias Guard')
		ON CONFLICT (slug) DO UPDATE SET name=EXCLUDED.name RETURNING id`).Scan(&brandID); err != nil {
		t.Fatalf("seed brand: %v", err)
	}
	var tenantID, domainID int64
	if err := platform.QueryRow(ctx, `INSERT INTO tenants(brand_id,name,slug,status)
		VALUES($1,'Alias Guard','alias-guard','active')
		RETURNING id`, brandID).Scan(&tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := platform.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token)
		VALUES($1,'guard.test','active',now(),'fixture-proof') RETURNING id`, tenantID).Scan(&domainID); err != nil {
		t.Fatalf("seed domain: %v", err)
	}

	insert := func(target string) error {
		_, err := platform.Exec(ctx, `INSERT INTO mailbox_aliases(tenant_id,domain_id,local_part,target,kind)
			VALUES($1,$2,'guard',$3,'alias')`, tenantID, domainID, target)
		return err
	}

	if err := insert("ok@example.test"); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
	for _, bad := range []string{"a*b@outside.test", "a?b@outside.test", "a/b@outside.test", "UPPER@Example.test"} {
		if err := insert(bad); err == nil {
			t.Errorf("generator-unsafe target accepted by database: %q", bad)
		}
	}
}
