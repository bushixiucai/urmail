package nsd

import (
	"fmt"
	"net"
	"strings"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// Validate checks the node configuration and zones.
func Validate(node NodeConfig, snap Snapshot) error {
	if !validation.IsValidHostname(node.Hostname) {
		return fmt.Errorf("%w: invalid hostname %q", ErrInvalidConfig, node.Hostname)
	}
	if !validation.IsValidHostname(node.PrimaryDomain) {
		return fmt.Errorf("%w: invalid primary domain %q", ErrInvalidConfig, node.PrimaryDomain)
	}
	if net.ParseIP(node.PublicIPv4) == nil {
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
	}
	return nil
}