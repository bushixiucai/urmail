package mail_test

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/mail"
)

// migrationStatuses is the status set accepted by the delivery_events table,
// copied verbatim from the delivery_events_status_check constraint in
// migrations/0027_onboarding_and_telemetry.up.sql.
var migrationStatuses = []string{
	"queued", "sent", "deferred", "bounced", "expired", "removed", "unknown",
}

func TestParsePostfixLogLine(t *testing.T) {
	t.Run("smtp delivered event", func(t *testing.T) {
		line := "Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mail.remote.example.com[93.184.216.34]:25, delay=1.4, delays=0.1/0.01/0.8/0.49, dsn=2.0.0, status=sent (250 2.0.0 Ok: queued as ABCD1234)"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.QueueID != "4S123456789" {
			t.Errorf("expected queue ID 4S123456789, got %s", ev.QueueID)
		}
		if ev.Recipient != "user@remote.example.com" {
			t.Errorf("expected recipient user@remote.example.com, got %s", ev.Recipient)
		}
		if ev.RecipientDomain != "remote.example.com" {
			t.Errorf("expected recipient domain remote.example.com, got %s", ev.RecipientDomain)
		}
		if ev.Status != mail.DeliveryStatusSent {
			t.Errorf("expected status sent, got %s", ev.Status)
		}
		if ev.DSN != "2.0.0" {
			t.Errorf("expected DSN 2.0.0, got %s", ev.DSN)
		}
		if ev.DelaySeconds != 1.4 {
			t.Errorf("expected delay 1.4, got %f", ev.DelaySeconds)
		}
		if ev.ResponseDetail != "250 2.0.0 Ok: queued as ABCD1234" {
			t.Errorf("expected response detail, got %q", ev.ResponseDetail)
		}
	})

	t.Run("smtp deferred event", func(t *testing.T) {
		line := "Oct 23 14:00:02 mail postfix/smtp[2345]: 4S123456789: to=<busy@example.org>, relay=none, delay=30, delays=0.1/0.01/30/0, dsn=4.4.1, status=deferred (connect to mail.example.org[93.184.216.34]:25: Connection timed out)"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Status != mail.DeliveryStatusDeferred {
			t.Errorf("expected status deferred, got %s", ev.Status)
		}
		if ev.DSN != "4.4.1" {
			t.Errorf("expected DSN 4.4.1, got %s", ev.DSN)
		}
		if ev.Relay != "none" {
			t.Errorf("expected relay none, got %s", ev.Relay)
		}
	})

	t.Run("smtp bounced event", func(t *testing.T) {
		line := "Oct 23 14:00:03 mail postfix/smtp[2345]: 4S123456789: to=<nosuchuser@example.org>, relay=mx.example.org[1.2.3.4]:25, delay=0.5, dsn=5.1.1, status=bounced (host mx.example.org said: 550 User not found)"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Status != mail.DeliveryStatusBounced {
			t.Errorf("expected status bounced, got %s", ev.Status)
		}
		if ev.DSN != "5.1.1" {
			t.Errorf("expected DSN 5.1.1, got %s", ev.DSN)
		}
	})

	t.Run("qmgr from event", func(t *testing.T) {
		line := "Oct 23 14:00:00 mail postfix/qmgr[1234]: 4S123456789: from=<sender@tenant.example.com>, size=4521, nrcpt=1 (queue active)"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Sender != "sender@tenant.example.com" {
			t.Errorf("expected sender sender@tenant.example.com, got %s", ev.Sender)
		}
		if ev.SenderDomain != "tenant.example.com" {
			t.Errorf("expected sender domain tenant.example.com, got %s", ev.SenderDomain)
		}
		if ev.Status != mail.DeliveryStatusQueued {
			t.Errorf("expected status queued, got %s", ev.Status)
		}
	})

	t.Run("cleanup message-id event", func(t *testing.T) {
		line := "Oct 23 14:00:00 mail postfix/cleanup[1234]: 4S123456789: message-id=<msg-12345@tenant.example.com>"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.MessageID != "msg-12345@tenant.example.com" {
			t.Errorf("expected message-id msg-12345@tenant.example.com, got %s", ev.MessageID)
		}
	})

	t.Run("qmgr null sender event", func(t *testing.T) {
		// A null sender is valid Postfix output (the bounce of a bounce).
		line := "Oct 23 14:00:01 mail postfix/qmgr[1234]: 4S123456789: from=<>, size=1234, nrcpt=1 (queue active)"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Status != mail.DeliveryStatusQueued {
			t.Errorf("expected status queued, got %s", ev.Status)
		}
		if ev.Sender != "" {
			t.Errorf("expected an empty sender, got %s", ev.Sender)
		}
	})

	t.Run("qmgr removed event", func(t *testing.T) {
		line := "Oct 23 14:00:05 mail postfix/qmgr[1234]: 4S123456789: removed"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Status != mail.DeliveryStatusRemoved {
			t.Errorf("expected status removed, got %s", ev.Status)
		}
	})

	t.Run("non-postfix line rejected", func(t *testing.T) {
		line := "Oct 23 14:00:00 mail dovecot: imap-login: Login: user=<test>"
		_, err := mail.ParsePostfixLogLine(line)
		if !errors.Is(err, mail.ErrNotPostfixLine) {
			t.Errorf("expected ErrNotPostfixLine, got %v", err)
		}
	})

	t.Run("syslog timestamp parsed into event time", func(t *testing.T) {
		line := "Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent"
		ev, err := mail.ParsePostfixLogLineWithOptions(line, mail.ParserOptions{
			Location: time.UTC,
			Now:      time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, time.October, 23, 14, 0, 1, 0, time.UTC)
		if !ev.Timestamp.Equal(want) {
			t.Errorf("expected event time %v, got %v", want, ev.Timestamp)
		}
	})

	t.Run("missing syslog timestamp falls back to a deterministic value", func(t *testing.T) {
		line := "postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Timestamp.IsZero() {
			t.Fatal("expected a non-zero fallback timestamp")
		}
		// The fallback must not be the current clock: a clock based value
		// would produce a different dedupe key on every re-read.
		if delta := ev.Timestamp.Sub(time.Now().UTC()); delta > -time.Hour && delta < time.Hour {
			t.Errorf("expected a deterministic fallback that is not the current clock, got %v", ev.Timestamp)
		}
	})

	t.Run("malformed syslog timestamp falls back to a deterministic value", func(t *testing.T) {
		line := "Otc 99 25:61:61 mail postfix/qmgr[1234]: 4S123456789: removed"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Timestamp.IsZero() {
			t.Fatal("expected a non-zero fallback timestamp")
		}
		if delta := ev.Timestamp.Sub(time.Now().UTC()); delta > -time.Hour && delta < time.Hour {
			t.Errorf("expected a deterministic fallback that is not the current clock, got %v", ev.Timestamp)
		}
	})

	t.Run("queue id charset includes upper, lower, digit and !", func(t *testing.T) {
		line := "Oct 23 14:00:01 mail postfix/smtp[2345]: 3tGqQ1Zx9c!Ab: to=<u@d.org>, relay=mx[1.2.3.4]:25, delay=0.1, dsn=2.0.0, status=sent"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error for long queue id: %v", err)
		}
		if ev.QueueID != "3tGqQ1Zx9c!Ab" {
			t.Errorf("expected queue id 3tGqQ1Zx9c!Ab, got %s", ev.QueueID)
		}
	})
}

