package selfhost

import (
	"context"
	"errors"
	"testing"
)

func TestPreflightRejectsMissingDatabase(t *testing.T) {
	err := Preflight(Input{AdminEmail: "a@example.test", Password: "long-enough-password", Hostname: "mail.example.test"})
	if !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
}

func TestPreflightAcceptsCompleteInput(t *testing.T) {
	err := Preflight(Input{DatabaseURL: "postgres://urmail@db/urmail", AdminEmail: "a@example.test", Password: "long-enough-password", Hostname: "mail.example.test"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPreflightConnectedRejectsDialFailure(t *testing.T) {
	in := Input{DatabaseURL: "postgres://urmail@db/urmail", AdminEmail: "a@example.test", Password: "long-enough-password", Hostname: "mail.example.test"}
	err := PreflightConnected(context.Background(), in, func(context.Context, string) error { return errors.New("down") })
	if !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
}

func TestPreflightDatabaseURISchemesAndAuthority(t *testing.T) {
	for _, uri := range []string{"postgres://urmail@db/urmail", "postgresql://urmail_platform:fixture@127.0.0.1:5432/urmail?sslmode=disable"} {
		in := Input{DatabaseURL: uri, AdminEmail: "admin@example.test", Password: "long-enough-password", Hostname: "box.example.test"}
		if err := Preflight(in); err != nil {
			t.Fatalf("valid database URI rejected: %v", err)
		}
	}
	for _, uri := range []string{"", "http://db/urmail", "postgresql:///urmail", "postgresql://user@/urmail", "postgres://db", "postgres://db/", "postgres://db:0/urmail", "postgres://db:65536/urmail", "postgres://db:not-a-port/urmail", "postgres://db/urmail#ignored"} {
		in := Input{DatabaseURL: uri, AdminEmail: "admin@example.test", Password: "long-enough-password", Hostname: "box.example.test"}
		if err := Preflight(in); !errors.Is(err, ErrNotReady) {
			t.Fatalf("invalid database URI accepted: %q", uri)
		}
	}
}
