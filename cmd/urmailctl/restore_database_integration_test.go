package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestRestrictedRestoreDatabaseCommandPipeline(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("fixed root CLI tools are exercised in the isolated Linux container")
	}
	for _, path := range []string{"/usr/bin/pg_dump", "/usr/bin/pg_restore", "/usr/bin/psql"} {
		if _, err := os.Stat(path); err != nil {
			t.Skip("fixed PostgreSQL utilities unavailable")
		}
	}
	ctx := context.Background()
	source := testdb.New(t)
	target := testdb.New(t)
	roleURL := func(raw string) string {
		u, _ := url.Parse(raw)
		q := u.Query()
		q.Set("role", "urmail_migrate")
		u.RawQuery = q.Encode()
		return u.String()
	}
	for _, dsn := range []string{source, target} {
		m, err := db.NewMigrator(roleURL(dsn))
		if err != nil {
			t.Fatal(err)
		}
		if err = m.Up(ctx); err != nil {
			t.Fatal(err)
		}
		m.Close()
	}
	sourceConn, err := pgx.Connect(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceConn.Close(ctx)
	if _, err = sourceConn.Exec(ctx, "INSERT INTO brands(slug,name) VALUES('cli-source','Source fixture')"); err != nil {
		t.Fatal(err)
	}
	targetConn, err := pgx.Connect(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer targetConn.Close(ctx)
	if _, err = targetConn.Exec(ctx, "INSERT INTO brands(slug,name) VALUES('cli-target','Old target fixture')"); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	dump := filepath.Join(directory, "snapshot.dump")
	command, err := postgresCommand(ctx, "pg_dump", source, "--format=custom", "--file", dump)
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Run(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, "marker")
	if _, err = createRestoreQuarantine(marker); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireRestoreOperation(marker)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err = restoreDatabaseArchive(ctx, target, dump, marker, lock); err != nil {
		t.Fatal(err)
	}
	if err = db.FinalizeRestore(ctx, roleURL(target), "cli-command-pipeline"); err != nil {
		t.Fatal(err)
	}
	if err = db.HoldRestoredSideEffects(ctx, roleURL(target), "cli-command-pipeline"); err != nil {
		t.Fatal(err)
	}
	if err = db.VerifyRuntimePrivileges(ctx, roleURL(target)); err != nil {
		t.Fatal(err)
	}
	var restored bool
	if err = targetConn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM brands WHERE slug='cli-source') AND NOT EXISTS(SELECT 1 FROM brands WHERE slug='cli-target')").Scan(&restored); err != nil || !restored {
		t.Fatalf("restore facts=%v %v", restored, err)
	}
}
