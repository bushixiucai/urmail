package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/sieve"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestSieveActivationCompileFailurePreservesPreviousScript(t *testing.T) {
	ctx := t.Context()
	dsn := testdb.New(t)
	migrator, err := db.NewMigrator(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	if err = migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var brand, tenant, domain, mailbox int64
	if err = pool.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('sieve-agent','Sieve') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Sieve','sieve-agent','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'agent.example.test','active','test',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	script, err := sieve.Compile(sieve.Rules{Rules: []sieve.Rule{{Header: "subject", Match: "contains", Value: "review", Action: "keep"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,sieve_script) VALUES($1,$2,'own','own@agent.example.test','test',$3) RETURNING id", tenant, domain, script).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t)
	manager.pool = pool
	destination := filepath.Join(manager.target(manager.cfg.StorageRoot), "sieve", "own@agent.example.test.sieve")
	if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, []byte("previous-active-script"), 0644); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(SieveActivateParams{TenantID: tenant, MailboxID: mailbox, Digest: sieve.Digest(script)})
	compileCalls := 0
	fail := true
	manager.run = func(_ context.Context, command string, args ...string) error {
		compileCalls++
		if command != "/usr/bin/sievec" || len(args) != 2 || filepath.Dir(filepath.Dir(args[0])) != manager.root {
			t.Fatalf("unbounded compiler command: %s %v", command, args)
		}
		if fail {
			return errors.New("synthetic compiler failure")
		}
		return nil
	}
	if _, _, err = manager.ActivateSieve(params); !errors.Is(err, ErrRejected) {
		t.Fatalf("compiler error %v", err)
	}
	previous, err := os.ReadFile(destination)
	if err != nil || string(previous) != "previous-active-script" {
		t.Fatalf("compile failure touched active script: %v", err)
	}
	fail = false
	if _, _, err = manager.ActivateSieve(params); err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(destination)
	if err != nil || string(active) != script {
		t.Fatalf("canonical script not published: %v", err)
	}
	unsafe := script + "redirect \"outside@example.test\";\n"
	if _, err = pool.Exec(ctx, "UPDATE mailboxes SET sieve_script=$3 WHERE tenant_id=$1 AND id=$2", tenant, mailbox, unsafe); err != nil {
		t.Fatal(err)
	}
	attack, _ := json.Marshal(SieveActivateParams{TenantID: tenant, MailboxID: mailbox, Digest: sieve.Digest(unsafe)})
	if _, _, err = manager.ActivateSieve(attack); !errors.Is(err, ErrRejected) {
		t.Fatalf("raw Sieve injection accepted %v", err)
	}
	if info, statErr := os.Stat(destination); statErr != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("script is not private: %v", statErr)
	}
	// Even a valid canonical desired script must never replace raw scripts for
	// mailboxes explicitly owned by the retained ManageSieve filesystem.
	// Test-only seed of a preexisting backend in this owned disposable database.
	// The guard is restored within the same private transaction before requests.
	fixtureTx, fixtureErr := pool.Begin(ctx)
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	defer func() { _ = fixtureTx.Rollback(context.Background()) }()
	if _, fixtureErr = fixtureTx.Exec(ctx, "ALTER TABLE mailboxes DISABLE TRIGGER mailbox_sieve_mode_guard"); fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if _, fixtureErr = fixtureTx.Exec(ctx, "UPDATE mailboxes SET sieve_mode='legacy_v80',sieve_script=$3 WHERE tenant_id=$1 AND id=$2", tenant, mailbox, script); fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if _, fixtureErr = fixtureTx.Exec(ctx, "ALTER TABLE mailboxes ENABLE TRIGGER mailbox_sieve_mode_guard"); fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if fixtureErr = fixtureTx.Commit(ctx); fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if _, _, err = manager.ActivateSieve(params); !errors.Is(err, ErrRejected) {
		t.Fatalf("legacy mode allowed structured publication: %v", err)
	}
	if compileCalls != 2 {
		t.Fatalf("unsafe source reached compiler %d", compileCalls)
	}
	active, _ = os.ReadFile(destination)
	if string(active) != script {
		t.Fatal("rejected source replaced active script")
	}
}
