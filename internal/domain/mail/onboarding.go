package mail

import (
	"errors"
	"time"
)

// Onboarding steps.
const (
	StepDomainVerification = "domain_verification"
	StepDNSPropagation     = "dns_propagation"
	StepCertificateIssue   = "certificate_issue"
	StepMailboxProvision   = "mailbox_provision"
	StepReady              = "ready"
)

// Onboarding statuses.
const (
	OnboardingStatusPending   = "pending"
	OnboardingStatusRunning   = "running"
	OnboardingStatusCompleted = "completed"
	OnboardingStatusFailed    = "failed"
	OnboardingStatusBlocked   = "blocked"
)

// Onboarding domain errors.
var (
	ErrTaskAlreadyCompleted = errors.New("onboarding task is already completed")
	ErrMaxAttemptsExceeded  = errors.New("maximum onboarding attempts exceeded")
	ErrDomainUnverified     = errors.New("domain ownership verification failed")
	ErrDNSConflict          = errors.New("dns record conflict detected")
)

// NextStep returns the subsequent step in the onboarding lifecycle.
func NextStep(current string) (string, bool) {
	switch current {
	case StepDomainVerification:
		return StepDNSPropagation, true
	case StepDNSPropagation:
		return StepCertificateIssue, true
	case StepCertificateIssue:
		return StepMailboxProvision, true
	case StepMailboxProvision:
		return StepReady, true
	case StepReady:
		return StepReady, false
	default:
		return "", false
	}
}

// VerificationChallenge represents a DNS TXT challenge string to prove domain ownership.
type VerificationChallenge struct {
	RecordName string
	Value      string
	ExpiresAt  time.Time
}

// GenerateVerificationChallenge creates an ownership challenge for onboarding.
func GenerateVerificationChallenge(fqdn, token string, ttl time.Duration) VerificationChallenge {
	return VerificationChallenge{
		RecordName: "_urmail-challenge." + fqdn,
		Value:      token,
		ExpiresAt:  time.Now().UTC().Add(ttl),
	}
}

// VerifyOwnershipChallenge matches resolved TXT record against expected challenge token.
func VerifyOwnershipChallenge(resolvedValues []string, expectedToken string) bool {
	for _, val := range resolvedValues {
		if val == expectedToken {
			return true
		}
	}
	return false
}
