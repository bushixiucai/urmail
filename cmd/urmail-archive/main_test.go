package main

import (
	"testing"

	archiveStore "github.com/bushixiucai/urmail/internal/archive"
)

func TestFallbackMessageIDUsesContent(t *testing.T) {
	a, b := []byte("same-size-a"), []byte("same-size-b")
	if archiveStore.ExtractMIME(a).MessageID == archiveStore.ExtractMIME(b).MessageID {
		t.Fatal("different messages shared a fallback ID")
	}
	again := append([]byte(nil), a...)
	if archiveStore.ExtractMIME(a).MessageID != archiveStore.ExtractMIME(again).MessageID {
		t.Fatal("fallback ID is unstable")
	}
}
