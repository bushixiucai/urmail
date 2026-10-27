package supportdiag

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func fixture() Input {
	lastBackup := time.Date(2026, 10, 26, 3, 0, 0, 0, time.UTC)
	return Input{
		TenantID:         4213,
		ClientVersion:    "web/2026.10",
		ShareWithSupport: true,
		GeneratedAt:      time.Date(2026, 10, 27, 5, 0, 0, 0, time.UTC),
		Delivery:         DeliveryCounters{Sent: 910, Deferred: 12, Failed: 3, QueuedNow: 2},
		Bounces: []BounceCode{
			{Code: "550 5.1.1", Count: 3, LastSeenAt: time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)},
			{Code: "421 4.4.1", Count: 9, LastSeenAt: time.Date(2026, 10, 27, 4, 30, 0, 0, time.UTC)},
		},
		Mailboxes: MailboxFlags{Total: 25, Active: 22, Suspended: 2, OverQuota: 1},
		Config:    ConfigStatus{DesiredGeneration: 88, AppliedGeneration: 87},
		Backup:    BackupFreshness{LastSuccessAt: &lastBackup, LastStatus: "success"},
		Addresses: []string{"alice@tenant4213.example", "bob@tenant4213.example"},
	}
}

func TestBuildDeterministic(t *testing.T) {
	first, err := Build(fixture())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(fixture())
	if err != nil {
		t.Fatal(err)
	}
	a, err := first.JSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("bundle not deterministic")
	}
	if len(a) == 0 || !strings.Contains(a, "\"tenant_id\":4213") {
		t.Fatalf("bundle missing tenant id: %s", a)
	}
}

func TestBuildFieldListIsClosed(t *testing.T) {
	bundle, err := Build(fixture())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range bundle.Fields {
		names[f.Name] = true
	}
	allow := []string{
		"delivery.sent_24h", "delivery.deferred_24h", "delivery.failed_24h", "delivery.queued_now",
		"mailboxes.total", "mailboxes.active", "mailboxes.suspended", "mailboxes.over_quota",
		"config.desired_generation", "config.applied_generation", "config.drifted",
		"backup.age_hours", "backup.last_status", "client.version",
		"bounce.421-4-4-1", "bounce.550-5-1-1", "affected.address",
	}
	if len(names) != len(allow) {
		t.Fatalf("unexpected field set: %v", names)
	}
	for _, want := range allow {
		if !names[want] {
			t.Errorf("missing field %q", want)
		}
	}
}

func TestConsentGate(t *testing.T) {
	in := fixture()
	in.ShareWithSupport = false
	if _, err := Build(in); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("consent gate: got %v", err)
	}
}

func TestLimits(t *testing.T) {
	in := fixture()
	in.Bounces = make([]BounceCode, MaxBounceCodes+1)
	if _, err := Build(in); !errors.Is(err, ErrTooMany) {
		t.Fatalf("bounce limit: got %v", err)
	}
	in = fixture()
	in.Addresses = make([]string, MaxAddresses+1)
	if _, err := Build(in); !errors.Is(err, ErrTooMany) {
		t.Fatalf("address limit: got %v", err)
	}
}

func TestPseudonymization(t *testing.T) {
	a := Pseudonymize("Alice@Example.com")
	b := Pseudonymize("alice@example.com")
	if a != b {
		t.Error("pseudonym not case-insensitive")
	}
	if a == "Alice@Example.com" || strings.Contains(a, "@") {
		t.Errorf("pseudonym leaks address: %q", a)
	}
	if len(a) != len("user_")+16 {
		t.Errorf("unexpected pseudonym shape %q", a)
	}
	if Pseudonymize("mallory@example.com") == a {
		t.Error("pseudonym collision across addresses")
	}
}

func TestBackupAgeNeverAndDrift(t *testing.T) {
	in := fixture()
	in.Backup = BackupFreshness{}
	bundle, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range bundle.Fields {
		if f.Name == "backup.age_hours" && f.Value != "never" {
			t.Errorf("backup age = %q", f.Value)
		}
		if f.Name == "config.drifted" && f.Value != "true" {
			t.Errorf("drift = %q", f.Value)
		}
	}
}

// secretShapes are patterns that must never appear in a bundle: password
// material, keys, tokens, raw local parts of addresses.
var secretShapes = map[string]*regexp.Regexp{
	"pemPrivateKey": regexp.MustCompile(`'-----BEGIN [A-Z ]*PRIVATE KEY-----'`),
	"bearerToken":   regexp.MustCompile("(?i)bearer[ _][A-Za-z0-9._-]{16,}"),
	"passwordKw":    regexp.MustCompile("(?i)(password|passwd|secret|api[_-]?key)[ \t]*[:=]"),
	"jwtShape":      regexp.MustCompile("eyJ[A-Za-z0-9_-]{10,}[.][A-Za-z0-9_-]{10,}"),
	"hexSecret64":   regexp.MustCompile("[0-9a-f]{64}"),
	"dovecotHash":   regexp.MustCompile("[{][$][0-9a-z]+[$][}][A-Za-z0-9+/=]{20,}"),
}

func TestNoSecretShapesLeak(t *testing.T) {
	in := fixture()
	// Hostile inputs: nothing in the Input can reach the bundle except the
	// allowlisted projections.
	in.Bounces = append(in.Bounces,
		BounceCode{Code: "-----BEGIN RSA PRIVATE KEY-----", Count: 1, LastSeenAt: time.Now()},
		BounceCode{Code: "password=hunter2", Count: 1, LastSeenAt: time.Now()},
	)
	in.Addresses = append(in.Addresses, "secret-admin@tenant4213.example")
	bundle, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := bundle.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for name, pattern := range secretShapes {
		if pattern.MatchString(rendered) {
			t.Errorf("secret shape %q leaked into bundle: %s", name, rendered)
		}
	}
	for _, leak := range []string{"hunter2", "alice@", "bob@", "secret-admin@", "BEGIN RSA"} {
		if strings.Contains(rendered, leak) {
			t.Errorf("raw sensitive text %q present in bundle", leak)
		}
	}
	found := 0
	for _, f := range bundle.Fields {
		if f.Name == "affected.address" {
			found++
			if strings.Contains(f.Value, "@") {
				t.Errorf("affected.address not pseudonymized: %q", f.Value)
			}
		}
	}
	if found != 3 {
		t.Errorf("affected.address count = %d want 3", found)
	}
}

func TestSanitizeBounceCode(t *testing.T) {
	cases := map[string]string{
		"550 5.1.1": "550-5-1-1",
		"421 4.4.1": "421-4-4-1",
		"5.x.x":     "5----",
		" 553 ":     "553",
	}
	for in, want := range cases {
		if got := sanitizeCode(in); got != want {
			t.Errorf("sanitizeCode(%q)=%q want %q", in, got, want)
		}
	}
}

func TestClientVersionFallback(t *testing.T) {
	in := fixture()
	in.ClientVersion = ""
	bundle, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range bundle.Fields {
		if f.Name == "client.version" && f.Value != "unspecified" {
			t.Errorf("client.version = %q", f.Value)
		}
	}
}
