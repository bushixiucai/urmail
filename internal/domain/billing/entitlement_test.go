package billing

import (
	"strings"
	"testing"
	"time"
)

func TestMigrationEntitlementRequiresExplicitSourceAndEvidence(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	zero := time.Time{}
	base := ServiceEntitlement{Source: EntitlementMigration, ManifestSHA256: strings.Repeat("a", 64), Status: StatusActive, PeriodStart: past}
	if !base.CurrentAt(now) || !base.CurrentAt(now.AddDate(200, 0, 0)) {
		t.Fatal("audited migration mail service received an implicit expiry")
	}
	tests := []struct {
		name   string
		mutate func(*ServiceEntitlement)
	}{
		{"missing source", func(e *ServiceEntitlement) { e.Source = "" }},
		{"unknown source", func(e *ServiceEntitlement) { e.Source = "caller_claim" }},
		{"missing manifest", func(e *ServiceEntitlement) { e.ManifestSHA256 = "" }},
		{"malformed manifest", func(e *ServiceEntitlement) { e.ManifestSHA256 = strings.Repeat("z", 64) }},
		{"canceled", func(e *ServiceEntitlement) { e.Status = StatusCanceled }},
		{"past due migration", func(e *ServiceEntitlement) { e.Status = StatusPastDue }},
		{"future start", func(e *ServiceEntitlement) { e.PeriodStart = future }},
		{"missing start", func(e *ServiceEntitlement) { e.PeriodStart = zero }},
		{"finite end", func(e *ServiceEntitlement) { e.PeriodEnd = &future }},
		{"zero/infinite placeholder", func(e *ServiceEntitlement) { e.PeriodEnd = &zero }},
		{"trial mixed into grant", func(e *ServiceEntitlement) { e.TrialEndsAt = &future }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := base
			tc.mutate(&value)
			if value.CurrentAt(now) {
				t.Fatal("invalid migration claim granted service")
			}
		})
	}
}
func TestStandardEntitlementRemainsFinite(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	for _, source := range []string{"", EntitlementStandard} {
		e := ServiceEntitlement{Source: source, Status: StatusActive, PeriodStart: past, PeriodEnd: &future}
		if !e.CurrentAt(now) || e.CurrentAt(future) {
			t.Fatal("normal finite subscription behavior changed")
		}
		e.PeriodEnd = nil
		if e.CurrentAt(now) {
			t.Fatal("missing normal end received free perpetual service")
		}
	}
}
func TestQuoteSourceDoesNotPromoteLegacySignaturesToMigration(t *testing.T) {
	if !QuoteSourceMatches("", EntitlementStandard) || !QuoteSourceMatches(EntitlementMigration, EntitlementMigration) || QuoteSourceMatches("", EntitlementMigration) || QuoteSourceMatches(EntitlementMigration, EntitlementStandard) || QuoteSourceMatches("caller_claim", "caller_claim") {
		t.Fatal("quote entitlement source boundary violated")
	}
}

func TestQuoteSignatureBindsExplicitMigrationSource(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	quote, err := BuildPricedQuote(1, "", "pro", 100, CycleMonthly, "CNY", "", 0, 0, time.Now(), time.Now().AddDate(0, 1, 0), time.Hour, key)
	if err != nil {
		t.Fatal(err)
	}
	original := quote.Digest
	if quote.ComputeDigest(key) != original {
		t.Fatal("empty source changed legacy signature")
	}
	quote.CurrentEntitlementSource = EntitlementMigration
	if quote.ComputeDigest(key) == original {
		t.Fatal("migration source was not authenticated")
	}
	if err = quote.VerifyIntegrity(time.Now(), key); err == nil {
		t.Fatal("source tampering passed HMAC")
	}
	quote.Digest = quote.ComputeDigest(key)
	if err = quote.VerifyIntegrity(time.Now(), key); err != nil {
		t.Fatal(err)
	}
	quote.CurrentEntitlementSource = EntitlementStandard
	if err = quote.VerifyIntegrity(time.Now(), key); err == nil {
		t.Fatal("migration source removed from quote")
	}
}
