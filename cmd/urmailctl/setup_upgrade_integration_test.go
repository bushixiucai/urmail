package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// These fixtures start at a real earlier schema, not at current head followed
// by a no-op Up. Existing identity and device facts must survive the upgrade.
func TestSelfhostUpgradePreservesIdentityAndDevices(t *testing.T) {
	for _, version := range []uint{77, 83, 118} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dsn := testdb.New(t)
			testdb.MigrateTo(t, dsn, version)
			conn, tenant, mailbox := seedSelfhostUpgrade(t, dsn)
			ctx := t.Context()
			if version >= 118 {
				var book, calendar int64
				if err := conn.QueryRow(ctx, `INSERT INTO mailbox_addressbooks(tenant_id,mailbox_id,name) VALUES($1,$2,'Personal') RETURNING id`, tenant, mailbox).Scan(&book); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Exec(ctx, `INSERT INTO mailbox_contacts(tenant_id,mailbox_id,addressbook_id,display_name,emails) VALUES($1,$2,$3,'Preserved contact','[{"value":"friend@example.test"}]')`, tenant, mailbox, book); err != nil {
					t.Fatal(err)
				}
				if err := conn.QueryRow(ctx, `INSERT INTO mailbox_calendars(tenant_id,mailbox_id,name) VALUES($1,$2,'Personal') RETURNING id`, tenant, mailbox).Scan(&calendar); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Exec(ctx, `INSERT INTO mailbox_events(tenant_id,mailbox_id,calendar_id,uid,summary,starts_at,ends_at) VALUES($1,$2,$3,'upgrade-event','Preserved event','2026-10-27T10:00:00Z','2026-10-27T11:00:00Z')`, tenant, mailbox, calendar); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Exec(ctx, `INSERT INTO mailbox_eas_devices(tenant_id,mailbox_id,device_id,protocol_version,policy_key,provisioned) VALUES($1,$2,'existing-device','14.1',424242,true)`, tenant, mailbox); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Exec(ctx, `INSERT INTO mailbox_eas_folder_state(tenant_id,mailbox_id,device_id,folder_id,display_name,folder_type,folder_class,change_seq) VALUES($1,$2,'existing-device','folder-42','Inbox',2,'Email',17)`, tenant, mailbox); err != nil {
					t.Fatal(err)
				}
			}
			migrator, err := db.NewMigrator(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = migrator.Close() }()
			if err = migrator.Up(ctx); err != nil {
				t.Fatal(err)
			}
			head, dirty, err := migrator.Version()
			if err != nil || dirty || head < 130 {
				t.Fatalf("head=%d dirty=%v err=%v", head, dirty, err)
			}
			if err = migrator.Up(ctx); err != nil {
				t.Fatalf("repeat upgrade: %v", err)
			}
			var address, password string
			if err = conn.QueryRow(ctx, `SELECT address,password_hash FROM mailboxes WHERE id=$1 AND tenant_id=$2`, mailbox, tenant).Scan(&address, &password); err != nil || address != "preserved@upgrade.example.test" || password != "{PLAIN}fixture-preserved-password" {
				t.Fatalf("identity changed %q %q %v", address, password, err)
			}
			if version >= 118 {
				var count int
				if err = conn.QueryRow(ctx, `SELECT count(*) FROM mailbox_eas_devices d JOIN mailbox_eas_folder_state f USING(tenant_id,mailbox_id,device_id) WHERE d.tenant_id=$1 AND d.mailbox_id=$2 AND d.policy_key=424242 AND d.provisioned AND f.folder_id='folder-42' AND f.change_seq=17`, tenant, mailbox).Scan(&count); err != nil || count != 1 {
					t.Fatalf("device mapping lost: %d %v", count, err)
				}
				if err = conn.QueryRow(ctx, `SELECT count(*) FROM mailbox_contacts WHERE tenant_id=$1 AND mailbox_id=$2 AND display_name='Preserved contact'`, tenant, mailbox).Scan(&count); err != nil || count != 1 {
					t.Fatalf("contact lost: %d %v", count, err)
				}
				if err = conn.QueryRow(ctx, `SELECT count(*) FROM mailbox_events WHERE tenant_id=$1 AND mailbox_id=$2 AND uid='upgrade-event' AND summary='Preserved event'`, tenant, mailbox).Scan(&count); err != nil || count != 1 {
					t.Fatalf("event lost: %d %v", count, err)
				}
			}
		})
	}
}

func TestSelfhostUpgradeFailureKeepsOldFactsAndRequiresRecovery(t *testing.T) {
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 83)
	conn, tenant, mailbox := seedSelfhostUpgrade(t, dsn)
	ctx := t.Context()
	// Deliberately conflict with the first new table. This fails migration 0084
	// before it can commit, while proving the existing mail data remains intact.
	if _, err := conn.Exec(ctx, `CREATE TABLE mailbox_addressbooks(fixture_collision bool)`); err != nil {
		t.Fatal(err)
	}
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.Up(ctx); err == nil {
		t.Fatal("injected upgrade failure accepted")
	}
	version, dirty, err := migrator.Version()
	if err != nil || !dirty || version != 84 {
		t.Fatalf("failure not fenced: %d %v %v", version, dirty, err)
	}
	if err = migrator.Up(ctx); err == nil {
		t.Fatal("dirty schema was retried without diagnosis")
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE id=$1 AND tenant_id=$2 AND address='preserved@upgrade.example.test'`, mailbox, tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("old facts damaged: %d %v", count, err)
	}
	var newTable *string
	if err = conn.QueryRow(ctx, `SELECT to_regclass('public.mailbox_contacts')::text`).Scan(&newTable); err != nil || newTable != nil {
		t.Fatalf("failed migration committed new tables: %v %v", newTable, err)
	}
	_ = migrator.Close()
	// Only this owned, diagnosed fixture can repair its dirty marker: the
	// assertions above established the failed migration committed no objects.
	if _, err = conn.Exec(ctx, `DROP TABLE mailbox_addressbooks`); err != nil {
		t.Fatal(err)
	}
	source, err := iofs.New(db.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := migratepgx.WithInstance(raw, &migratepgx.Config{})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err = recovery.Force(83); err != nil {
		t.Fatal(err)
	}
	if a, b := recovery.Close(); a != nil || b != nil {
		t.Fatal(a, b)
	}
	forward, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = forward.Close() }()
	if err = forward.Up(ctx); err != nil {
		t.Fatal(err)
	}
	_, dirty, err = forward.Version()
	if err != nil || dirty {
		t.Fatalf("forward repair failed: %v %v", dirty, err)
	}
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE id=$1 AND tenant_id=$2`, mailbox, tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("forward repair lost old mailbox: %d %v", count, err)
	}
}

func seedSelfhostUpgrade(t *testing.T, dsn string) (*pgx.Conn, int64, int64) {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err = conn.Exec(t.Context(), "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	var brand, tenant, domain, mailbox int64
	if err = conn.QueryRow(t.Context(), `INSERT INTO brands(slug,name) VALUES('upgrade-fixture','Upgrade fixture') RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(t.Context(), `INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Upgrade','upgrade-fixture','active') RETURNING id`, brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(t.Context(), `INSERT INTO domains(tenant_id,fqdn,mode,status,verification_token,verified_at) VALUES($1,'upgrade.example.test','delegated','active','fixture-proof',now()) RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(t.Context(), `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash) VALUES($1,$2,'preserved','preserved@upgrade.example.test','{PLAIN}fixture-preserved-password') RETURNING id`, tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	return conn, tenant, mailbox
}
