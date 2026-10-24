package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestRootBackupIncludesForcedRLSAndPrivateRoundcube(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root local-peer backup is exercised in the isolated Linux container")
	}
	for _, path := range []string{"/usr/sbin/runuser", "/usr/bin/pg_dump", "/usr/bin/pg_restore"} {
		if _, err := os.Stat(path); err != nil {
			t.Skip("fixed backup tools unavailable")
		}
	}
	ctx := context.Background()
	source := testdb.New(t)
	uri, _ := url.Parse(source)
	query := uri.Query()
	query.Set("role", "urmail_migrate")
	uri.RawQuery = query.Encode()
	migrator, err := db.NewMigrator(uri.String())
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	migrator.Close()
	connection, err := pgx.Connect(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	_, err = connection.Exec(ctx, `CREATE TABLE public.backup_forced_probe(value text);ALTER TABLE public.backup_forced_probe OWNER TO urmail_platform;ALTER TABLE public.backup_forced_probe ENABLE ROW LEVEL SECURITY;ALTER TABLE public.backup_forced_probe FORCE ROW LEVEL SECURITY;INSERT INTO public.backup_forced_probe VALUES('forced-row-proof');CREATE SCHEMA IF NOT EXISTS roundcube AUTHORIZATION urmail_roundcube;CREATE TABLE roundcube.backup_private_probe(value text);ALTER TABLE roundcube.backup_private_probe OWNER TO urmail_roundcube;INSERT INTO roundcube.backup_private_probe VALUES('private-row-proof')`)
	if err != nil {
		t.Fatal(err)
	}
	uri, _ = url.Parse(source)
	uri.User = url.User("urmail_migrate")
	peer, err := backupPeerDatabaseURL(uri.String())
	if err != nil {
		t.Fatal(err)
	}
	command, err := backupDatabaseCommand(ctx, peer)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := backupDatabaseCommand(ctx, peer)
	if err != nil {
		t.Fatal(err)
	}
	restricted.Args = append(restricted.Args, "--role=urmail_migrate")
	if err = restricted.Run(); err == nil {
		t.Fatal("migration membership unexpectedly bypassed FORCE RLS")
	}
	raw, err := command.Output()
	if err != nil {
		t.Fatal("root peer database backup failed")
	}
	dump := filepath.Join(t.TempDir(), "root-peer.dump")
	if err = os.WriteFile(dump, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	restore := exec.CommandContext(ctx, "/usr/bin/pg_restore", "--data-only", "--file=-", "--", dump)
	decoded, err := restore.Output()
	if err != nil {
		t.Fatal("decode backup failed")
	}
	for _, fact := range []string{"public.backup_forced_probe", "roundcube.backup_private_probe", "forced-row-proof", "private-row-proof"} {
		if !bytes.Contains(decoded, []byte(fact)) {
			t.Fatal("complete backup omitted a required private fact")
		}
	}
	if installer := os.Getenv("URMAIL_TEST_INSTALLER_SOURCE"); installer != "" {
		sourceFile, readErr := os.ReadFile(installer)
		if readErr != nil {
			t.Fatal(readErr)
		}
		_, block, found := strings.Cut(string(sourceFile), "python3 - \"$BACKUP_DIR/urmaild.env\" \"$BACKUP_DIR/database.dump\" <<'PYCODE'\n")
		if !found {
			t.Fatal("installer backup block missing")
		}
		program, _, found := strings.Cut(block, "\nPYCODE")
		if !found {
			t.Fatal("installer backup block unterminated")
		}
		directory := t.TempDir()
		environment := filepath.Join(directory, "urmaild.env")
		if err = os.WriteFile(environment, []byte("URMAIL_DATABASE_URL_MIGRATE="+uri.String()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		installerDump := filepath.Join(directory, "installer.dump")
		python := exec.CommandContext(ctx, "/usr/bin/python3", "-", environment, installerDump)
		python.Stdin = strings.NewReader(program)
		if err = python.Run(); err != nil {
			t.Fatal("installer root-peer backup failed")
		}
		decoded, err = exec.CommandContext(ctx, "/usr/bin/pg_restore", "--data-only", "--file=-", "--", installerDump).Output()
		if err != nil || !bytes.Contains(decoded, []byte("forced-row-proof")) || !bytes.Contains(decoded, []byte("private-row-proof")) {
			t.Fatal("installer archive omitted protected facts")
		}
		t.Log("exact installer backup block contains forced-RLS and Roundcube private rows")
	}
	var platformDenied bool
	if err = connection.QueryRow(ctx, "SELECT NOT has_schema_privilege('urmail_platform','roundcube','USAGE')").Scan(&platformDenied); err != nil || !platformDenied {
		t.Fatal("runtime Roundcube boundary changed")
	}
}
