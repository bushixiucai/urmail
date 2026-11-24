package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// readinessAnchorPath is the deployment-controlled trusted anchor for signed
// readiness attestations. The normal product build has no caller override.
const readinessAnchorPath = "/etc/urmail/readiness-attest.pub"

// readinessAnchorLocation is overridden only by the tagged test-injection
// build; the normal binary always uses the fixed path above.
func readinessAnchorLocation() string { return readinessAnchorPath }

// loadTrustedReadinessAnchor validates the fixed anchor's ancestry and file
// identity (root-owned, not group/world writable, single regular link) before
// reading the pinned ed25519 public key.
func loadTrustedReadinessAnchor() (ed25519.PublicKey, error) {
	return loadAnchorFile(readinessAnchorLocation(), true)
}

// loadAnchorFile parses a hex or base64 ed25519 public key after structural
// validation. requireRoot enforces the production ownership rules.
func loadAnchorFile(path string, requireRoot bool) (ed25519.PublicKey, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("readiness anchor path is not absolute")
	}
	if err := validateAnchorAncestry(filepath.Dir(path), requireRoot); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("readiness anchor is missing or not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("readiness anchor has no usable stat identity")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("readiness anchor is group or world writable")
	}
	if stat.Mode&(syscall.S_ISUID|syscall.S_ISGID|syscall.S_ISVTX) != 0 {
		return nil, errors.New("readiness anchor has disallowed special permission bits")
	}
	if stat.Nlink != 1 {
		return nil, errors.New("readiness anchor must have exactly one link")
	}
	if requireRoot {
		if stat.Uid != 0 {
			return nil, errors.New("readiness anchor must be owned by root")
		}
	} else if stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("test readiness anchor must be owned by the current user")
	}
	raw, err := readBoundedRegular(path, 4096)
	if err != nil {
		return nil, fmt.Errorf("read readiness anchor: %w", err)
	}
	text := strings.TrimSpace(string(raw))
	if decoded, err := hex.DecodeString(text); err == nil && len(decoded) == ed25519.PublicKeySize {
		return ed25519.PublicKey(decoded), nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil && len(decoded) == ed25519.PublicKeySize {
		return ed25519.PublicKey(decoded), nil
	}
	return nil, errors.New("readiness anchor is not a hex or base64 ed25519 public key")
}

// validateAnchorAncestry rejects symlinked, group/world-writable or (in the
// production path) non-root ancestors so a caller cannot redirect the anchor.
func validateAnchorAncestry(dir string, requireRoot bool) error {
	dirs := []string{dir}
	if requireRoot {
		for {
			parent := filepath.Dir(dirs[len(dirs)-1])
			if parent == dirs[len(dirs)-1] {
				break
			}
			dirs = append(dirs, parent)
		}
	}
	for _, current := range dirs {
		info, err := os.Lstat(current)
		if err != nil || !info.Mode().IsDir() {
			return fmt.Errorf("readiness anchor ancestor %q is missing, symlinked or not a directory", current)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("readiness anchor ancestor %q has no usable identity", current)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("readiness anchor ancestor %q is group or world writable", current)
		}
		if requireRoot && stat.Uid != 0 {
			return fmt.Errorf("readiness anchor ancestor %q is not root owned", current)
		}
	}
	return nil
}
