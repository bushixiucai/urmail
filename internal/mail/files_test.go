package mail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportProtectsAllSecretsAndRejectsSymlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"/etc/dovecot/conf.d/urmail/db.conf": "password=secret", "/etc/opendkim/keys/example.com/mail.private": "private key", "/etc/postfix/main.cf": "public"}
	if err := ExportFiles(root, files); err != nil {
		t.Fatal(err)
	}
	for path := range files {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0644)
		if SecretFile(path) {
			want = 0600
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s: %o", path, info.Mode().Perm())
		}
	}
	key := filepath.Join(root, "/etc/opendkim/keys/example.com/mail.private")
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ExportFiles(root, files); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(key)
	if info.Mode().Perm() != 0600 {
		t.Fatal("existing key permissions were not repaired")
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, key); err != nil {
		t.Fatal(err)
	}
	if err := ExportFiles(root, files); err == nil {
		t.Fatal("symlink target accepted")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "unchanged" {
		t.Fatal("symlink target modified")
	}
}
