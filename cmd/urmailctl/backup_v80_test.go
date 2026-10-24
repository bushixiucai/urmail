package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func v80Fixture(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		full := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range v80Roots {
		if root.Required {
			if root.Directory {
				if err := os.MkdirAll(filepath.Join(base, root.Path), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				write(root.Path, "placeholder")
			}
		}
	}
	for name, kind := range v80RequiredLayout {
		switch kind {
		case "directory":
			if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
				t.Fatal(err)
			}
		case "file", "persistent_file":
			write(name, "placeholder")
		}
	}
	write("etc/default/postgrey", "POSTGREY_OPTS=\"--inet=127.0.0.1:10023 --delay=0\"\n")
	write("etc/opendkim/KeyTable", "preserved signing table\n")
	write("etc/systemd/system/mailinabox.service", "[Service]\nExecStart=/safe/preserved-command\n")
	write("var/spool/cron/crontabs/root", "0 2 * * * /safe/preserved-job\n")
	write("etc/mailinabox.conf", "STORAGE_ROOT=/home/user-data\nPRIVATE_CONFIG=secret-never-public\n")
	write("home/user-data/owncloud/config.php", "<?php $CONFIG = array('datadirectory'=>'/home/user-data/owncloud','dbtype'=>'sqlite3','dbname'=>'owncloud','passwordsalt'=>'secret-never-public');")
	write("usr/local/lib/roundcubemail/config/config.inc.php", "<?php $config['db_dsnw']='sqlite:////home/user-data/mail/roundcube/roundcube.sqlite?mode=0640'; $config['des_key']='secret-never-public';")
	write("etc/php/8.4/fpm/pool.d/mailinabox.conf", "[mailinabox]\nlisten = /run/php/mailinabox-fpm.sock\n")
	for _, name := range []string{"home/user-data/mail/users.sqlite", "home/user-data/mail/roundcube/roundcube.sqlite", "home/user-data/owncloud/owncloud.db"} {
		write(name, "SQLite format 3\x00private-source-data")
	}
	write("home/user-data/mail/mailboxes/source.test/private-user/cur/message", "From: private-user@source.test\n\nsecret-never-public\n")
	write("home/user-data/mail/sieve/source.test/account/active.sieve", "keep;\n")
	write("var/lib/z-push/private-device-state", "secret-never-public-device")
	write("usr/local/lib/owncloud/index.php", "<?php // preserved application code, never executed")
	write("usr/local/lib/z-push/index.php", "<?php // preserved application code, never executed")
	link := func(name, target string) {
		t.Helper()
		full := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}
	write("usr/share/postfix/makedefs.out", "preserved vendor definitions\n")
	write("etc/ssl/private/ssl-cert-snakeoil.key", "private fixture fallback key\n")
	write("etc/ssl/certs/ssl-cert-snakeoil.pem", "fixture fallback certificate\n")
	link("etc/postfix/makedefs.out", "/usr/share/postfix/makedefs.out")
	link("etc/dovecot/private/dovecot.key", "/etc/ssl/private/ssl-cert-snakeoil.key")
	link("etc/dovecot/private/dovecot.pem", "/etc/ssl/certs/ssl-cert-snakeoil.pem")
	link("usr/local/lib/owncloud/config/config.php", "/home/user-data/owncloud/config.php")
	link("usr/local/lib/roundcubemail/temp", "/var/tmp/roundcubemail")
	link("usr/local/lib/roundcubemail/logs", "/var/log/roundcubemail")
	link("home/user-data/mail/sieve/source.test/account.sieve", "account/active.sieve")
	return base
}