// TestParsePostfixLogLineYearRollover pins the RFC3164 year inference. It
// uses a fixed reference clock, so it runs - and passes - in every month of
// the year.
func TestParsePostfixLogLineYearRollover(t *testing.T) {
	cases := []struct {
		name string
		ref  time.Time
		line string
		want time.Time
	}{
		{
			name: "december line read in january belongs to the previous year",
			ref:  time.Date(2027, time.January, 1, 0, 30, 0, 0, time.UTC),
			line: "Dec 31 23:59:59 mail postfix/qmgr[1234]: 4S123456789: removed",
			want: time.Date(2026, time.December, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			name: "january line read in december belongs to the next year",
			ref:  time.Date(2026, time.December, 31, 23, 30, 0, 0, time.UTC),
			line: "Jan 1 00:00:01 mail postfix/qmgr[1234]: 4S123456789: removed",
			want: time.Date(2027, time.January, 1, 0, 0, 1, 0, time.UTC),
		},
		{
			name: "same day line keeps the reference year",
			ref:  time.Date(2026, time.March, 15, 8, 0, 0, 0, time.UTC),
			line: "Mar 15 07:59:59 mail postfix/qmgr[1234]: 4S123456789: removed",
			want: time.Date(2026, time.March, 15, 7, 59, 59, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := mail.ParsePostfixLogLineWithOptions(tc.line, mail.ParserOptions{
				Location: time.UTC,
				Now:      tc.ref,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ev.Timestamp.Equal(tc.want) {
				t.Errorf("expected event time %v, got %v", tc.want, ev.Timestamp)
			}
		})
	}
}

// TestParsePostfixLogLineBounce covers the bounce daemon, whose lines carry
// neither a recipient nor a status word. They must be parsed and mapped to
// the bounced status instead of being dropped as "not a postfix line".
func TestParsePostfixLogLineBounce(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{
			name: "bounce daemon notification",
			line: "Oct 23 14:00:03 mail postfix/bounce[1234]: 4S123456789: sender non-delivery notification: 5A1B2C3D4E",
		},
		{
			name: "bounce daemon in a multi instance deployment",
			line: "Oct 23 14:00:03 mail postfix-out/bounce[1234]: 4S123456789: sender non-delivery notification: 5A1B2C3D4E",
		},
		{
			name: "delivery status report",
			line: "Oct 23 14:00:03 mail postfix/bounce[1234]: 4S123456789: delivery status report: 5.1.1 User not found",
		},
		{
			name: "fatal delivery error",
			line: "Oct 23 14:00:04 mail postfix/error[1234]: 4S123456789: fatal: delivery error: bounced to nosuchuser@example.org",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := mail.ParsePostfixLogLine(tc.line)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ev.Status != mail.DeliveryStatusBounced {
				t.Errorf("expected status bounced, got %s", ev.Status)
			}
			if ev.QueueID != "4S123456789" {
				t.Errorf("expected queue ID 4S123456789, got %s", ev.QueueID)
			}
		})
	}
}

// TestParsePostfixLogLineMultiInstance covers deployments that enable
// multi_instance_wrapper(5): their syslog tag carries the instance name in
// front of the service name.
func TestParsePostfixLogLineMultiInstance(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		process   string
		component string
	}{
		{
			name:      "postfix-out instance",
			line:      "Oct 23 14:00:01 mail postfix-out/smtp[1234]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent (250 Ok)",
			process:   "postfix-out",
			component: "smtp",
		},
		{
			name:      "postfix-in instance",
			line:      "Oct 23 14:00:01 mail postfix-in/smtp[1234]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent (250 Ok)",
			process:   "postfix-in",
			component: "smtp",
		},
		{
			name:      "postfix-in cleanup instance",
			line:      "Oct 23 14:00:00 mail postfix-in/cleanup[1234]: 4S123456789: message-id=<msg-12345@tenant.example.com>",
			process:   "postfix-in",
			component: "cleanup",
		},
		{
			name:      "plain postfix instance",
			line:      "Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent",
			process:   "postfix",
			component: "smtp",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := mail.ParsePostfixLogLine(tc.line)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ev.Process != tc.process {
				t.Errorf("expected process %s, got %s", tc.process, ev.Process)
			}
			if ev.Component != tc.component {
				t.Errorf("expected component %s, got %s", tc.component, ev.Component)
			}
			if ev.QueueID != "4S123456789" {
				t.Errorf("expected queue ID 4S123456789, got %s", ev.QueueID)
			}
		})
	}
}

