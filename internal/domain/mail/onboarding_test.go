package mail_test

import (
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/mail"
)

func TestOnboardingNextStep(t *testing.T) {
	steps := []string{
		mail.StepDomainVerification,
		mail.StepDNSPropagation,
		mail.StepCertificateIssue,
		mail.StepMailboxProvision,
		mail.StepReady,
	}

	current := steps[0]
	for i := 0; i < len(steps)-1; i++ {
		next, hasNext := mail.NextStep(current)
		if !hasNext {
			t.Fatalf("expected hasNext=true at step %s", current)
		}
		if next != steps[i+1] {
			t.Errorf("expected next step %s, got %s", steps[i+1], next)
		}
		current = next
	}

	_, hasNext := mail.NextStep(mail.StepReady)
	if hasNext {
		t.Error("expected hasNext=false after ready step")
	}

	_, valid := mail.NextStep("invalid_step")
	if valid {
		t.Error("expected valid=false for invalid step")
	}
}

func TestVerificationChallenge(t *testing.T) {
	challenge := mail.GenerateVerificationChallenge("customer.example.com", "token-xyz123", 24*time.Hour)
	if challenge.RecordName != "_urmail-challenge.customer.example.com" {
		t.Errorf("unexpected record name: %s", challenge.RecordName)
	}
	if challenge.Value != "token-xyz123" {
		t.Errorf("unexpected value: %s", challenge.Value)
	}

	t.Run("valid challenge matches", func(t *testing.T) {
		resolved := []string{"unrelated-record", "token-xyz123"}
		if !mail.VerifyOwnershipChallenge(resolved, "token-xyz123") {
			t.Error("expected ownership challenge verification to succeed")
		}
	})

	t.Run("missing challenge fails", func(t *testing.T) {
		resolved := []string{"unrelated-record"}
		if mail.VerifyOwnershipChallenge(resolved, "token-xyz123") {
			t.Error("expected ownership challenge verification to fail")
		}
	})
}
