package agentd

import (
	"fmt"
	"os"
	"path/filepath"
)

// ValidateRootEnvironment refuses configuration writable by the RPC caller.
func ValidateRootEnvironment(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("read root environment: %w", err)
	}
	uid, _ := fileOwner(info)
	if !info.Mode().IsRegular() || uid != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("root environment must be a root-owned private regular file")
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		uid, _ := fileOwner(info)
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || uid != 0 || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("root environment parent must not be writable by unprivileged users")
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return nil
}
