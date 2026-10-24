package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
)

// Supported billing cycles.
const (
	CycleMonthly   = "monthly"
	CycleQuarterly = "quarterly"
	CycleAnnual    = "annual"
)

// Pricing domain errors.
var (
	ErrInvalidCycle     = errors.New("invalid billing cycle")
	ErrInvalidPlanPrice = errors.New("invalid plan price")
	ErrInvalidPeriod    = errors.New("invalid subscription period")
	ErrQuoteExpired     = errors.New("quote has expired")
	ErrQuoteTampered    = errors.New("quote integrity check failed")
	ErrQuoteKeyMissing  = errors.New("quote signing key is not configured or too short")
)

// CalculateRenewalPeriod computes the start and end timestamps for a subscription.
// If currentPeriodEnd is after now (an active subscription renewed early), the new
// period begins at currentPeriodEnd, preserving all remaining days. Otherwise, it
// begins at now.
func CalculateRenewalPeriod(now, currentPeriodEnd time.Time, cycle string) (time.Time, time.Time, error) {
	if now.IsZero() {
		now = time.Now()
	}
	start := now
	if !currentPeriodEnd.IsZero() && currentPeriodEnd.After(now) {
		start = currentPeriodEnd
	}

	end, err := AddCycle(start, cycle)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, end, nil
}

// AddCycle adds the specified billing cycle duration to a timestamp,
// cleanly respecting calendar month and year boundaries.
func AddCycle(t time.Time, cycle string) (time.Time, error) {
	months := 0
	switch cycle {
	case CycleMonthly:
		months = 1
	case CycleQuarterly:
		months = 3
	case CycleAnnual, "yearly":
		months = 12
	default:
		return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidCycle, cycle)
	}
	// Construct from the first of the month to avoid AddDate normalizing
	// January 31 into March. This primitive clamps one step; retaining an
	// original day across multiple periods requires explicit lifecycle state.
	target := time.Date(t.Year(), t.Month()+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	last := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	day := min(t.Day(), last)
	return time.Date(target.Year(), target.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()), nil
}

// CycleMultiplier returns the number of months and standard discount basis points
// for each supported cycle.
// - Monthly: 1 month, 0 discount (0 bps)
// - Quarterly: 3 months, 5% discount (500 bps)
// - Annual: 12 months, charged as 10 months (approx 16.67% discount, 1667 bps)
func CycleMultiplier(cycle string) (months int64, discountBps int64, err error) {
	switch cycle {
	case CycleMonthly:
		return 1, 0, nil
	case CycleQuarterly:
		return 3, 500, nil
	case CycleAnnual, "yearly":
		return 12, 1667, nil
	default:
		return 0, 0, fmt.Errorf("%w: %s", ErrInvalidCycle, cycle)
	}
}

// CalculateBasePrice computes the undiscounted base price in cents for a given monthly price and cycle.
func CalculateBasePrice(monthlyPriceCents int64, cycle string) (int64, error) {
	if monthlyPriceCents < 0 || monthlyPriceCents > math.MaxInt64/10 {
		return 0, ErrInvalidPlanPrice
	}
	switch cycle {
	case CycleMonthly:
		return monthlyPriceCents, nil
	case CycleQuarterly:
		// Bound the intermediate product as well as the annual total.
		if monthlyPriceCents > math.MaxInt64/30 {
			return 0, ErrInvalidPlanPrice
		}
		nominal := monthlyPriceCents * 3
		discount := new(big.Int).Quo(new(big.Int).Mul(big.NewInt(nominal), big.NewInt(500)), big.NewInt(10000)).Int64()
		return nominal - discount, nil
	case CycleAnnual, "yearly":
		// 12 months charged as 10 months: 10 * price
		return monthlyPriceCents * 10, nil
	default:
		return 0, fmt.Errorf("%w: %s", ErrInvalidCycle, cycle)
	}
}

