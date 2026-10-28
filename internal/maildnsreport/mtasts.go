package maildnsreport

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

var policyIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
var extensionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)

// ErrInvalidMTASTS rejects a policy that cannot be safely published.
var ErrInvalidMTASTS = errors.New("invalid MTA-STS policy")

// MTASTSMode is the RFC 8461 policy mode.
type MTASTSMode string

// RFC 8461 policy modes.
const (
	MTASTSEnforce MTASTSMode = "enforce"
	MTASTSTesting MTASTSMode = "testing"
	MTASTSNone    MTASTSMode = "none"
)

// DefaultMTASTSMaxAge is the default policy lifetime (RFC 8461 recommends
// 86400 or more; the platform pins a day so policy changes propagate fast).
const DefaultMTASTSMaxAge = 86400

// MTASTSPolicy is the well-known MTA-STS policy body for one domain.
type MTASTSPolicy struct {
	// ID must match the id= in the _mta-sts TXT record (RFC 3339-ish
	// timestamp string, max 32 chars per RFC 8461).
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

// Validate checks the policy against RFC 8461 section 3.2.
func (p MTASTSPolicy) Validate() error {
	if !policyIDPattern.MatchString(p.ID) {
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
		host := strings.TrimSuffix(strings.TrimPrefix(mx, "*."), ".")
		if !validation.IsValidHostname(host) {
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
	return mtastsPrefix + "; id=" + id
}

// TLSRPTRecord renders the matching _smtp._tls TLS-RPT value.
func TLSRPTRecord(rua string) string {
	rua = strings.TrimSpace(rua)
	if rua != "" && !strings.HasPrefix(rua, "mailto:") && !strings.HasPrefix(rua, "https://") {
		rua = "mailto:" + rua
	}
	return tlsrptPrefix + "; rua=" + rua
}

// ValidTLSReportURI follows the two report delivery schemes in RFC 8460.
func ValidTLSReportURI(raw string) bool {
	if strings.ContainsAny(raw, "\r\n\t ") {
		return false
	}
	if strings.HasPrefix(raw, "mailto:") {
		return validMailbox(strings.TrimPrefix(raw, "mailto:"))
	}
	uri, err := url.Parse(raw)
	return err == nil && uri.Scheme == "https" && uri.Hostname() != "" && uri.User == nil && uri.Fragment == ""
}

// MTASTSRecordID validates the semicolon-delimited RFC 8461 TXT grammar.
func MTASTSRecordID(raw string) (string, bool) {
	if len(raw) > 2048 {
		return "", false
	}
	parts := strings.Split(raw, ";")
	if len(parts) < 2 || strings.TrimRight(parts[0], " \t") != "v=STSv1" {
		return "", false
	}
	seen := map[string]bool{"v": true}
	id := ""
	for index, part := range parts[1:] {
		part = strings.Trim(part, " \t")
		if part == "" {
			if index == len(parts)-2 {
				continue
			}
			return "", false
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok || seen[name] || !extensionNamePattern.MatchString(name) || value == "" {
			return "", false
		}
		seen[name] = true
		for _, r := range value {
			if r < 33 || r > 126 || r == '=' || r == ';' {
				return "", false
			}
		}
		if name == "id" {
			if !policyIDPattern.MatchString(value) {
				return "", false
			}
			id = value
		}
	}
	return id, id != ""
}

// ManagedTransportRecord permits explicit replacement of only the supported
// transport TXT/CNAME namespaces; unrelated records remain conflicts.
func ManagedTransportRecord(name, kind, value string) bool {
	switch name {
	case "_mta-sts":
		if kind != "TXT" {
			return false
		}
		_, ok := MTASTSRecordID(value)
		return ok
	case "_smtp._tls":
		return kind == "TXT" && strings.HasPrefix(value, "v=TLSRPTv1;") && ValidTLSReportURI(stsTag(value, "rua"))
	case "mta-sts":
		return kind == "CNAME" && validation.IsValidHostname(strings.TrimSuffix(value, "."))
	default:
		return false
	}
}
