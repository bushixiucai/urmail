package agentd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"
)

func TestMailAccessBoundaryDisconnectsOnlyRegisteredExactUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reads := 0
	var calls [][]string
	snapshot := func(context.Context) ([]MailAccess, time.Time, error) {
		reads++
		return []MailAccess{{Address: "alice@example.test", Allowed: reads == 1}, {Address: "bob@example.test", Allowed: true}, {Address: "*@example.test", Allowed: false}}, time.Now().Add(time.Millisecond), nil
	}
	WatchMailAccess(ctx, snapshot, func(_ context.Context, command string, args ...string) error {
		calls = append(calls, append([]string{command}, args...))
		cancel()
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	want := [][]string{{"/usr/bin/doveadm", "kick", "--", "alice@example.test"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	if reads < 2 {
		t.Fatal("scheduled boundary was not queried")
	}
}

func TestMailAccessDatabaseFailureDisconnectsExistingWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reads := 0
	snapshot := func(context.Context) ([]MailAccess, time.Time, error) {
		reads++
		if reads > 1 {
			return nil, time.Time{}, errors.New("offline")
		}
		return []MailAccess{{Address: "alice@example.test", Allowed: true}}, time.Now().Add(time.Millisecond), nil
	}
	kicked := false
	WatchMailAccess(ctx, snapshot, func(_ context.Context, _ string, _ ...string) error { kicked = true; cancel(); return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !kicked {
		t.Fatal("database outage left writer connected")
	}
}

func TestKickAddressRejectsGlobAndArgumentInjection(t *testing.T) {
	for _, address := range []string{"*@example.test", "a?@example.test", "a@example.test\n", "a/../b@example.test", "x", "a@-bad.test"} {
		if safeKickAddress(address) {
			t.Errorf("accepted %q", address)
		}
	}
	for _, address := range []string{"-x@example.test", "alice+tag@example.test", "alice@example.test"} {
		if !safeKickAddress(address) {
			t.Errorf("rejected safe address %q", address)
		}
	}
}

func TestPasswordRevocationSurvivesWatcherRestartAndRetriesBeforeAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	row := MailAccess{Address: "alice@example.test", Allowed: true, TenantID: 7, MailboxID: 9, PasswordVersion: 3, PasswordRevokePending: true}
	kicks, acks := 0, 0
	watchMailAccess(ctx, func(context.Context) ([]MailAccess, time.Time, error) {
		return []MailAccess{row}, time.Now().Add(time.Millisecond), nil
	}, func(_ context.Context, command string, args ...string) error {
		kicks++
		if command != "/usr/bin/doveadm" || !reflect.DeepEqual(args, []string{"kick", "--", row.Address}) {
			t.Fatal("unexpected password revocation command")
		}
		if kicks == 1 {
			return errors.New("offline")
		}
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(_ context.Context, actual MailAccess) error {
		acks++
		if actual.PasswordVersion != 3 || actual.MailboxID != 9 || actual.TenantID != 7 {
			t.Fatal("credential acknowledgement lost exact binding")
		}
		if acks == 1 {
			return errors.New("database unavailable")
		}
		cancel()
		return nil
	})
	if kicks != 3 || acks != 2 {
		t.Fatalf("kick failure or ack failure lost durable work kicks=%d acks=%d", kicks, acks)
	}
}
