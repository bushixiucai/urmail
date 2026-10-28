package nsd

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// recordTypes is the whitelist of resource record types a tenant may define.
// Anything else (SOA, NS edits on apex, ANY, unknown classes) is rejected:
// the values end up verbatim in a zone file, so this list is the injection
// boundary (M2 review P1-4).
var recordTypes = map[string]bool{
	"A":     true,
	"AAAA":  true,
	"CNAME": true,
	"MX":    true,
	"TXT":   true,
	"SPF":   true,
	"SRV":   true,
	"NS":    true,
	"CAA":   true,
}

// recordNameLabelPattern is the per-label grammar: letters, digits,
// underscores and hyphens (1..63 chars), per the M2 review P1-4 fix.
var recordNameLabelPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]([a-zA-Z0-9_-]{0,61}[a-zA-Z0-9_-])?$`)

// isValidRecordName accepts "@" (apex) or a dotted name whose labels each
// match recordNameLabelPattern (e.g. mail._domainkey, _sip._tcp). Spaces,
// quotes and semicolons anywhere in the name are rejected.
func isValidRecordName(name string) bool {
	if name == "@" {
		return true
	}
	if len(name) > 255 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !recordNameLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

// caaTagPattern restricts CAA property tags.
var caaTagPattern = regexp.MustCompile(`^(issue|issuewild|iodef)$`)

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

func isUint16(s string) bool {
	if !isNumeric(s) {
		return false
	}
	v, err := strconv.Atoi(s)
	return err == nil && v >= 0 && v <= 65535
}

// validateRecordValue checks the record value against its type. Values are
// rendered verbatim into the zone file, so each type has a strict grammar.
func validateRecordValue(r Record) error {
	value := r.Value
	switch r.Type {
	case "A":
		if !validation.IsValidIPv4(value) {
			return fmt.Errorf("invalid IPv4 address %q", value)
		}
	case "AAAA":
		if !validation.IsValidIPv6(value) {
			return fmt.Errorf("invalid IPv6 address %q", value)
		}
	case "CNAME", "MX", "NS":
		if !validation.IsValidDomainName(value) {
			return fmt.Errorf("invalid domain name %q", value)
		}
	case "TXT", "SPF":
		// Quoted-content rules: no embedded quotes, no line breaks. render.go
		// wraps the value in quotes, so these characters would break out of
		// the quoted string.
		if strings.ContainsAny(value, "\"\n\r") {
			return fmt.Errorf("TXT/SPF value must not contain double quotes or line breaks")
		}
		if value == "" {
			return fmt.Errorf("TXT/SPF value must not be empty")
		}
	case "SRV":
		// Four fields: priority weight port target
		parts := strings.Fields(value)
		if len(parts) != 4 {
			return fmt.Errorf("SRV value must have exactly 4 fields (priority weight port target), got %d", len(parts))
		}
		if !isUint16(parts[0]) || !isUint16(parts[1]) || !isUint16(parts[2]) {
			return fmt.Errorf("SRV priority/weight/port must be numeric 0-65535")
		}
		if !validation.IsValidDomainName(parts[3]) {
			return fmt.Errorf("SRV target %q is not a valid domain name", parts[3])
		}
	case "CAA":
		// Three fields: flags tag value, e.g. "0 issue letsencrypt.org"
		parts := strings.Fields(value)
		if len(parts) != 3 {
			return fmt.Errorf("CAA value must have exactly 3 fields (flags tag value), got %d", len(parts))
		}
		if parts[0] != "0" && parts[0] != "128" {
			return fmt.Errorf("CAA flags must be 0 or 128, got %q", parts[0])
		}
		if !caaTagPattern.MatchString(parts[1]) {
			return fmt.Errorf("CAA tag %q must be issue, issuewild or iodef", parts[1])
		}
		if strings.ContainsAny(parts[2], "\" \t\"") {
			return fmt.Errorf("CAA value %q must not contain whitespace or quotes", parts[2])
		}
	default:
		return fmt.Errorf("record type %q is not allowed", r.Type)
	}
	return nil
}

// validateRecord checks a single custom record.
func validateRecord(r Record) error {
	name := r.Name
	if name == "" {
		name = "@"
	}
	if !isValidRecordName(name) {
		return fmt.Errorf("%w: invalid record name %q", ErrInvalidRecord, r.Name)
	}
	if !recordTypes[r.Type] {
		return fmt.Errorf("%w: record type %q is not allowed", ErrInvalidRecord, r.Type)
	}
	if r.TTL < 0 || r.TTL > 2147483647 {
		return fmt.Errorf("%w: record TTL %d out of range", ErrInvalidRecord, r.TTL)
	}
	if err := validateRecordValue(r); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidRecord, err.Error())
	}
	return nil
}

// Validate checks the node configuration and zones.
func Validate(node NodeConfig, snap Snapshot) error {
	if len(node.ListenAddresses) > 64 {
		return fmt.Errorf("%w: too many DNS listeners", ErrInvalidConfig)
	}
	for _, address := range node.ListenAddresses {
		ip := net.ParseIP(address)
		if ip == nil || (!ip.IsGlobalUnicast() && !ip.Equal(net.ParseIP("127.0.0.1")) && !ip.Equal(net.IPv6loopback)) {
			return fmt.Errorf("%w: invalid DNS listen IP", ErrInvalidConfig)
		}
	}
	if !validation.IsValidHostname(node.Hostname) {
		return fmt.Errorf("%w: invalid hostname %q", ErrInvalidConfig, node.Hostname)
	}
	if !validation.IsValidHostname(node.PrimaryDomain) {
		return fmt.Errorf("%w: invalid primary domain %q", ErrInvalidConfig, node.PrimaryDomain)
	}
	if !validation.IsValidIPv4(node.PublicIPv4) {
		return fmt.Errorf("%w: invalid public IPv4 %q", ErrInvalidConfig, node.PublicIPv4)
	}
	if len(node.Nameservers) == 0 {
		return fmt.Errorf("%w: at least one nameserver is required", ErrInvalidConfig)
	}
	for _, ns := range node.Nameservers {
		if !validation.IsValidHostname(ns) {
			return fmt.Errorf("%w: invalid nameserver %q", ErrInvalidConfig, ns)
		}
	}

	seenZones := make(map[string]bool, len(snap.Zones))
	for _, z := range snap.Zones {
		if z.SigningVersion != "" && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(z.SigningVersion) {
			return fmt.Errorf("%w: invalid signing generation", ErrInvalidZone)
		}
		if (z.Sign || z.Secondary != nil) && z.Mode != "managed" {
			return fmt.Errorf("%w: DNS security requires a managed authoritative zone", ErrInvalidZone)
		}
		if z.Secondary != nil {
			if err := validateSecondary(*z.Secondary); err != nil {
				return err
			}
		}
		domain := strings.ToLower(strings.TrimSuffix(z.Domain, "."))
		if !validation.IsValidHostname(domain) {
			return fmt.Errorf("%w: invalid domain %q", ErrInvalidZone, z.Domain)
		}
		if seenZones[domain] {
			return fmt.Errorf("%w: duplicate zone %q", ErrInvalidZone, z.Domain)
		}
		seenZones[domain] = true

		if z.Mode != "managed" && z.Mode != "delegated" {
			return fmt.Errorf("%w: mode must be managed or delegated, got %q", ErrInvalidZone, z.Mode)
		}

		for _, r := range z.Records {
			if err := validateRecord(r); err != nil {
				return fmt.Errorf("%w: zone %s: %w", ErrInvalidZone, domain, err)
			}
		}
	}
	return nil
}