// TestParsePostfixLogLineRFC5424 covers journald / RFC5424 timestamps, which
// carry a full date and an explicit UTC offset.
func TestParsePostfixLogLineRFC5424(t *testing.T) {
	t.Run("timestamp with positive offset", func(t *testing.T) {
		line := "2026-10-23T14:00:12+08:00 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, time.October, 23, 6, 0, 12, 0, time.UTC)
		if !ev.Timestamp.Equal(want) {
			t.Errorf("expected event time %v, got %v", want, ev.Timestamp)
		}
		if _, offset := ev.Timestamp.Zone(); offset != 8*3600 {
			t.Errorf("expected the +08:00 offset to be preserved, got %d seconds", offset)
		}
		if ev.Status != mail.DeliveryStatusSent {
			t.Errorf("expected status sent, got %s", ev.Status)
		}
	})

	t.Run("timestamp with negative offset", func(t *testing.T) {
		line := "2026-10-23T14:00:12-05:00 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, time.October, 23, 19, 0, 12, 0, time.UTC)
		if !ev.Timestamp.Equal(want) {
			t.Errorf("expected event time %v, got %v", want, ev.Timestamp)
		}
		if _, offset := ev.Timestamp.Zone(); offset != -5*3600 {
			t.Errorf("expected the -05:00 offset to be preserved, got %d seconds", offset)
		}
	})

	t.Run("timestamp in UTC", func(t *testing.T) {
		line := "2026-10-23T14:00:12Z mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, time.October, 23, 14, 0, 12, 0, time.UTC)
		if !ev.Timestamp.Equal(want) {
			t.Errorf("expected event time %v, got %v", want, ev.Timestamp)
		}
	})

	t.Run("timestamp with fractional seconds and a multi instance tag", func(t *testing.T) {
		line := "2026-10-23T14:00:12.123456+08:00 mail postfix-out/smtp[1234]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, time.October, 23, 6, 0, 12, 123456000, time.UTC)
		if !ev.Timestamp.Equal(want) {
			t.Errorf("expected event time %v, got %v", want, ev.Timestamp)
		}
		if ev.Process != "postfix-out" {
			t.Errorf("expected process postfix-out, got %s", ev.Process)
		}
	})

	t.Run("timestamp without offset uses the configured location", func(t *testing.T) {
		line := "2026-10-23T14:00:12 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent"
		loc := time.FixedZone("TST", 8*3600)
		ev, err := mail.ParsePostfixLogLineWithOptions(line, mail.ParserOptions{Location: loc})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, time.October, 23, 14, 0, 12, 0, loc)
		if !ev.Timestamp.Equal(want) {
			t.Errorf("expected event time %v, got %v", want, ev.Timestamp)
		}
		if _, offset := ev.Timestamp.Zone(); offset != 8*3600 {
			t.Errorf("expected the +08:00 location to be used, got %d seconds", offset)
		}
	})
}

