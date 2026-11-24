//go:build !urmailreadinesstest

package main

import (
	"crypto/ed25519"
	"errors"
)

// readinessVerifyAnchor always loads the deployment-controlled fixed anchor.
// Any caller-supplied path is refused in the normal product build.
func readinessVerifyAnchor(callerPath string) (ed25519.PublicKey, error) {
	if callerPath != "" {
		return nil, errors.New("readiness-attest verify: a caller-supplied anchor is not accepted; the deployment anchor is fixed")
	}
	return loadTrustedReadinessAnchor()
}
