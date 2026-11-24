//go:build urmailreadinesstest

package main

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
)

// Test-injection build only: a non-root process that explicitly sets the
// marker may point verification at an isolated anchor file.
func readinessVerifyAnchor(callerPath string) (ed25519.PublicKey, error) {
	root := os.Getenv("URMAIL_READINESS_TEST_ANCHOR")
	if os.Geteuid() == 0 || os.Getenv("URMAIL_ALLOW_TEST_USER") == "" || root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("readiness-attest verify: test anchor injection is not enabled")
	}
	if callerPath != "" && callerPath != root {
		return nil, errors.New("readiness-attest verify: caller anchor does not match the isolated test anchor")
	}
	return loadAnchorFile(root, false)
}
