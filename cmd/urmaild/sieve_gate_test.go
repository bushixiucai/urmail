package main

import (
	"os/user"
	"strconv"
	"strings"
	"testing"
)

// TestResolveMailUIDUsesConfiguredAccount proves the gate derives its peer uid
// from the configured account name, never from a built-in literal, and refuses
// a name no account carries.
func TestResolveMailUIDUsesConfiguredAccount(t *testing.T) {
	account, err := user.Lookup("mail")
	if err != nil {
		t.Skipf("this host has no mail account: %v", err)
	}
	want, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveMailUID("mail")
	if err != nil {
		t.Fatal(err)
	}
	if uint64(got) != want {
		t.Fatalf("mail uid=%d, want %d", got, want)
	}

	if _, err = resolveMailUID("urmail-identity-that-does-not-exist"); err == nil || !strings.Contains(err.Error(), "urmail-identity-that-does-not-exist") {
		t.Fatalf("unknown identity accepted: %v", err)
	}
}
