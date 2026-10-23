package agentd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// This file implements the filesystem half of the agent's privilege boundary.
//
// Every write must stay inside one of allowedWriteRoots:
//
//  1. the request path is checked lexically (absolute, cleaned, prefixed by an
//     allowed root);
//  2. the parent directory is created one component at a time, without ever
//     following an intermediate symlink;
//  3. the resulting parent is resolved with filepath.EvalSymlinks and must
//     still be inside the resolved allowed root;
//  4. the file is written to a temporary file in that parent, fsynced, renamed
//     over the destination, and the directory is fsynced.
//
// A symlink at the destination is replaced by the rename, never followed: the
// link target is left untouched.

// resolveAllowedPath performs the lexical half of the path check. It returns
// the cleaned path and the allowed root it belongs to.
func resolveAllowedPath(path string, roots []string) (string, string, error) {
	if path == "" {
		return "", "", fmt.Errorf("%w: empty path", ErrRejected)
	}
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("%w: path %q must be absolute", ErrRejected, path)
	}

	cleaned := filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(root)
		if cleaned == root {
			return "", "", fmt.Errorf("%w: path %q names an allowed root, not a file", ErrRejected, path)
		}
		if strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
			return cleaned, root, nil
		}
	}
	return "", "", fmt.Errorf("%w: path %q is outside the allowed roots", ErrRejected, path)
}

// writeFileAtomic writes content to path with mode under one of roots.
//
// The destination is replaced atomically. A symlink already present at the
// destination is replaced, not followed. Intermediate symlinks are rejected.
func writeFileAtomic(path string, content []byte, mode os.FileMode, roots []string) (string, error) {
	if mode > 0o777 {
		return "", fmt.Errorf("%w: mode %04o is not allowed", ErrRejected, mode)
	}

	cleaned, root, err := resolveAllowedPath(path, roots)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(root, cleaned)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path %q escapes its allowed root", ErrRejected, path)
	}

	// The allowed root is trusted and normally already exists. Create it when
	// the installer has not done so yet, then work only with its resolved form.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("%w: create allowed root %s: %w", ErrFailed, root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve allowed root %s: %w", ErrFailed, root, err)
	}

	resolvedTarget := filepath.Join(resolvedRoot, rel)
	dir := filepath.Dir(resolvedTarget)
	if err := ensureDirNoSymlinks(resolvedRoot, dir); err != nil {
		return "", err
	}

	// Re-resolve the parent after creating it and verify it is still beneath
	// the real allowed root. This catches a symlink anywhere below the root
	// even when a path component existed before the lexical check ran.
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("%w: resolve parent directory %s: %w", ErrFailed, dir, err)
	}
	if !pathInside(resolvedDir, resolvedRoot) {
		return "", fmt.Errorf("%w: parent directory %s resolves outside the allowed root %s", ErrRejected, dir, resolvedRoot)
	}

	if info, err := os.Lstat(resolvedTarget); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("%w: destination %s is a directory", ErrRejected, cleaned)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect destination %s: %w", ErrFailed, cleaned, err)
	}

	tmp, err := os.CreateTemp(resolvedDir, ".urmail-write-*")
	if err != nil {
		return "", fmt.Errorf("%w: create temporary file in %s: %w", ErrFailed, resolvedDir, err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		return "", fmt.Errorf("%w: write temporary file: %w", ErrFailed, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return "", fmt.Errorf("%w: set mode on temporary file: %w", ErrFailed, err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("%w: sync temporary file: %w", ErrFailed, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("%w: close temporary file: %w", ErrFailed, err)
	}

	// rename(2) never follows a symlink in the final component: an attacker
	// supplied symlink at the destination is replaced by the new file.
	if err := os.Rename(tmpName, resolvedTarget); err != nil {
		return "", fmt.Errorf("%w: replace %s: %w", ErrFailed, cleaned, err)
	}
	cleanup = false

	if err := syncDir(resolvedDir); err != nil {
		return "", fmt.Errorf("%w: sync directory %s: %w", ErrFailed, resolvedDir, err)
	}
	return cleaned, nil
}

// ensureDirNoSymlinks creates dir below root one component at a time. Any
// existing symlink component is rejected instead of followed. dir must be a
// lexical descendant of root and root must already be resolved (symlink-free).
func ensureDirNoSymlinks(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return fmt.Errorf("%w: cannot make %s relative to %s: %w", ErrRejected, dir, root, err)
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: directory %s escapes the allowed root %s", ErrRejected, dir, root)
	}

	current := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)

		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%w: create directory %s: %w", ErrFailed, current, err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("%w: inspect directory %s: %w", ErrRejected, current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: intermediate path component %s is a symlink", ErrRejected, current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: path component %s is not a directory", ErrRejected, current)
		}
	}
	return nil
}

// pathInside reports whether path is root or below root. Both must be cleaned
// absolute paths; callers use paths returned by filepath.EvalSymlinks.
func pathInside(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// syncDir fsyncs a directory so a rename is durable. Directory fsync is not
// supported on every platform (Darwin returns EINVAL); those errors are
// treated as best effort there, but a real I/O error is reported.
func syncDir(dir string) error {
	handle, err := os.Open(dir) // #nosec G304 -- fsync the previously validated managed parent directory.
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()

	if err := handle.Sync(); err != nil {
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
			return nil
		}
		return err
	}
	return nil
}