// TestParsePostfixLogLineRFC3164UsesConfiguredLocation documents that an
// RFC3164 timestamp carries no offset and is therefore interpreted in the
// configured local zone, which is the zone Postfix logs in.
func TestParsePostfixLogLineRFC3164UsesConfiguredLocation(t *testing.T) {
	line := "Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent"
	loc := time.FixedZone("TST", 8*3600)
	ev, err := mail.ParsePostfixLogLineWithOptions(line, mail.ParserOptions{
		Location: loc,
		Now:      time.Date(2026, time.October, 24, 12, 0, 0, 0, loc),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, time.October, 23, 14, 0, 1, 0, loc)
	if !ev.Timestamp.Equal(want) {
		t.Errorf("expected event time %v, got %v", want, ev.Timestamp)
	}
	if _, offset := ev.Timestamp.Zone(); offset != 8*3600 {
		t.Errorf("expected the configured location to be used, got %d seconds", offset)
	}
	// The same wall clock in a different zone is a different instant.
	utcEv, err := mail.ParsePostfixLogLineWithOptions(line, mail.ParserOptions{
		Location: time.UTC,
		Now:      time.Date(2026, time.October, 24, 4, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if utcEv.Timestamp.Equal(ev.Timestamp) {
		t.Errorf("expected different instants for different zones, got %v", ev.Timestamp)
	}
}

// TestParsePostfixLogLineNonNumericDelay covers delivery status records whose
// delay cannot be parsed. The recipient and the status are still known, so the
// event must be kept and only the delay left unset.
func TestParsePostfixLogLineNonNumericDelay(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{
			name: "comma separated delay",
			line: "Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1,234, delays=0.1/0.01/0.8/0.49, dsn=2.0.0, status=sent (250 Ok)",
		},
		{
			name: "non numeric delay",
			line: "Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=abc, dsn=2.0.0, status=sent (250 Ok)",
		},
		{
			name: "empty delay",
			line: "Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=, dsn=2.0.0, status=sent (250 Ok)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := mail.ParsePostfixLogLine(tc.line)
			if err != nil {
				t.Fatalf("expected the event to be kept, got error: %v", err)
			}
			if ev.DelaySeconds != 0 {
				t.Errorf("expected the delay to be left unset, got %f", ev.DelaySeconds)
			}
			if ev.Recipient != "user@remote.example.com" {
				t.Errorf("expected recipient user@remote.example.com, got %s", ev.Recipient)
			}
			if ev.Status != mail.DeliveryStatusSent {
				t.Errorf("expected status sent, got %s", ev.Status)
			}
			if ev.DSN != "2.0.0" {
				t.Errorf("expected DSN 2.0.0, got %s", ev.DSN)
			}
		})
	}
}

// TestParsePostfixLogLineOnlyEmitsAcceptedStatuses asserts that the parser
// never emits a status the delivery_events CHECK rejects, because such an
// insert would fail and the delivery event would be lost.
func TestParsePostfixLogLineOnlyEmitsAcceptedStatuses(t *testing.T) {
	accepted := mail.AcceptedDeliveryStatuses()
	sort.Strings(accepted)
	want := append([]string(nil), migrationStatuses...)
	sort.Strings(want)
	if !reflect.DeepEqual(accepted, want) {
		t.Errorf("expected accepted statuses %v, got %v", want, accepted)
	}

	lines := []string{
		"Oct 23 14:00:00 mail postfix/qmgr[1234]: 4S123456789: from=<sender@tenant.example.com>, size=4521, nrcpt=1 (queue active)",
		"Oct 23 14:00:00 mail postfix/cleanup[1234]: 4S123456789: message-id=<msg-1@tenant.example.com>",
		"Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent (250 Ok)",
		"Oct 23 14:00:02 mail postfix/smtp[2345]: 4S123456789: to=<busy@example.org>, relay=none, delay=30, dsn=4.4.1, status=deferred (timeout)",
		"Oct 23 14:00:03 mail postfix/smtp[2345]: 4S123456789: to=<nosuchuser@example.org>, relay=mx[1.2.3.4]:25, delay=0.5, dsn=5.1.1, status=bounced (550 User not found)",
		"Oct 23 14:00:03 mail postfix/bounce[1234]: 4S123456789: sender non-delivery notification: 5A1B2C3D4E",
		"Oct 23 14:00:04 mail postfix/error[1234]: 4S123456789: fatal: delivery error: bounced to nosuchuser@example.org",
		"Oct 23 14:00:05 mail postfix/qmgr[1234]: 4S123456789: removed",
		"Oct 23 14:00:06 mail postfix/qmgr[1234]: 4S123456789: message expired",
		"Oct 23 14:00:07 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=warning (250 Ok)",
		"Oct 23 14:00:08 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=undeliverable",
		"Oct 23 14:00:09 mail postfix-out/smtp[1234]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent",
		"2026-10-23T14:00:10+08:00 mail postfix-in/smtp[1234]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent",
		"postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent",
	}

	for _, line := range lines {
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", line, err)
		}
		if !mail.IsAcceptedDeliveryStatus(ev.Status) {
			t.Errorf("status %q of %q is not accepted by delivery_events", ev.Status, line)
		}
	}

	// A status word outside the accepted set is normalised, not passed
	// through: it would be rejected by the delivery_events CHECK.
	ev, err := mail.ParsePostfixLogLine("Oct 23 14:00:07 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=warning (250 Ok)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Status != mail.DeliveryStatusUnknown {
		t.Errorf("expected status warning to be normalised to unknown, got %s", ev.Status)
	}
}

// TestParsePostfixLogLineIsDeterministic asserts that re-reading the same log
// line yields the same event time and therefore the same dedupe key
// (queue_id, recipient, occurred_at). A clock based fallback would count the
// same delivery event twice after a log rotation.
func TestParsePostfixLogLineIsDeterministic(t *testing.T) {
	lines := []string{
		"Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent",
		"2026-10-23T14:00:01+08:00 mail postfix-out/smtp[1234]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent",
		"postfix/smtp[2345]: 4S123456789: to=<user@remote.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent",
		"Otc 99 25:61:61 mail postfix/qmgr[1234]: 4S123456789: removed",
		"Oct 23 14:00:03 mail postfix/bounce[1234]: 4S123456789: sender non-delivery notification: 5A1B2C3D4E",
	}

	for _, line := range lines {
		first, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", line, err)
		}
		second, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", line, err)
		}
		if !first.Timestamp.Equal(second.Timestamp) {
			t.Errorf("expected identical event time for %q, got %v and %v", line, first.Timestamp, second.Timestamp)
		}
		if !reflect.DeepEqual(first, second) {
			t.Errorf("expected identical events for %q, got %+v and %+v", line, first, second)
		}
		if first.QueueID != second.QueueID || first.Recipient != second.Recipient {
			t.Errorf("expected identical dedupe key parts for %q", line)
		}
	}
}

