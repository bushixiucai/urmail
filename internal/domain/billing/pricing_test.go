package billing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/billing"
)

func TestCalculateRenewalPeriod(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	t.Run("early renewal preserves remaining time", func(t *testing.T) {
		currentEnd := now.Add(15 * 24 * time.Hour) // expires in 15 days
		start, end, err := billing.CalculateRenewalPeriod(now, currentEnd, billing.CycleMonthly)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !start.Equal(currentEnd) {
			t.Errorf("expected start to match currentEnd %v, got %v", currentEnd, start)
		}
		expectedEnd := currentEnd.AddDate(0, 1, 0)
		if !end.Equal(expectedEnd) {
			t.Errorf("expected end %v, got %v", expectedEnd, end)
		}
	})

	t.Run("post expiry renewal starts from now", func(t *testing.T) {
		currentEnd := now.Add(-5 * 24 * time.Hour) // expired 5 days ago
		start, end, err := billing.CalculateRenewalPeriod(now, currentEnd, billing.CycleMonthly)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !start.Equal(now) {
			t.Errorf("expected start %v, got %v", now, start)
		}
		expectedEnd := now.AddDate(0, 1, 0)
		if !end.Equal(expectedEnd) {
			t.Errorf("expected end %v, got %v", expectedEnd, end)
		}
	})

	t.Run("new subscription without prior end starts from now", func(t *testing.T) {
		start, end, err := billing.CalculateRenewalPeriod(now, time.Time{}, billing.CycleAnnual)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !start.Equal(now) {
			t.Errorf("expected start %v, got %v", now, start)
		}
		expectedEnd := now.AddDate(1, 0, 0)
		if !end.Equal(expectedEnd) {
			t.Errorf("expected end %v, got %v", expectedEnd, end)
		}
	})

	t.Run("invalid cycle returns error", func(t *testing.T) {
		_, _, err := billing.CalculateRenewalPeriod(now, now, "bi-weekly")
		if !errors.Is(err, billing.ErrInvalidCycle) {
			t.Errorf("expected ErrInvalidCycle, got %v", err)
		}
	})
}

func TestCalculateBasePrice(t *testing.T) {
	monthlyCents := int64(10000) // 100.00 CNY

	t.Run("monthly", func(t *testing.T) {
		price, err := billing.CalculateBasePrice(monthlyCents, billing.CycleMonthly)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if price != 10000 {
			t.Errorf("expected 10000, got %d", price)
		}
	})

	t.Run("quarterly 5% discount", func(t *testing.T) {
		price, err := billing.CalculateBasePrice(monthlyCents, billing.CycleQuarterly)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// 3 * 10000 = 30000; 5% of 30000 = 1500; 30000 - 1500 = 28500
		if price != 28500 {
			t.Errorf("expected 28500, got %d", price)
		}
	})

	t.Run("annual 10 months charge", func(t *testing.T) {
		price, err := billing.CalculateBasePrice(monthlyCents, billing.CycleAnnual)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if price != 100000 {
			t.Errorf("expected 100000, got %d", price)
		}
	})

	t.Run("negative price rejected", func(t *testing.T) {
		_, err := billing.CalculateBasePrice(-50, billing.CycleMonthly)
		if !errors.Is(err, billing.ErrInvalidPlanPrice) {
			t.Errorf("expected ErrInvalidPlanPrice, got %v", err)
		}
	})
}