// CalculateUpgradeProration computes the unused monetary value of the current subscription
// and the net amount to charge for upgrading to a new plan.
// currentCycle is the cycle the tenant is billed on TODAY and newCycle the one
// they are switching to: pricing the current plan with the NEW cycle would
// multiply a monthly tenant's remaining credit by the annual factor (M5
// review P2-2). The credit is computed in integer arithmetic; the fraction is
// scaled first so no float rounds money.
// Returns (proratedCreditCents, netPayableCents, error).
func CalculateUpgradeProration(
	now time.Time,
	currentPeriodStart, currentPeriodEnd time.Time,
	currentMonthlyPriceCents int64,
	newMonthlyPriceCents int64,
	currentCycle, newCycle string,
) (int64, int64, error) {
	if currentPeriodEnd.Before(currentPeriodStart) || currentPeriodStart.IsZero() || currentPeriodEnd.IsZero() {
		return 0, 0, ErrInvalidPeriod
	}
	if currentMonthlyPriceCents < 0 || newMonthlyPriceCents < 0 {
		return 0, 0, ErrInvalidPlanPrice
	}

	newBase, err := CalculateBasePrice(newMonthlyPriceCents, newCycle)
	if err != nil {
		return 0, 0, err
	}

	// If already past period end, no unused credit remains.
	if !now.Before(currentPeriodEnd) {
		return 0, newBase, nil
	}

	totalDuration := currentPeriodEnd.Sub(currentPeriodStart)
	remainingDuration := currentPeriodEnd.Sub(now)
	if totalDuration <= 0 {
		return 0, newBase, nil
	}

	// The current plan is priced on the cycle it was actually sold on.
	currentBase, err := CalculateBasePrice(currentMonthlyPriceCents, currentCycle)
	if err != nil {
		return 0, 0, err
	}

	// Unused credit proportional to remaining time, computed in big.Int so a
	// nanosecond-scaled product cannot overflow: credit = currentBase *
	// remaining / total, rounded half up.
	num := new(big.Int).Mul(big.NewInt(currentBase), big.NewInt(remainingDuration.Nanoseconds()))
	den := big.NewInt(totalDuration.Nanoseconds())
	quotient, remainder := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Mul(remainder, big.NewInt(2))
	if twice.Cmp(den) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	proratedCredit := quotient.Int64()
	if proratedCredit > currentBase {
		proratedCredit = currentBase
	}

	netPayable := newBase - proratedCredit
	if netPayable < 0 {
		netPayable = 0
	}
	return proratedCredit, netPayable, nil
}

// Quote carries an immutable pricing snapshot with an integrity digest.
type Quote struct {
	Calendar               *CalendarSpec `json:"calendar,omitempty"`
	PlanSnapshotDigest     string        `json:"plan_snapshot_digest,omitempty"`
	DocumentSnapshotDigest string        `json:"document_snapshot_digest,omitempty"`
	DomainID               int64         `json:"domain_id,omitempty"`
	TenantID               int64         `json:"tenant_id"`
	Kind                   string        `json:"kind,omitempty"`
	PriceVersion           int64         `json:"price_version,omitempty"`
	PlanAmountCents        int64         `json:"plan_amount_cents,omitempty"`
	SubscriptionVersion    int64         `json:"subscription_version,omitempty"`
	CouponCode             string        `json:"coupon_code,omitempty"`
	PlanCode               string        `json:"plan_code"`
	BillingCycle           string        `json:"billing_cycle"`
	Currency               string        `json:"currency"`
	BaseAmountCents        int64         `json:"base_amount_cents"`
	DiscountCents          int64         `json:"discount_cents"`
	ProratedCreditCents    int64         `json:"prorated_credit_cents"`
	FinalAmountCents       int64         `json:"final_amount_cents"`
	PeriodStart            time.Time     `json:"period_start"`
	PeriodEnd              time.Time     `json:"period_end"`
	GeneratedAt            time.Time     `json:"generated_at"`
	ExpiresAt              time.Time     `json:"expires_at"`
	Digest                 string        `json:"digest"`
}

// BuildQuote generates an immutable, tamper-evident Quote.
func BuildQuote(
	tenantID int64,
	couponCode string,
	planCode string,
	monthlyPriceCents int64,
	cycle string,
	currency string,
	couponKind string,
	couponValue int64,
	creditCents int64,
	periodStart, periodEnd time.Time,
	ttl time.Duration,
	key []byte,
) (Quote, error) {
	if len(key) < 32 {
		return Quote{}, ErrQuoteKeyMissing
	}
	if tenantID <= 0 || strings.TrimSpace(planCode) == "" || strings.TrimSpace(currency) == "" {
		return Quote{}, ErrInvalidPlanPrice
	}
	basePrice, err := CalculateBasePrice(monthlyPriceCents, cycle)
	if err != nil {
		return Quote{}, err
	}

	return BuildPricedQuote(tenantID, couponCode, planCode, basePrice, cycle, currency, couponKind, couponValue, creditCents, periodStart, periodEnd, ttl, key)
}

