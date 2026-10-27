package main

import (
	"strings"
	"testing"
)

func TestRestoreRejectsMissingManifestBeforeWork(t *testing.T) {
	err := runRestore(t.Context(), []string{"--dump", "database.dump", "--force"})
	if err == nil || !strings.Contains(err.Error(), "--manifest") {
		t.Fatal(err)
	}
}
