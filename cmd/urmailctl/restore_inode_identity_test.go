package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreComponentRejectsRelativeSiblingSwap(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"original", "victim"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	expected, err := root.Lstat("original")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("original", "saved"); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("victim", "original"); err != nil {
		t.Fatal(err)
	}
	// Standard-library OpenRoot follows this relative link, so inode identity is essential.
	followed, err := root.OpenRoot("original")
	if err != nil {
		t.Fatal(err)
	}
	_ = followed.Close()
	opened, err := openVerifiedRestoreComponent(root, "original", expected)
	if err == nil {
		_ = opened.Close()
		t.Fatal("accepted swapped sibling directory")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "victim"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("victim modified: entries=%v err=%v", entries, err)
	}
}