// TestParsePostfixLogLineCorrelationFields asserts that the correlation
// fields are populated from whichever line carries them, so a consumer can
// relate a message id to its recipients.
func TestParsePostfixLogLineCorrelationFields(t *testing.T) {
	t.Run("line carrying a recipient and a message id exposes both", func(t *testing.T) {
		line := "Oct 23 14:00:00 mail postfix/cleanup[1234]: 4S123456789: message-id=<msg-1@tenant.example.com>, to=<user@tenant.example.com>"
		ev, err := mail.ParsePostfixLogLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.MessageID != "msg-1@tenant.example.com" {
			t.Errorf("expected message id msg-1@tenant.example.com, got %s", ev.MessageID)
		}
		if ev.Recipient != "user@tenant.example.com" {
			t.Errorf("expected recipient user@tenant.example.com, got %s", ev.Recipient)
		}
		if ev.RecipientDomain != "tenant.example.com" {
			t.Errorf("expected recipient domain tenant.example.com, got %s", ev.RecipientDomain)
		}
	})

	t.Run("queue id correlates the message id with its recipients", func(t *testing.T) {
		qmgr, err := mail.ParsePostfixLogLine("Oct 23 14:00:00 mail postfix/qmgr[1234]: 4S123456789: from=<sender@tenant.example.com>, size=4521, nrcpt=1 (queue active)")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cleanup, err := mail.ParsePostfixLogLine("Oct 23 14:00:00 mail postfix/cleanup[1234]: 4S123456789: message-id=<msg-1@tenant.example.com>")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		smtp, err := mail.ParsePostfixLogLine("Oct 23 14:00:01 mail postfix/smtp[2345]: 4S123456789: to=<user@tenant.example.com>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if qmgr.QueueID != cleanup.QueueID || cleanup.QueueID != smtp.QueueID {
			t.Errorf("expected the same queue id on every line of the message, got %q, %q and %q", qmgr.QueueID, cleanup.QueueID, smtp.QueueID)
		}
		if cleanup.MessageID == "" {
			t.Error("expected the message id to be parsed from the cleanup line")
		}
		if smtp.Recipient == "" {
			t.Error("expected the recipient to be parsed from the smtp line")
		}
	})
}

func TestAuthenticatedPrincipalOnlyComesFromCanonicalClientRecord(t *testing.T) {
	line := "2026-10-24T10:00:00Z host postfix/smtpd[1]: ABC123: client=unknown[127.0.0.1], sasl_method=PLAIN, sasl_username=status=sent@example.test"
	event, err := mail.ParsePostfixLogLine(line)
	if err != nil || event.AuthenticatedMailbox != "status=sent@example.test" || event.Status != mail.DeliveryStatusUnknown {
		t.Fatalf("canonical client not recognized safely: %+v %v", event, err)
	}
	spoof := "2026-10-24T10:00:00Z host postfix/smtpd[1]: ABC123: reject: from=<forged, sasl_method=PLAIN, sasl_username=alice@example.test>"
	event, err = mail.ParsePostfixLogLine(spoof)
	if err != nil || event.AuthenticatedMailbox != "" {
		t.Fatalf("envelope text authenticated: %+v %v", event, err)
	}
}
