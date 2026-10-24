package billing

import "time"

const (
	// EntitlementStandard is a finite free, trial or commercial subscription.
	EntitlementStandard = "standard"
	// EntitlementMigration preserves explicitly audited pre-existing mail service.
	EntitlementMigration = "migration_legacy"
)

// ServiceEntitlement distinguishes a missing end from a zero or infinite timestamp.
// Migration grants are not orders, trial delivery or proof of payment.
type ServiceEntitlement struct {
	Source         string
	ManifestSHA256 string
	Status         string
	PeriodStart    time.Time
	PeriodEnd      *time.Time
	TrialEndsAt    *time.Time
}

// CurrentAt never treats a missing standard end or an unknown source as service.
func (e ServiceEntitlement) CurrentAt(now time.Time) bool {
	if e.PeriodStart.IsZero() || e.PeriodStart.After(now) {
		return false
	}
	if e.Source == EntitlementMigration {
		if e.Status != StatusActive || e.PeriodEnd != nil || e.TrialEndsAt != nil || len(e.ManifestSHA256) != 64 {
			return false
		}
		for _, value := range e.ManifestSHA256 {
			if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
				return false
			}
		}
		return true
	}
	if e.Source != "" && e.Source != EntitlementStandard || e.ManifestSHA256 != "" {
		return false
	}
	if e.Status != StatusActive && e.Status != StatusTrialing && e.Status != StatusPastDue {
		return false
	}
	if e.PeriodEnd == nil || e.PeriodEnd.IsZero() || !e.PeriodEnd.After(now) {
		return false
	}
	return e.Status != StatusTrialing || e.TrialEndsAt == nil || e.TrialEndsAt.After(now)
}

// QuoteSourceMatches accepts old finite quotes as standard, never as migration grants.
func QuoteSourceMatches(quoted, current string) bool {
	if quoted == "" {
		quoted = EntitlementStandard
	}
	if current == "" {
		current = EntitlementStandard
	}
	return quoted == current && (current == EntitlementStandard || current == EntitlementMigration)
}
