package maildnsreport

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidMTASTS is returned for an MTA-STS policy that cannot be served.
var ErrInvalidMTASTS = errors.New("invalid MTA-STS policy")

// MTASTSMode is the RFC 8460 policy mode.
type MTASTSMode string

// RFC 8460 policy modes.
const (
	MTASTSEnforce MTASTSMode = "enforce"
	MTASTSTesting MTASTSMode = "testing"
	MTASTSNone    MTASTSMode = "none"
)

// DefaultMTASTSMaxAge is the default policy lifetime (RFC 8460 recommends
// 86400 or more; the platform pins a day so policy changes propagate fast).
const DefaultMTASTSMaxAge = 86400

// MTASTSPolicy is the well-known MTA-STS policy body for one domain.
type MTASTSPolicy struct {
	// ID must match the id= in the _mta-sts TXT record (RFC 3339-ish
	// timestamp string, max 32 chars per RFC 8460).
	ID string
	// Mode: enforce, testing or none.
	Mode MTASTSPolicyMode
	// MX hosts allowed to receive mail for the domain (trailing dot
	// optional, rendered without).
	MX []string
	// MaxAgeSeconds in [1, 31557600]. 0 selects DefaultMTASTSMaxAge.
	MaxAgeSeconds int
}

// MTASTSPolicyMode aliases MTASTSMode for readability at call sites.
type MTASTSPolicyMode = MTASTSMode

// Validate checks the policy against RFC 8460 section 3.2.
func (p MTASTSPolicy) Validate() error {
	if strings.TrimSpace(p.ID) == "" || len(p.ID) > 32 {
		return fmt.Errorf("%w: policy id must be 1..32 characters, got %q", ErrInvalidMTASTS, p.ID)
	}
	switch p.Mode {
	case MTASTSEnforce, MTASTSTesting, MTASTSNone:
	default:
		return fmt.Errorf("%w: mode must be enforce, testing or none, got %q", ErrInvalidMTASTS, p.Mode)
	}
	if len(p.MX) == 0 {
		return fmt.Errorf("%w: at least one mx entry is required", ErrInvalidMTASTS)
	}
	for _, mx := range p.MX {
		if strings.TrimSpace(mx) == "" || strings.ContainsAny(mx, " \t\r\n") {
			return fmt.Errorf("%w: mx entry %q must be a single hostname", ErrInvalidMTASTS, mx)
		}
		if strings.Contains(mx, "..") {
			return fmt.Errorf("%w: mx entry %q is not a valid hostname", ErrInvalidMTASTS, mx)
		}
	}
	maxAge := p.MaxAgeSeconds
	if maxAge == 0 {
		maxAge = DefaultMTASTSMaxAge
	}
	if maxAge < 1 || maxAge > 31557600 {
		return fmt.Errorf("%w: max_age %d outside 1..31557600", ErrInvalidMTASTS, maxAge)
	}
	return nil
}

// RenderWellKnown renders the MTA-STS policy body served at
// https://mta-sts.<domain>/.well-known/mta-sts.txt. Pure; golden-tested.
func RenderWellKnown(p MTASTSPolicy) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	maxAge := p.MaxAgeSeconds
	if maxAge == 0 {
		maxAge = DefaultMTASTSMaxAge
	}
	var b strings.Builder
	b.WriteString("version: STSv1\n")
	fmt.Fprintf(&b, "mode: %s\n", p.Mode)
	for _, mx := range p.MX {
		fmt.Fprintf(&b, "mx: %s\n", strings.TrimSuffix(strings.ToLower(mx), "."))
	}
	fmt.Fprintf(&b, "max_age: %d\n", maxAge)
	return b.String(), nil
}

// MTASTSTXTRecord renders the matching _mta-sts TXT value.
func MTASTSTXTRecord(id string) string {
	return mtastsPrefix + " id=" + id
}

// TLSRPTRecord renders the matching _smtp._tls TLS-RPT value.
func TLSRPTRecord(rua string) string {
	rua = strings.TrimSpace(rua)
	if rua != "" && !strings.HasPrefix(rua, "mailto:") {
		rua = "mailto:" + rua
	}
	return tlsrptPrefix + "; rua=" + rua
}