func TestV80ProfilePreservesExactDataAndLegalLinksWithoutActivatingSecrets(t *testing.T) {
	base := v80Fixture(t)
	statePath := filepath.Join(base, "var/lib/z-push/private-device-state")
	if err := os.Chmod(statePath, 0o640); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2020, 2, 3, 4, 5, 6, 123456789, time.UTC)
	if err := os.Chtimes(statePath, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(statePath, 12345, 23456); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := collectV80Inventory(base)
	if err != nil || len(inventory.Blockers) != 0 || len(inventory.Excluded) != 2 || inventory.Bytes == 0 {
		t.Fatalf("invalid synthetic profile: blockers=%v err=%v", inventory.Blockers, err)
	}
	for _, name := range []string{"etc/default/postgrey", "etc/opendkim/KeyTable", "etc/systemd/system/mailinabox.service", "var/spool/cron/crontabs/root"} {
		found := false
		for _, member := range inventory.Members {
			if member.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("protected legacy runtime configuration was omitted")
		}
	}
	foundMetadata := false
	for _, member := range inventory.Members {
		if member.Name == "var/lib/z-push/private-device-state" {
			foundMetadata = true
			if member.Mode != 0o640 || !member.ModifiedAt.Equal(stamp) {
				t.Fatal("persistent device mode or nanosecond mtime was lost")
			}
			if os.Geteuid() == 0 && (member.UID != 12345 || member.GID != 23456) {
				t.Fatal("persistent device numeric ownership was lost")
			}
		}
	}
	if !foundMetadata {
		t.Fatal("device state was not inventoried")
	}
	before, err := os.Readlink(filepath.Join(base, "usr/local/lib/owncloud/config/config.php"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	manifestPath, err := createV80Bundle(context.Background(), base, out, inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored v80Manifest
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(out, stored.Artifact.Name)
	verified, err := verifyV80Bundle(context.Background(), manifestPath, archive)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Profile != v80Profile || verified.Scope != "legacy-file-profile" || verified.RestoreApplyAvailable || verified.SQLiteIntegrityVerified || len(verified.Members) != len(inventory.Members) {
		t.Fatal("profile overstated restore readiness")
	}
	after, err := os.Readlink(filepath.Join(base, "usr/local/lib/owncloud/config/config.php"))
	if err != nil || before != after {
		t.Fatal("backup followed or rewrote the source symlink")
	}
	for _, name := range []string{manifestPath, archive} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("private backup artifact permissions are incorrect")
		}
	}
	if err = verifyBackupManifest(manifestPath, []string{archive}, false); err == nil {
		t.Fatal("legacy file profile was mistaken for a PostgreSQL backup")
	}
	if err = rejectV80BundleAsDatabase(archive); err == nil {
		t.Fatal("gzip legacy bundle was accepted as a PostgreSQL dump")
	}
	var output bytes.Buffer
	if err = runV80Restore(context.Background(), []string{"--profile", v80Profile, "--apply"}, &output); err == nil || output.Len() != 0 {
		t.Fatal("legacy profile restore apply must remain unavailable")
	}
	// Even after both independent hashes are refreshed, missing persistent layout
	// is not made complete by a valid JSON wrapper.
	stored.Members = stored.Members[1:]
	stored.LayoutSHA256 = v80LayoutDigest(stored.Members, stored.Excluded)
	damaged, _ := json.Marshal(stored)
	if err = os.WriteFile(manifestPath, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = verifyV80Bundle(context.Background(), manifestPath, archive); err == nil {
		t.Fatal("incomplete profile layout was accepted")
	}
}

func TestV80ProfileRejectsEscapesChangesAndUnrepresentedDatastores(t *testing.T) {
	t.Run("external-link", func(t *testing.T) {
		base := v80Fixture(t)
		if err := os.Symlink("/etc/passwd", filepath.Join(base, "usr/local/lib/owncloud/escape")); err != nil {
			t.Fatal(err)
		}
		inventory, err := collectV80Inventory(base)
		if err != nil || len(inventory.Blockers) == 0 {
			t.Fatal("escaping source link was accepted")
		}
	})
	t.Run("cache-name-is-not-an-escape", func(t *testing.T) {
		base := v80Fixture(t)
		name := filepath.Join(base, "usr/local/lib/roundcubemail/temp")
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/passwd", name); err != nil {
			t.Fatal(err)
		}
		inventory, err := collectV80Inventory(base)
		if err != nil || len(inventory.Blockers) == 0 {
			t.Fatal("cache link exception widened to an arbitrary target")
		}
	})
	t.Run("source-change", func(t *testing.T) {
		base := v80Fixture(t)
		inventory, err := collectV80Inventory(base)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(base, "var/lib/z-push/private-device-state"), []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err = createV80Bundle(context.Background(), base, t.TempDir(), inventory, false); err == nil {
			t.Fatal("changed source bypassed the reviewed layout")
		}
	})
	t.Run("external-nextcloud-db", func(t *testing.T) {
		base := v80Fixture(t)
		name := filepath.Join(base, "home/user-data/owncloud/config.php")
		if err := os.WriteFile(name, []byte("<?php $CONFIG=['dbtype'=>'pgsql','dbname'=>'owncloud','datadirectory'=>'/other/private-data'];"), 0o600); err != nil {
			t.Fatal(err)
		}
		inventory, err := collectV80Inventory(base)
		if err != nil || len(inventory.Blockers) == 0 {
			t.Fatal("unpreserved external datastore was accepted")
		}
	})
}

func TestV80ProfileCLIRequiresExplicitOfflineOptInAndRedactsOutput(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-only CLI is exercised in the isolated Linux test container")
	}
	base := v80Fixture(t)
	out := t.TempDir()
	args := []string{"--profile", v80Profile, "--source-tree", base, "--out-dir", out, "--preview"}
	var preview bytes.Buffer
	if err := runV80Backup(context.Background(), args, &preview); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview.String(), "secret-never-public") || strings.Contains(preview.String(), "private-user") {
		t.Fatal("preview exposed secret material or mailbox paths")
	}
	var report struct {
		Layout string `json:"layout_preview_sha256"`
	}
	if err := json.Unmarshal(preview.Bytes(), &report); err != nil || len(report.Layout) != 64 {
		t.Fatal("layout preview binding missing")
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 0 {
		t.Fatal("preview created an artifact")
	}
	var output bytes.Buffer
	missing := make([]string, 0, 13)
	missing = append(missing, "--profile", v80Profile, "--source-tree", base, "--out-dir", out, "--apply")
	if err = runV80Backup(context.Background(), missing, &output); err == nil {
		t.Fatal("missing offline confirmation did not block bundle creation")
	}
	apply := append(append([]string{}, missing...), "--maintenance", "--include-mail", "--offline-source-confirmed", "--expected-preview", report.Layout)
	if err = runV80Backup(context.Background(), apply, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret-never-public") || strings.Contains(output.String(), "private-user") {
		t.Fatal("bundle output exposed source values")
	}
	var created struct {
		Manifest string `json:"manifest"`
	}
	if err = json.Unmarshal(output.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(created.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var manifest v80Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var restored bytes.Buffer
	if err = runV80Restore(context.Background(), []string{"--profile", v80Profile, "--preview", "--manifest", created.Manifest, "--archive", filepath.Join(out, manifest.Artifact.Name)}, &restored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(restored.String(), "secret-never-public") || strings.Contains(restored.String(), "private-user") {
		t.Fatal("restore preview exposed source values")
	}
}

func TestV80ProfileWriterProofRejectsActiveOrUnknownRequiredUnits(t *testing.T) {
	calls := []string{}
	if err := verifyV80WriterStates(func(unit string) (string, error) {
		calls = append(calls, unit)
		return "LoadState=loaded\nActiveState=inactive\n", nil
	}); err != nil || len(calls) != len(v80WriterUnits) {
		t.Fatal("inactive writer inventory was not checked")
	}
	for _, state := range []string{"LoadState=loaded\nActiveState=active\n", "LoadState=not-found\nActiveState=inactive\n", "LoadState=loaded\nActiveState=activating\n"} {
		if err := verifyV80WriterStates(func(string) (string, error) { return state, nil }); err == nil {
			t.Fatal("live or unidentified writer was treated as an offline snapshot")
		}
	}
	for _, args := range [][]string{{"--profile", "unknown"}, {"--profile", v80Profile, "--profile", v80Profile}, {"--preview"}, {"--source-tree", "/private/tree"}} {
		if _, _, err := selectedFileProfile(args); err == nil {
			t.Fatal("implicit or ambiguous profile selection was accepted")
		}
	}
}

func TestV80ProfileDestinationInsideSourceIsRejectedBeforeCreation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root profile CLI exercised in isolated Linux")
	}
	base := v80Fixture(t)
	inventory, err := collectV80Inventory(base)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(base, "home/user-data/mail/never-create-backups-here")
	args := []string{"--profile", v80Profile, "--source-tree", base, "--out-dir", out, "--apply", "--maintenance", "--include-mail", "--offline-source-confirmed", "--expected-preview", inventory.LayoutSHA256}
	var output bytes.Buffer
	if err = runV80Backup(context.Background(), args, &output); err == nil {
		t.Fatal("source-contained output directory was accepted")
	}
	if _, err = os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("rejected destination changed source directory layout")
	}
}