// BuildPricedQuote signs a configured total; it never derives a quarterly or annual price.
func BuildPricedQuote(tenantID int64, couponCode, planCode string, basePrice int64, cycle, currency, couponKind string, couponValue, creditCents int64, periodStart, periodEnd time.Time, ttl time.Duration, key []byte) (Quote, error) {
	if len(key) < 32 {
		return Quote{}, ErrQuoteKeyMissing
	}
	if tenantID <= 0 || planCode == "" || (currency != "CNY" && currency != "USD") || basePrice < 0 || basePrice > 9007199254740991 {
		return Quote{}, ErrInvalidPlanPrice
	}
	if _, err := AddCycle(periodStart, cycle); err != nil {
		return Quote{}, err
	}
	if !periodEnd.After(periodStart) {
		return Quote{}, ErrInvalidPeriod
	}
	discountCents := int64(0)
	if creditCents < 0 || creditCents > basePrice {
		return Quote{}, ErrInvalidPlanPrice
	}
	if couponKind != "" && couponValue > 0 {
		if couponKind == CouponKindPercent && couponValue > 100 {
			return Quote{}, ErrInvalidPlanPrice
		}
		discounted, err := ApplyDiscount(basePrice, couponKind, couponValue)
		if err != nil {
			return Quote{}, err
		}
		discountCents = basePrice - discounted
	}

	finalAmount := basePrice - discountCents - creditCents
	if finalAmount < 0 {
		finalAmount = 0
	}

	now := time.Now().UTC()
	if ttl <= 0 {
		ttl = 30 * time.Minute // default 30 min quote validity
	}
	expiresAt := now.Add(ttl)

	q := Quote{
		TenantID:            tenantID,
		CouponCode:          couponCode,
		PlanCode:            planCode,
		BillingCycle:        cycle,
		Currency:            currency,
		BaseAmountCents:     basePrice,
		DiscountCents:       discountCents,
		ProratedCreditCents: creditCents,
		FinalAmountCents:    finalAmount,
		PeriodStart:         periodStart.UTC(),
		PeriodEnd:           periodEnd.UTC(),
		GeneratedAt:         now,
		ExpiresAt:           expiresAt,
	}
	q.Digest = q.ComputeDigest(key)
	return q, nil
}

// canonical returns the pipe-delimited canonical representation of the
// integrity-relevant quote fields. Timestamps are covered at nanosecond
// resolution: a second-granularity digest left the sub-second part of every
// time outside the HMAC, so a quote could be shifted by up to a second
// without invalidating the signature (M5 review 3.25).
func (q *Quote) canonical() string {
	canonical := fmt.Sprintf("%q|%q|%d|%d|%q|%d|%d|%d|%q|%q|%q|%q|%d|%d|%d|%d|%d|%d|%d|%d",
		q.PlanSnapshotDigest, q.DocumentSnapshotDigest,
		q.DomainID,
		q.TenantID,
		q.Kind,
		q.PriceVersion,
		q.PlanAmountCents,
		q.SubscriptionVersion,
		q.CouponCode,
		q.PlanCode,
		q.BillingCycle,
		q.Currency,
		q.BaseAmountCents,
		q.DiscountCents,
		q.ProratedCreditCents,
		q.FinalAmountCents,
		q.PeriodStart.UnixNano(),
		q.PeriodEnd.UnixNano(),
		q.GeneratedAt.UnixNano(),
		q.ExpiresAt.UnixNano(),
	)
	if q.Calendar != nil {
		raw, _ := json.Marshal(q.Calendar)
		canonical += "|calendar:" + string(raw)
	}
	return canonical
}

// ComputeDigest computes an HMAC-SHA256 integrity tag over canonical quote
// fields using the platform quote signing key.
func (q *Quote) ComputeDigest(key []byte) string {
	if len(key) < 32 {
		// BuildQuote and VerifyIntegrity reject missing or weak keys.
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(q.canonical()))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyIntegrity checks the quote HMAC with the given signing key and
// whether it has expired.
func (q *Quote) VerifyIntegrity(now time.Time, key []byte) error {
	if len(key) < 32 {
		return ErrQuoteKeyMissing
	}
	if now.IsZero() {
		now = time.Now()
	}
	if now.After(q.ExpiresAt) {
		return ErrQuoteExpired
	}
	if !q.ExpiresAt.After(q.GeneratedAt) || q.ExpiresAt.Sub(q.GeneratedAt) > 24*time.Hour {
		return ErrQuoteTampered
	}
	expected := q.ComputeDigest(key)
	if !hmac.Equal([]byte(expected), []byte(q.Digest)) {
		return ErrQuoteTampered
	}
	return nil
}

// VerifySignature authenticates an existing intent without applying a new-order expiry check.
func (q *Quote) VerifySignature(key []byte) error {
	if len(key) < 32 {
		return ErrQuoteKeyMissing
	}
	if !hmac.Equal([]byte(q.ComputeDigest(key)), []byte(q.Digest)) {
		return ErrQuoteTampered
	}
	return nil
}

// UpgradeDifference preserves existing discounts and rounds the remaining
// catalog-price difference exactly once, in the currency's smallest unit.
func UpgradeDifference(currentBase, targetBase int64, start, end, now time.Time) (int64, error) {
	if currentBase < 0 || targetBase < currentBase || targetBase > 9007199254740991 {
		return 0, ErrInvalidPlanPrice
	}
	if !end.After(start) || now.Before(start) || !now.Before(end) {
		return 0, ErrInvalidPeriod
	}
	numerator := new(big.Int).Mul(big.NewInt(targetBase-currentBase), big.NewInt(end.Sub(now).Nanoseconds()))
	denominator := big.NewInt(end.Sub(start).Nanoseconds())
	rounded, remainder := new(big.Int).QuoRem(numerator, denominator, new(big.Int))
	if new(big.Int).Lsh(remainder, 1).Cmp(denominator) >= 0 {
		rounded.Add(rounded, big.NewInt(1))
	}
	return rounded.Int64(), nil
}