func TestCalculateUpgradeProration(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	// exactly halfway through the period (15 days elapsed, 15 days remaining out of 30 days)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)

	currentMonthly := int64(6000) // 60 CNY
	newMonthly := int64(12000)    // 120 CNY

	credit, net, err := billing.CalculateUpgradeProration(now, start, end, currentMonthly, newMonthly, billing.CycleMonthly, billing.CycleMonthly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Unused 15/30 = 0.5 of 6000 = 3000 credit
	if credit != 3000 {
		t.Errorf("expected credit 3000, got %d", credit)
	}
	// Net payable = 12000 - 3000 = 9000
	if net != 9000 {
		t.Errorf("expected net payable 9000, got %d", net)
	}

	t.Run("invalid period returns error", func(t *testing.T) {
		_, _, err := billing.CalculateUpgradeProration(now, end, start, currentMonthly, newMonthly, billing.CycleMonthly, billing.CycleMonthly)
		if !errors.Is(err, billing.ErrInvalidPeriod) {
			t.Errorf("expected ErrInvalidPeriod, got %v", err)
		}
	})
}

func TestQuoteIntegrityAndExpiration(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	end := start.AddDate(0, 1, 0)

	quoteKey := []byte("test-quote-signing-key-at-least-32-bytes")
	quote, err := billing.BuildQuote(
		1,
		"SAVE",
		"pro",
		10000,
		billing.CycleMonthly,
		"CNY",
		billing.CouponKindPercent,
		20,   // 20% off
		1000, // 10.00 credit
		start,
		end,
		15*time.Minute,
		quoteKey,
	)
	if err != nil {
		t.Fatalf("failed to build quote: %v", err)
	}

	// Base 10000, 20% discount = 2000, credit = 1000, final = 7000
	if quote.BaseAmountCents != 10000 {
		t.Errorf("expected base 10000, got %d", quote.BaseAmountCents)
	}
	if quote.DiscountCents != 2000 {
		t.Errorf("expected discount 2000, got %d", quote.DiscountCents)
	}
	if quote.FinalAmountCents != 7000 {
		t.Errorf("expected final 7000, got %d", quote.FinalAmountCents)
	}

	// Verify valid quote
	if err := quote.VerifyIntegrity(time.Now().UTC(), quoteKey); err != nil {
		t.Fatalf("expected valid quote, got error: %v", err)
	}

	// Verify expired quote
	expiredTime := quote.ExpiresAt.Add(1 * time.Minute)
	if err := quote.VerifyIntegrity(expiredTime, quoteKey); !errors.Is(err, billing.ErrQuoteExpired) {
		t.Errorf("expected ErrQuoteExpired, got %v", err)
	}

	// Verify tampered quote
	tamperedQuote := quote
	tamperedQuote.FinalAmountCents = 100 // malicious client tamper
	if err := tamperedQuote.VerifyIntegrity(time.Now().UTC(), quoteKey); !errors.Is(err, billing.ErrQuoteTampered) {
		t.Errorf("expected ErrQuoteTampered, got %v", err)
	}

	// A signed quote cannot extend its own expiry beyond the issuance cap.
	longLived := quote
	longLived.ExpiresAt = longLived.GeneratedAt.Add(48 * time.Hour)
	longLived.Digest = longLived.ComputeDigest(quoteKey)
	if err := longLived.VerifyIntegrity(time.Now().UTC(), quoteKey); !errors.Is(err, billing.ErrQuoteTampered) {
		t.Errorf("expected overlong quote lifetime to fail, got %v", err)
	}
	// Tenant identity is included in the signature.
	tenantChanged := quote
	tenantChanged.TenantID++
	if err := tenantChanged.VerifyIntegrity(time.Now().UTC(), quoteKey); !errors.Is(err, billing.ErrQuoteTampered) {
		t.Errorf("expected tenant tampering to fail, got %v", err)
	}

	// Verify with the wrong key fails: digests are keyed, not public hashes
	if err := quote.VerifyIntegrity(time.Now().UTC(), []byte("wrong-key-that-is-long-enough-for-testing")); !errors.Is(err, billing.ErrQuoteTampered) {
		t.Errorf("expected ErrQuoteTampered with wrong key, got %v", err)
	}
	// BuildQuote refuses to sign without a key (no hardcoded default)
	_, err = billing.BuildQuote(1, "", "pro", 10000, billing.CycleMonthly, "CNY", "", 0, 0, start, end, time.Minute, nil)
	if !errors.Is(err, billing.ErrQuoteKeyMissing) {
		t.Errorf("expected ErrQuoteKeyMissing, got %v", err)
	}
	// VerifyIntegrity likewise refuses an empty key
	if err := quote.VerifyIntegrity(time.Now().UTC(), nil); !errors.Is(err, billing.ErrQuoteKeyMissing) {
		t.Errorf("expected ErrQuoteKeyMissing, got %v", err)
	}
	// Digests are deterministic under the same key and differ across keys
	d1 := quote.ComputeDigest(quoteKey)
	if d1 != quote.Digest {
		t.Error("digest not reproducible under same key")
	}
	if d1 == quote.ComputeDigest([]byte("another-key-that-is-long-enough-for-testing")) {
		t.Error("digest identical across different keys")
	}
}
