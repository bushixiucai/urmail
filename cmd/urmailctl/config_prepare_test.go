package main

import (
	"context"
	"strings"
	"testing"
)

func TestConfigPrepareRejectsBeforeAnyPrivilegedWork(t *testing.T) {
	// The privilege check is intentionally not overridden by URMAIL_ALLOW_TEST_USER.
	t.Setenv("URMAIL_ALLOW_TEST_USER", "1")
	err := runConfigPrepare(context.Background(), []string{"--force"})
	if err == nil || (!strings.Contains(err.Error(), "requires root") && !strings.Contains(err.Error(), "usage:")) {
		t.Fatalf("unexpected prepare argument result: %v", err)
	}
}
