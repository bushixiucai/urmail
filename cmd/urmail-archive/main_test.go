package main

import "testing"

func TestFallbackMessageIDUsesContent(t *testing.T) {
	a, b := []byte("same-size-a"), []byte("same-size-b")
	if fallbackMessageID(a) == fallbackMessageID(b) {
		t.Fatal("different messages shared a fallback ID")
	}
	again := append([]byte(nil), a...)
	if fallbackMessageID(a) != fallbackMessageID(again) {
		t.Fatal("fallback ID is unstable")
	}
}
