package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// Published tag v0.4.1-rc.2 ends at 25; deployed source 588c1b8 ends at 83.
// The 128 fixture additionally protects pre-hardening collaboration/device
// records. These are actual forward upgrades of populated old schemas, not
// a same-head pg_restore followed by a no-op migration.
func TestOPS009PopulatedHistoricalUpgrade(t *testing.T) {
	for _, version := range []uint{25, 83, 128} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dsn := testdb.New(t)
			testdb.MigrateTo(t, dsn, version)
			conn := ops009Connect(t, dsn)
			ops009SeedHistoricalMailbox(t, dsn, version)
			migrator, err := db.NewMigrator(ops009RoleURL(dsn))
			if err != nil {
				t.Fatal(err)
			}
			defer migrator.Close()
			old, dirty, err := migrator.Version()
			if err != nil || dirty || old != version {
				t.Fatalf("historical fixture %d %t %v", old, dirty, err)
			}
			if err = migrator.Up(t.Context()); err != nil {
				t.Fatal(err)
			}
			current, dirty, err := migrator.Version()
			if err != nil || dirty || current <= old {
				t.Fatalf("not a real forward upgrade: %d -> %d dirty=%t %v", old, current, dirty, err)
			}
			ops009AssertHistoricalMailbox(t, dsn, version)
			var epoch int64
			if err = conn.QueryRow(t.Context(), "SELECT urmail_restore_sync_epoch()").Scan(&epoch); err != nil || epoch != 1 {
				t.Fatalf("new epoch getter after upgrade: %d %v", epoch, err)
			}
		})
	}
}

