package main

import (
	"context"
	"strings"
	"testing"
)

func TestRecoverPendingCommandIsRoutedAndRejectsUnexpectedArgs(t *testing.T) {
	err := run([]string{"recover-pending", "unexpected"})
	if err == nil {
		t.Fatal("recover-pending accepted unexpected arguments")
	}
	message := err.Error()
	if !strings.Contains(message, "recover-pending requires root") && !strings.Contains(message, "usage: recover-pending") {
		t.Fatalf("command was not routed to recover-pending handler: %v", err)
	}
}

func TestRecoverPendingHandlerRejectsUnexpectedArgs(t *testing.T) {
	err := runRecoverPendingOffline(context.Background(), []string{"unexpected"})
	if err == nil {
		t.Fatal("unexpected argument accepted")
	}
}
