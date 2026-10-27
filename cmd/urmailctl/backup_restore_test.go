package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestQuotaStorageParserUsesCanonicalKiBAndRejectsUnknown(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
		bad  bool
	}{{`[{"type":"STORAGE","value":"7"}]`, 7168, false}, {`[{"type":"STORAGE","value":0}]`, 0, false}, {`[]`, 0, true}, {`[{"type":"MESSAGE","value":"7"}]`, 0, true}, {`[{"type":"STORAGE","value":"9223372036854775807"}]`, 0, true}, {`[{"type":"STORAGE","value":"-1"}]`, 0, true}} {
		got, err := parseQuotaStorage([]byte(tc.raw))
		if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
			t.Fatalf("parse %s: got=%d err=%v", tc.raw, got, err)
		}
	}
}
func TestBackupManifestChecksTamperAndScope(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "database.dump")
	manifest := filepath.Join(dir, "backup.json")
	if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupManifest(manifest, "metadata-only", false, []string{file}); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupManifest(manifest, []string{file}, false); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupManifest(manifest, nil, false); err == nil {
		t.Fatal("omitted manifest artifact accepted")
	}
	if err := verifyBackupManifest(manifest, []string{file}, true); err == nil {
		t.Fatal("metadata accepted as complete mail backup")
	}
	if err := os.WriteFile(file, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupManifest(manifest, []string{file}, false); err == nil {
		t.Fatal("modified backup accepted")
	}
}
func TestRestoreArchiveRejectsTraversalAndEscapingLinks(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		bad          bool
	}{{"etc/urmail/env", "", false}, {"../../etc/passwd", "", true}, {"etc/passwd", "", true}, {"var/mail/evil", "../../etc/passwd", true}, {"etc/letsencrypt/live/test/cert.pem", "../../archive/test/cert1.pem", false}} {
		t.Run(strings.ReplaceAll(tc.name, "/", "_"), func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "backup.tar.gz")
			f, err := os.Create(filename)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			header := &tar.Header{Name: tc.name, Mode: 0o600, Typeflag: tar.TypeReg}
			if tc.target != "" {
				header.Typeflag = tar.TypeSymlink
				header.Linkname = tc.target
			}
			if err = tw.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if err = tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err = gz.Close(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			if err = validateRestoreArchive(filename); (err != nil) != tc.bad {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

func TestMaildirQuotaLedgerPreservesSubKiBUsage(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
		bad   bool
	}{{"1000S\n500 1\n-100 -1\n", 400, false}, {"1000S\n", 0, false}, {"1000S\nbad\n", 0, true}, {"1000S\n-1 -1\n", 0, true}} {
		got, err := parseMaildirQuotaBytes(strings.NewReader(tc.input))
		if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
			t.Fatalf("ledger %q => %d %v", tc.input, got, err)
		}
	}
}

func TestLiveCertificateBackupRootAndMissingInput(t *testing.T) {
	if !managedArchiveName("var/lib/urmail-agent/certs/mail/box.test.key") {
		t.Fatal("live certificate consumer root not restorable")
	}
	missing := filepath.Join(t.TempDir(), "missing-live-certificate-root")
	if err := validateBackupInputs([]string{missing}); err == nil {
		t.Fatal("missing required certificate root accepted")
	}
}

func TestPostgresCommandsNeverExposePasswordsInArgv(t *testing.T) {
	for _, program := range []string{"pg_dump", "pg_restore"} {
		cmd, err := postgresCommand(context.Background(), program, "postgres://urmail_migrate:fixture-secret@127.0.0.1:55432/urmail?sslmode=disable", "--no-owner")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(cmd.Args, " "), "fixture-secret") {
			t.Fatal("password exposed in argv")
		}
		found := false
		for _, entry := range cmd.Env {
			if entry == "PGPASSWORD=fixture-secret" {
				found = true
			}
		}
		if !found {
			t.Fatal("child credential missing")
		}
	}
}
func TestRestoreKeepsTargetRoleCredentialsAndRestoresOtherSettings(t *testing.T) {
	cfg := &config.Config{DatabaseURL: "postgres://urmail_app:new-app@127.0.0.1/old", DatabaseURLPlatform: "postgres://urmail_platform:new-platform@127.0.0.1/old", DatabaseURLMigrate: "postgres://urmail_migrate:new-migrate@127.0.0.1/old", DatabaseURLDovecot: "postgres://urmail_dovecot:new-dovecot@127.0.0.1/old", RoundcubeBridgeKey: "target-private-bridge-key-that-must-survive"}
	values, err := targetDatabaseURLs(cfg, "postgres://urmail_migrate:new-migrate@127.0.0.1/restored")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("URMAIL_DATABASE_URL=postgres://urmail_app:old-source-secret@127.0.0.1/source\nURMAIL_MYHOSTNAME=source.example.test\n")
	merged := string(mergeTargetDatabaseURLs(raw, values))
	if strings.Contains(merged, "old-source-secret") || !strings.Contains(merged, "new-app@127.0.0.1/restored") || !strings.Contains(merged, "URMAIL_MYHOSTNAME=source.example.test") || !strings.Contains(merged, "URMAIL_ROUNDCUBE_BRIDGE_KEY=target-private-bridge-key-that-must-survive") {
		t.Fatal("source DB credentials replaced validated target credentials or other source settings were lost")
	}
}

func TestCompleteBackupRetainsExistingArchiveAndSpoolWhenDisabled(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"var/lib/urmail/archive", "var/lib/urmail/archive-spool"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := archiveBackupPaths(root, false)
	if err != nil || len(paths) != 2 {
		t.Fatalf("feature toggle omitted existing archive data: %v %v", paths, err)
	}
	if err = os.Remove(filepath.Join(root, "var/lib/urmail/archive-spool")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("archive", filepath.Join(root, "var/lib/urmail/archive-spool")); err != nil {
		t.Fatal(err)
	}
	if _, err = archiveBackupPaths(root, false); err == nil {
		t.Fatal("symlinked spool backup accepted")
	}
}
