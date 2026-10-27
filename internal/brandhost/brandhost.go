// Package brandhost owns the brand-host ownership and TLS lifecycle model.
//
// The logic here is pure: every decision is a function of its explicit
// inputs, so persistence (PostgreSQL), issuance (the agentd certificate
// pipeline) and routing (generated nginx snippets) stay thin adapters.
//
// Ownership method (documented choice): DNS TXT. The operator publishes one
// single-use random token at
//
//	_urmail-brand-ownership.<hostname>  TXT  "urmail-brand-verify=<token>"
//
// DNS is chosen over the HTTP token method because a brand host may not yet
// have any HTTP frontend (and must never trust an unverified Host header),
// while the tenant already controls DNS for the delegated hostname.
package brandhost

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// Status is the brand-host lifecycle state.
type Status string

// The lifecycle: pending-verify -> verified -> cert-issued -> active.
// failed is a terminal-until-repaired side state reachable from any state;
// revoked is expressed by deleting the binding (existing behavior).
const (
	StatusPendingVerify Status = "pending-verify"
	StatusVerified      Status = "verified"
	StatusCertIssued    Status = "cert-issued"
	StatusActive        Status = "active"
	StatusFailed        Status = "failed"
)

// Failure reasons. They describe the closure step that failed; none of them
// embed tenant-controlled free text.
const (
	FailExpired      = "challenge_expired"
	FailTokenMissing = "challenge_token_missing"
	FailReused       = "challenge_token_reused"
	FailCert         = "certificate_issuance_failed"
	FailRenew        = "certificate_renewal_failed"
	FailInvalidHost  = "invalid_hostname"
)

// CertEligible reports whether a host in the given status may be issued (or,
// for an already-issued host, renewed) a certificate through the agent
// pipeline. Ownership must be proven before any issuance.
func (s Status) CertEligible() bool {
	switch s {
	case StatusVerified, StatusCertIssued, StatusActive:
		return true
	}
	return false
}

// ServingEligible reports whether the proxy may route the hostname to the
// brand entry point. TLS must be ready first.
func (s Status) ServingEligible() bool { return s == StatusActive }

// Challenge is a freshly issued ownership challenge. Token is revealed
// exactly once; only Digest is persisted.
type Challenge struct {
	Hostname string
	Token    string
	Digest   string
	Method   string
	Expires  time.Time
}

// ChallengeTTL bounds how long an operator has to publish the TXT record.
const ChallengeTTL = 24 * time.Hour

// ErrChallenge is the base error for every verification rejection.
var ErrChallenge = errors.New("brand host challenge rejected")

// TXTName returns the DNS record name the operator must publish.
func (c Challenge) TXTName() string { return "_urmail-brand-ownership." + c.Hostname }

// TXTValue returns the full TXT record value.
func (c Challenge) TXTValue() string { return "urmail-brand-verify=" + c.Token }

// DigestToken hashes a presented token the same way persistence stores it.
func DigestToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NormalizeHost lowercases and trims a candidate hostname and applies the
// shared RFC 1123 grammar. Uppercase input is normalized, not rejected, so a
// paste from a DNS console cannot wedge the state machine.
func NormalizeHost(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(host), ".")))
	if !validation.IsValidHostname(host) || strings.Contains(host, ":") {
		return "", false
	}
	return host, true
}

