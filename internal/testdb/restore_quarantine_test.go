package testdb

import "testing"

func TestRestoreQuarantineBlocksNewWrites(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var active bool
	if err = tx.QueryRow(ctx, "SELECT urmail_restore_quarantine_active()").Scan(&active); err != nil || active {
		t.Fatal(active, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_begin_restore_quarantine()").Scan(&active); err != nil || !active {
		t.Fatal(active, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_restore_quarantine_active()").Scan(&active); err != nil || !active {
		t.Fatal(active, err)
	}
	var brand int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('quarantine','Quarantine') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Q','quarantine-q','active')", brand); err == nil {
		t.Fatal("tenant insert succeeded")
	}
	if _, err = tx.Exec(ctx, "INSERT INTO domains(tenant_id,fqdn,mode,status,verification_token) VALUES(1,'quarantine.test','delegated','pending_dns',repeat('a',64))"); err == nil {
		t.Fatal("domain insert succeeded")
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES(1,1,'a','a@quarantine.test','x','active')"); err == nil {
		t.Fatal("mailbox insert succeeded")
	}
}

func TestRestoreQuarantineReleaseAllowsTenant(t *testing.T) {
	platform, _ := OpenMigrated(t)
	ctx := t.Context()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var active bool
	if err = tx.QueryRow(ctx, "SELECT urmail_begin_restore_quarantine()").Scan(&active); err != nil || !active {
		t.Fatal(active, err)
	}
	if err = tx.QueryRow(ctx, "SELECT urmail_release_restore_quarantine()").Scan(&active); err != nil || !active {
		t.Fatal(active, err)
	}
	var brand, tenant int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('released','Released') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'R','released-r','active') RETURNING id", brand).Scan(&tenant); err != nil || tenant <= 0 {
		t.Fatal(tenant, err)
	}
}
