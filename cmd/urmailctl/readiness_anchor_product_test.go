//go:build !urmailreadinesstest

package main

import "testing"

func TestNormalBuildRejectsCallerAnchor(t *testing.T) {
	if _, err := readinessVerifyAnchor("/tmp/caller.pub"); err == nil || !contains(err.Error(), "fixed") {
		t.Fatalf("caller anchor accepted: %v", err)
	}
}
