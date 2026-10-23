// Package mail defines generated file security classifications.
package mail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretFile identifies every generated credential or private key.
func SecretFile(path string) bool {
	return filepath.Base(path) == "db.conf" || strings.HasSuffix(path, ".private") || strings.HasSuffix(path, ".key")
}

// ExportFiles exports a configuration without following symlinks. The export
// root is private because ordinary parent directories otherwise expose keys.
func ExportFiles(root string, files map[string]string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := privateDirectory(root); err != nil {
		return err
	}
	for path, content := range files {
		target := filepath.Join(root, strings.TrimPrefix(path, "/"))
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("unsafe export path")
		}
		current := root
		for _, part := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
			if part == "." {
				continue
			}
			current = filepath.Join(current, part)
			if err := privateDirectory(current); err != nil {
				return err
			}
		}
		if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("export target must be a regular file: %s", target)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		mode := os.FileMode(0644)
		if SecretFile(path) {
			mode = 0600
		}
		f, err := os.CreateTemp(filepath.Dir(target), ".urmail-export-*")
		if err != nil {
			return err
		}
		name := f.Name()
		if err = f.Chmod(mode); err == nil {
			_, err = f.WriteString(content)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, target)
		}
		_ = os.Remove(name)
		if err != nil {
			return err
		}
	}
	return nil
}

func privateDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("unsafe export directory: %s", path)
		}
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	} else {
		return err
	}
	// Reject symlink ancestors even when the final directory is regular.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != filepath.Clean(path) {
		return fmt.Errorf("symlink export directory: %s", path)
	}
	return os.Chmod(path, 0700)
}
