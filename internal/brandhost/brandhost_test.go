package brandhost

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustChallenge(t *testing.T, host string, now time.Time) Challenge {
	t.Helper()
	c, err := NewChallenge(host, now)
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	return c
}

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Mail.Example.com", "mail.example.com", true},
		{"mail.example.com.", "mail.example.com", true},
		{" mail.example.com ", "mail.example.com", true},
		{"", "", false},
		{"mail.example.com:8443", "", false},
		{"-bad.example.com", "", false},
		{"und ae.example.com", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeHost(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("NormalizeHost(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestChallengeRoundTripAndSingleUse(t *testing.T) {
	now := time.Now()
	ch := mustChallenge(t, "brand.example.com", now)
	if ch.Hostname != "brand.example.com" || len(ch.Token) < 40 {
		t.Fatalf("unexpected challenge %+v", ch)
	}
	if !strings.HasPrefix(ch.TXTName(), "_urmail-brand-ownership.brand.example.com") {
		t.Fatalf("unexpected TXT name %q", ch.TXTName())
	}
	if !strings.HasPrefix(ch.TXTValue(), "urmail-brand-verify=") {
		t.Fatalf("unexpected TXT value %q", ch.TXTValue())
	}
	record := ChallengeRecord{TokenDigest: ch.Digest, ExpiresAt: ch.Expires}
	if err := CheckChallenge(record, ch.Token, now.Add(time.Minute)); err != nil {
		t.Fatalf("fresh token rejected: %v", err)
	}
}

func TestChallengeRejections(t *testing.T) {
	now := time.Now()
	ch := mustChallenge(t, "brand.example.com", now)
	base := ChallengeRecord{TokenDigest: ch.Digest, ExpiresAt: ch.Expires}

	if err := CheckChallenge(base, "wrong-token", now.Add(time.Minute)); !errors.Is(err, ErrChallenge) || !strings.Contains(err.Error(), FailTokenMissing) {
		t.Errorf("wrong token: got %v", err)
	}
	expired := base
	expired.ExpiresAt = now.Add(-time.Second)
	if err := CheckChallenge(expired, ch.Token, now); !errors.Is(err, ErrChallenge) || !strings.Contains(err.Error(), FailExpired) {
		t.Errorf("expired: got %v", err)
	}
	consumedAt := now.Add(-time.Minute)
	used := base
	used.ConsumedAt = &consumedAt
	// Hostile replay: even the exactly-correct token must be refused once used.
	if err := CheckChallenge(used, ch.Token, now); !errors.Is(err, ErrChallenge) || !strings.Contains(err.Error(), FailReused) {
		t.Errorf("replay: got %v", err)
	}
	// Digest mismatch: a token from another host must not verify here.
	other := mustChallenge(t, "other.example.com", now)
	if err := CheckChallenge(base, other.Token, now.Add(time.Minute)); err == nil {
		t.Errorf("cross-host token accepted")
	}
	// Expiry is bounded by ChallengeTTL.
	if ch.Expires.Sub(now) != ChallengeTTL {
		t.Errorf("expiry = %v want %v", ch.Expires.Sub(now), ChallengeTTL)
	}
}

func TestNewChallengeRejectsInvalidHost(t *testing.T) {
	if _, err := NewChallenge("bad host", time.Now()); !errors.Is(err, ErrChallenge) {
		t.Errorf("invalid host: got %v", err)
	}
}

func TestChallengeTokensAreUnique(t *testing.T) {
	now := time.Now()
	a := mustChallenge(t, "a.example.com", now)
	b := mustChallenge(t, "a.example.com", now)
	if a.Token == b.Token || a.Digest == b.Digest {
		t.Fatalf("challenge tokens repeat")
	}
}

func lifecycleState() HostState {
	return HostState{Hostname: "brand.example.com", Status: StatusPendingVerify}
}

func TestTransitionHappyPath(t *testing.T) {
	now := time.Now()
	state := lifecycleState()
	next, err := Transition(state, EventOwnershipVerified, now, "")
	if err != nil || next.Status != StatusVerified {
		t.Fatalf("verify: %v %+v", err, next)
	}
	next, err = Transition(next, EventCertIssued, now.Add(time.Minute), "")
	if err != nil || next.Status != StatusCertIssued {
		t.Fatalf("issue: %v %+v", err, next)
	}
	next, err = Transition(next, EventActivated, now.Add(2*time.Minute), "")
	if err != nil || next.Status != StatusActive {
		t.Fatalf("activate: %v %+v", err, next)
	}
	if next.VerifiedAt == nil || next.CertIssuedAt == nil || next.ActivatedAt == nil {
		t.Fatalf("timestamps not recorded: %+v", next)
	}
}

func TestTransitionSkipsAndReversalsRejected(t *testing.T) {
	now := time.Now()
	// Cannot activate before a certificate exists.
	if _, err := Transition(lifecycleState(), EventActivated, now, ""); err == nil {
		t.Errorf("activation from pending-verify accepted")
	}
	// Cannot activate straight after verification.
	verified, _ := Transition(lifecycleState(), EventOwnershipVerified, now, "")
	if _, err := Transition(verified, EventActivated, now, ""); err == nil {
		t.Errorf("activation from verified accepted")
	}
	// Verified hosts cannot re-verify.
	if _, err := Transition(verified, EventOwnershipVerified, now, ""); err == nil {
		t.Errorf("re-verification accepted")
	}
	// Unknown event.
	if _, err := Transition(lifecycleState(), Event("explode"), now, ""); err == nil {
		t.Errorf("unknown event accepted")
	}
}

func TestTransitionFailureAndRecovery(t *testing.T) {
	now := time.Now()
	verified, _ := Transition(lifecycleState(), EventOwnershipVerified, now, "")
	failed, err := Transition(verified, EventCertFailed, now, FailCert)
	if err != nil || failed.Status != StatusFailed || failed.FailureReason != FailCert {
		t.Fatalf("cert failure: %v %+v", err, failed)
	}
	if failed.Status.CertEligible() {
		t.Errorf("failed host still cert eligible")
	}
	// Repair path: re-verify after fixing the failure reason.
	recovered, err := Transition(failed, EventOwnershipVerified, now.Add(time.Minute), "")
	if err != nil || recovered.Status != StatusVerified || recovered.FailureReason != "" {
		t.Fatalf("recovery: %v %+v", err, recovered)
	}
	if recovered.VerifiedAt == nil || !recovered.VerifiedAt.After(now) {
		t.Errorf("recovery did not refresh verified_at: %+v", recovered)
	}
}

func TestTransitionRenewalFromActive(t *testing.T) {
	now := time.Now()
	state := HostState{Hostname: "h.example.com", Status: StatusActive, ActivatedAt: &now}
	renewed, err := Transition(state, EventCertIssued, now.Add(time.Hour), "")
	if err != nil || renewed.Status != StatusCertIssued || renewed.ActivatedAt == nil {
		t.Fatalf("renewal: %v %+v", err, renewed)
	}
}

func TestStatusEligibility(t *testing.T) {
	cases := map[Status][2]bool{
		StatusPendingVerify: {false, false},
		StatusVerified:      {true, false},
		StatusCertIssued:    {true, false},
		StatusActive:        {true, true},
		StatusFailed:        {false, false},
	}
	for status, want := range cases {
		if got := status.CertEligible(); got != want[0] {
			t.Errorf("%s.CertEligible()=%v", status, got)
		}
		if got := status.ServingEligible(); got != want[1] {
			t.Errorf("%s.ServingEligible()=%v", status, got)
		}
	}
}

func TestRenderRouting(t *testing.T) {
	out, err := RenderRouting(HostRoute{Hostname: "Brand.Example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server_name brand.example.com;", "proxy_pass https://127.0.0.1:443;", "acme-challenge"} {
		if !strings.Contains(out, want) {
			t.Errorf("routing snippet missing %q: %s", want, out)
		}
	}
	if _, err := RenderRouting(HostRoute{Hostname: "not a host"}); err == nil {
		t.Errorf("invalid host routing accepted")
	}
}