// NewChallenge generates a 256-bit single-use challenge for hostname.
func NewChallenge(hostname string, now time.Time) (Challenge, error) {
	normalized, ok := NormalizeHost(hostname)
	if !ok {
		return Challenge{}, fmt.Errorf("%w: %s", ErrChallenge, FailInvalidHost)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Challenge{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return Challenge{
		Hostname: normalized,
		Token:    token,
		Digest:   DigestToken(token),
		Method:   "dns-txt",
		Expires:  now.Add(ChallengeTTL),
	}, nil
}

// ChallengeRecord is the persisted view of a challenge the verifier checks
// against: the stored digest, its expiry and whether it was already used.
type ChallengeRecord struct {
	TokenDigest string
	ExpiresAt   time.Time
	ConsumedAt  *time.Time
}

// CheckChallenge verifies a presented TXT token against the persisted
// record. Single use and expiry are enforced here, not by the caller.
func CheckChallenge(record ChallengeRecord, presented string, now time.Time) error {
	if record.ConsumedAt != nil {
		return fmt.Errorf("%w: %s", ErrChallenge, FailReused)
	}
	if now.After(record.ExpiresAt) {
		return fmt.Errorf("%w: %s", ErrChallenge, FailExpired)
	}
	if DigestToken(presented) != record.TokenDigest {
		return fmt.Errorf("%w: %s", ErrChallenge, FailTokenMissing)
	}
	return nil
}

// HostState is the observable lifecycle state of one brand host.
type HostState struct {
	Hostname      string
	Status        Status
	FailureReason string
	VerifiedAt    *time.Time
	CertIssuedAt  *time.Time
	ActivatedAt   *time.Time
}

// Event drives a lifecycle transition.
type Event string

// Event labels emitted into the audit trail for brand-host transitions.
const (
	EventOwnershipVerified Event = "ownership_verified"
	EventCertIssued        Event = "cert_issued"
	EventCertFailed        Event = "cert_failed"
	EventActivated         Event = "activated"
	EventRepairFailed      Event = "repair_failed"
)

// Transition applies one lifecycle event to the current state and returns
// the next state. Every legal path is explicit; anything else is rejected so
// a host can never serve traffic that skipped ownership or TLS.
func Transition(state HostState, event Event, now time.Time, failureReason string) (HostState, error) {
	next := state
	next.FailureReason = ""
	verifiedAt, certIssuedAt, activatedAt := state.VerifiedAt, state.CertIssuedAt, state.ActivatedAt
	switch event {
	case EventOwnershipVerified:
		if state.Status != StatusPendingVerify && state.Status != StatusFailed {
			return HostState{}, fmt.Errorf("ownership verify is not valid from status %q", state.Status)
		}
		next.Status = StatusVerified
		verifiedAt = &now
	case EventCertIssued:
		if state.Status != StatusVerified && state.Status != StatusCertIssued && state.Status != StatusActive {
			return HostState{}, fmt.Errorf("cert issuance is not valid from status %q", state.Status)
		}
		next.Status = StatusCertIssued
		certIssuedAt = &now
	case EventActivated:
		if state.Status != StatusCertIssued {
			return HostState{}, fmt.Errorf("activation is not valid from status %q", state.Status)
		}
		next.Status = StatusActive
		activatedAt = &now
	case EventCertFailed:
		if state.Status != StatusVerified && state.Status != StatusCertIssued && state.Status != StatusActive {
			return HostState{}, fmt.Errorf("cert failure is not valid from status %q", state.Status)
		}
		next.Status = StatusFailed
		next.FailureReason = failureReason
	case EventRepairFailed:
		if state.Status != StatusFailed {
			return HostState{}, fmt.Errorf("repair failure is not valid from status %q", state.Status)
		}
		next.Status = StatusFailed
		next.FailureReason = failureReason
	default:
		return HostState{}, fmt.Errorf("unknown brand-host event %q", event)
	}
	next.VerifiedAt, next.CertIssuedAt, next.ActivatedAt = verifiedAt, certIssuedAt, activatedAt
	return next, nil
}

// HostRoute is the routing input for one active brand host.
type HostRoute struct {
	Hostname      string
	PanelUpstream string
	APIUpstream   string
}

// RenderRouting renders the nginx location snippet that serves the brand
// host. It is a pure function so the reconcile layer can golden-test it; the
// snippet is written by the agent through its existing whitelisted
// file.write operation into /etc/nginx/conf.d/urmail/.
func RenderRouting(route HostRoute) (string, error) {
	normalized, ok := NormalizeHost(route.Hostname)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrChallenge, FailInvalidHost)
	}
	panel := route.PanelUpstream
	if panel == "" {
		panel = "127.0.0.1:443"
	}
	api := route.APIUpstream
	if api == "" {
		api = panel
	}
	var b strings.Builder
	b.WriteString("# managed by urmail: brand host routing (OPS-101)\n")
	b.WriteString("server_name " + normalized + ";\n")
	b.WriteString("location = /.well-known/acme-challenge/ { root " + ACMEWebrootConst + "; }\n")
	b.WriteString("location / { proxy_pass https://" + panel + "; proxy_set_header Host $host; }\n")
	b.WriteString("location /api/ { proxy_pass https://" + api + "; proxy_set_header Host $host; }\n")
	return b.String(), nil
}

// ACMEWebrootConst mirrors the platform ACME webroot used by the certificate
// pipeline.
const ACMEWebrootConst = "/var/lib/urmail/acme"
