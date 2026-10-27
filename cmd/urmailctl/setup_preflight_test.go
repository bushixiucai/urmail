package main

import (
	"os"
	"testing"
)

func TestSetupStopsBeforeDatabaseWhenHostnameMissing(t *testing.T) {
	t.Setenv("URMAIL_DATABASE_URL_PLATFORM", "postgres://urmail@db/urmail")
	t.Setenv("URMAIL_MYHOSTNAME", "")
	err := runSetup(t.Context(), []string{"--email", "admin@example.test", "--password", "long-enough-password"})
	if err == nil || os.Getenv("URMAIL_MYHOSTNAME") != "" {
		t.Fatal(err)
	}
}
