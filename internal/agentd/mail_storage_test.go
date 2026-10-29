package agentd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func storageTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMailStorageFreshAndLegacyIdempotent(t *testing.T) {
	root := storageTestRoot(t)
	// StorageRoot itself remains owned and protected exactly as the operator
	// configured it. Only the new mail parents receive mail ownership/mode.
	if err := os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if err := ensureMailStorageTree(root, uid, gid); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"mail", "mail/mailboxes"} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		owner, group := fileOwner(info)
		if !info.IsDir() || info.Mode().Perm() != 0750 || owner != uid || group != gid {
			t.Fatalf("fresh storage permissions: %s %v %d:%d", rel, info.Mode(), owner, group)
		}
	}
	legacy := filepath.Join(root, "mail", "mailboxes")
	if err := os.Chmod(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	message := filepath.Join(legacy, "existing-message")
	if err := os.WriteFile(message, []byte("existing customer mail"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureMailStorageTree(root, uid, gid); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(root)
	if info.Mode().Perm() != 0711 {
		t.Fatal("StorageRoot mode changed")
	}
	info, _ = os.Stat(legacy)
	if info.Mode().Perm() != 0755 {
		t.Fatal("legacy mail directory mode changed")
	}
	body, err := os.ReadFile(message)
	if err != nil || string(body) != "existing customer mail" {
		t.Fatal("legacy mail changed", err)
	}
}

func TestMailStorageRejectsSymlinksAndFiles(t *testing.T) {
	for _, component := range []string{"storage", "mail", "mailboxes", "ancestor"} {
		t.Run(component, func(t *testing.T) {
			root := storageTestRoot(t)
			outside := storageTestRoot(t)
			if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			storage := filepath.Join(root, "storage")
			link := storage
			switch component {
			case "mail":
				if err := os.Mkdir(storage, 0755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(storage, "mail")
			case "mailboxes":
				if err := os.MkdirAll(filepath.Join(storage, "mail"), 0755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(storage, "mail", "mailboxes")
			case "ancestor":
				storage = filepath.Join(root, "link", "nested")
				link = filepath.Join(root, "link")
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			if err := ensureMailStorageTree(storage, os.Geteuid(), os.Getegid()); !errors.Is(err, ErrRejected) {
				t.Fatal("symlink accepted", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "mail")); !os.IsNotExist(err) {
				t.Fatal("symlink destination was provisioned", err)
			}
			body, _ := os.ReadFile(filepath.Join(outside, "sentinel"))
			if string(body) != "untouched" {
				t.Fatal("outside content changed")
			}
		})
	}
	root := storageTestRoot(t)
	if err := os.WriteFile(filepath.Join(root, "mail"), []byte("existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureMailStorageTree(root, os.Geteuid(), os.Getegid()); !errors.Is(err, ErrRejected) {
		t.Fatal("non-directory accepted", err)
	}
}

func TestMailStorageRejectsWritableExistingDirectoryWithoutRepair(t *testing.T) {
	root := storageTestRoot(t)
	final := filepath.Join(root, "mail", "mailboxes")
	if err := os.MkdirAll(final, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(final, 0777); err != nil {
		t.Fatal(err)
	}
	if err := ensureMailStorageTree(root, os.Geteuid(), os.Getegid()); !errors.Is(err, ErrRejected) {
		t.Fatal("world-writable existing storage accepted", err)
	}
	info, _ := os.Stat(final)
	if info.Mode().Perm() != 0777 {
		t.Fatal("existing permissions silently changed")
	}
	for _, invalid := range []string{"", "/", "relative", "/tmp/../unsafe"} {
		if err := ensureMailStorageTree(invalid, os.Geteuid(), os.Getegid()); !errors.Is(err, ErrRejected) {
			t.Fatal("unbounded storage path accepted", invalid, err)
		}
	}
}

func TestApplyProvisionsMailStorageBeforeReadyFastPath(t *testing.T) {
	m := testManager(t)
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(m.target(m.cfg.StorageRoot), "mail", "mailboxes")
	if err := os.Remove(final); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal("same generation failed to repair missing empty parent", err)
	}
	if info, err := os.Stat(final); err != nil || !info.IsDir() {
		t.Fatal("ready without mail storage", err)
	}
	if err := os.Remove(final); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(storageTestRoot(t), final); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Apply(emptyApply()); !errors.Is(err, ErrRejected) {
		t.Fatal("same-generation symlink storage accepted", err)
	}
	if m.status.Ready || m.status.Step != "mail-storage" {
		t.Fatalf("storage refusal hidden in readiness: %+v", m.status)
	}
}
