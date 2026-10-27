package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/store"
)

func mailboxRestoreFixture(t *testing.T) (string, string, string, store.PlatformMailboxRestoreIdentityRow) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mailbox := filepath.Join(root, "mail", "example.test", "alice")
	for _, folder := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(mailbox, folder), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mailbox, "cur", "live-message"), []byte("From: live@example.test\r\n\r\nLive body\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "mail.tar.gz")
	buildTarGz(t, archive, map[string]string{
		"var/mail/mail/mailboxes/example.test/alice/cur/old:2,S":     "From: old@example.test\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nOld body\r\n--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\nAQIDBA==\r\n--b--\r\n",
		"var/mail/mail/mailboxes/example.test/alice/.Sent/new/sent":  "From: sent@example.test\r\n\r\nSent body",
		"var/mail/mail/mailboxes/example.test/alice/dovecot-uidlist": "untrusted old UID state",
		"var/mail/mail/mailboxes/example.test/bob/new/other":         "Other mailbox private body",
	})
	manifest := filepath.Join(root, "manifest.json")
	if err := writeBackupManifest(manifest, backupauto.ScopeComplete, true, []string{archive}); err != nil {
		t.Fatal(err)
	}
	return mailbox, archive, manifest, store.PlatformMailboxRestoreIdentityRow{TenantID: 10, ID: 20, Address: "alice@example.test", LocalPart: "alice", Fqdn: "example.test", QuotaBytes: 1 << 20}
}

func TestMailboxRestoreReviewApplyRollbackIsolation(t *testing.T) {
	target, archive, manifest, id := mailboxRestoreFixture(t)
	p, err := reviewMailboxRestore(manifest, archive, target, "var/mail/mail/mailboxes/example.test/"+id.LocalPart, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Messages != 2 || p.ReviewHash == "" {
		t.Fatalf("bad plan %+v", p)
	}
	before, _ := os.ReadFile(filepath.Join(target, "cur", "live-message"))
	backupParent := t.TempDir()
	if err = os.Chmod(backupParent, 0o700); err != nil {
		t.Fatal(err)
	}
	// os.MkdirTemp can reside under an intentionally shared OS temporary
	// directory; choose the private ancestry validator's test convention.
	rollback := filepath.Join(backupParent, "rollback")
	if err = applyMailboxRestoreFiles(p, archive, target, rollback, func(directory string) error {
		info, e := os.Stat(directory)
		if e != nil {
			return e
		}
		if info.Mode().Perm() != 0o700 {
			return errors.New("not private")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(target, "cur", "live-message"))
	if string(before) != string(after) {
		t.Fatal("current mailbox was overwritten")
	}
	retained, _ := os.ReadFile(filepath.Join(rollback, "mailbox", "cur", "live-message"))
	if string(before) != string(retained) {
		t.Fatal("rollback copy not byte faithful")
	}
	entries, err := os.ReadDir(filepath.Join(target, p.Folder, "new"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("import: %v %v", entries, err)
	}
	var restored strings.Builder
	for _, e := range entries {
		raw, readErr := os.ReadFile(filepath.Join(target, p.Folder, "new", e.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		restored.Write(raw)
	}
	if !strings.Contains(restored.String(), "AQIDBA==") || !strings.Contains(restored.String(), "Old body") || strings.Contains(restored.String(), "Other mailbox") {
		t.Fatal("body/attachment or mailbox isolation failed")
	}
	if _, err = reviewMailboxRestore(manifest, archive, target, "var/mail/mail/mailboxes/example.test/"+id.LocalPart, id); err == nil {
		t.Fatal("duplicate import was accepted")
	}
	if _, err = os.Stat(filepath.Join(target, p.Folder, "dovecot-uidlist")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("stale UID state imported")
	}
}

func TestMailboxRestoreRejectsChangedManifestQuotaAndLinks(t *testing.T) {
	t.Run("checksum", func(t *testing.T) {
		target, archive, manifest, id := mailboxRestoreFixture(t)
		if err := os.WriteFile(archive, []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := reviewMailboxRestore(manifest, archive, target, "var/mail/mail/mailboxes/example.test/"+id.LocalPart, id); err == nil {
			t.Fatal("tampered mail accepted")
		}
	})
	t.Run("quota", func(t *testing.T) {
		target, archive, manifest, id := mailboxRestoreFixture(t)
		id.QuotaBytes = 40
		if _, err := reviewMailboxRestore(manifest, archive, target, "var/mail/mail/mailboxes/example.test/"+id.LocalPart, id); err == nil {
			t.Fatal("over quota accepted")
		}
	})
	t.Run("other identity", func(t *testing.T) {
		target, archive, manifest, id := mailboxRestoreFixture(t)
		id.LocalPart = "charlie"
		id.Address = "charlie@example.test"
		if _, err := reviewMailboxRestore(manifest, archive, target, "var/mail/mail/mailboxes/example.test/"+id.LocalPart, id); err == nil {
			t.Fatal("wrong storage identity accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target, archive, manifest, id := mailboxRestoreFixture(t)
		if err := os.Symlink("/etc/passwd", filepath.Join(target, "new", "linked")); err != nil {
			t.Fatal(err)
		}
		if _, err := reviewMailboxRestore(manifest, archive, target, "var/mail/mail/mailboxes/example.test/"+id.LocalPart, id); err == nil {
			t.Fatal("symlink accepted")
		}
	})
}
