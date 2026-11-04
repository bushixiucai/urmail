package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestInstallerPreparedHeldExitCode(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &installerExitError{cause: errors.New("prepared-held"), code: 2})
	if commandExitCode(err) != 2 {
		t.Fatal("lost prepared-held exit status")
	}
	if commandExitCode(errors.New("ordinary failure")) != 1 {
		t.Fatal("ordinary failures must stay nonzero")
	}
}