func ops009SeedHistoricalMailbox(t *testing.T, dsn string, version uint) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	_, err := conn.Exec(t.Context(), `SET ROLE urmail_platform; DO $$ DECLARE b bigint;t bigint;d bigint;m bigint; BEGIN
 INSERT INTO brands(slug,name) VALUES('upgrade-source','Upgrade Source') RETURNING id INTO b;
 INSERT INTO tenants(brand_id,name,slug,status) VALUES(b,'Upgrade Source','upgrade-source','active') RETURNING id INTO t;
 INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES(t,'upgrade-source.test','active','existing-proof',now()) RETURNING id INTO d;
 INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES(t,d,'alice','alice@upgrade-source.test','historical-password-hash','active') RETURNING id INTO m;
 END $$`)
	if err != nil {
		t.Fatal(err)
	}
	if version >= 128 {
		_, err = conn.Exec(t.Context(), `DO $$ DECLARE t bigint;m bigint;ab bigint; BEGIN
 SELECT tenant_id,id INTO t,m FROM mailboxes WHERE address='alice@upgrade-source.test';
 INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES(t,m,'Historical') RETURNING id INTO ab;
 INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name,emails) VALUES(t,m,ab,'Historical Contact','[{"value":"old@example.test"}]');
 INSERT INTO mailbox_eas_devices(tenant_id,mailbox_id,device_id,protocol_version,policy_key,provisioned) VALUES(t,m,'old-phone','14.1',123,true);
 INSERT INTO mailbox_eas_collection_state(tenant_id,mailbox_id,device_id,folder_id,sync_epoch,change_seq,uid_validity) VALUES(t,m,'old-phone','ab:1',8,50,0);
 END $$`)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func ops009AssertHistoricalMailbox(t *testing.T, dsn string, version uint) {
	t.Helper()
	conn := ops009Connect(t, dsn)
	var ok bool
	if err := conn.QueryRow(t.Context(), `SELECT m.password_hash='historical-password-hash' AND m.status='active' AND d.status='active' FROM mailboxes m JOIN domains d ON d.id=m.domain_id WHERE m.address='alice@upgrade-source.test'`).Scan(&ok); err != nil || !ok {
		t.Fatalf("historical mailbox changed: %v %v", ok, err)
	}
	if version >= 128 {
		if err := conn.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM mailbox_contacts WHERE display_name='Historical Contact') AND EXISTS(SELECT 1 FROM mailbox_eas_devices WHERE device_id='old-phone' AND provisioned AND policy_key=123) AND EXISTS(SELECT 1 FROM mailbox_eas_collection_state WHERE device_id='old-phone' AND sync_epoch=8 AND change_seq=50)`).Scan(&ok); err != nil || !ok {
			t.Fatalf("upgrade lost collaboration/device mapping: %v %v", ok, err)
		}
	}
}

// An incompatible object makes migration 84 fail. Restore the untouched
// pre-upgrade backup into a separate quarantined database and migrate it
// forward. Neither Force(version) nor a speculative schema Down is used.
func TestOPS009FailedUpgradeRecoversIndependentTarget(t *testing.T) {
	ops009RequireBinaries(t)
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	source := testdb.New(t)
	testdb.MigrateTo(t, source, 83)
	ops009SeedHistoricalMailbox(t, source, 83)
	dumpPath := filepath.Join(t.TempDir(), "before-upgrade.dump")
	dump := exec.CommandContext(ctx, ops009PGBin.dump, "--format=custom", "--file", dumpPath, "--dbname", source)
	if out, err := dump.CombinedOutput(); err != nil {
		t.Fatalf("pre-upgrade pg_dump: %v %s", err, out)
	}
	conn := ops009Connect(t, source)
	if _, err := conn.Exec(ctx, "CREATE TABLE mailbox_addressbooks(incompatible_fixture boolean)"); err != nil {
		t.Fatal(err)
	}
	migrator, err := db.NewMigrator(ops009RoleURL(source))
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.Up(ctx); err == nil {
		t.Fatal("injected upgrade failure did not fail")
	}
	_, dirty, err := migrator.Version()
	if err != nil || !dirty {
		t.Fatalf("failed upgrade not marked dirty: %t %v", dirty, err)
	}
	if err = migrator.Up(ctx); err == nil {
		t.Fatal("dirty database accepted another blind upgrade")
	}
	_ = migrator.Close()
	ops009AssertHistoricalMailbox(t, source, 83)
	target := testdb.New(t)
	ops009Migrate(t, target)
	ops009BeginQuarantine(t, target)
	ops009RestorePipeline(t, target, dumpPath, "ops009-failed-upgrade-recovery")
	ops009AssertHistoricalMailbox(t, target, 83)
	if err = db.VerifyRestoreIdentity(ctx, ops009RoleURL(target), "ops009-failed-upgrade-recovery"); err != nil {
		t.Fatal(err)
	}
	if !ops009Release(t, target) {
		t.Fatal("recovered target could not be released")
	}
}

func TestMailboxRestoreIdentityRejectsCrossTenantAndChangedAddress(t *testing.T) {
	dsn := testdb.New(t)
	ops009Migrate(t, dsn)
	ops009SeedHistoricalMailbox(t, dsn, 83)
	conn := ops009Connect(t, dsn)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), "SET LOCAL ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	var tenant, mailbox int64
	if err = tx.QueryRow(t.Context(), "SELECT tenant_id,id FROM mailboxes WHERE address='alice@upgrade-source.test'").Scan(&tenant, &mailbox); err != nil {
		t.Fatal(err)
	}
	q := store.New(tx)
	row, err := q.PlatformMailboxRestoreIdentity(t.Context(), store.PlatformMailboxRestoreIdentityParams{TenantID: tenant, ID: mailbox})
	if err != nil || row.Address != "alice@upgrade-source.test" {
		t.Fatalf("current identity: %+v %v", row, err)
	}
	if _, err = q.PlatformMailboxRestoreIdentity(t.Context(), store.PlatformMailboxRestoreIdentityParams{TenantID: tenant + 1, ID: mailbox}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross tenant identity leaked: %v", err)
	}
	if _, err = tx.Exec(t.Context(), "UPDATE mailboxes SET address='other@upgrade-source.test' WHERE tenant_id=$1 AND id=$2", tenant, mailbox); err == nil {
		t.Fatal("mailbox storage identity was mutable")
	}
}
