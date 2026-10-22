package postfix

import (
	"fmt"
	"strings"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

func isValidEmail(addr string) bool {
	parts := strings.Split(addr, "@")
	if len(parts) != 2 {
		return false
	}
	local, domain := parts[0], parts[1]
	if len(local) == 0 || len(local) > 64 || !validation.LocalPartRegex.MatchString(local) {
		return false
	}
	return validation.IsValidHostname(domain)
}

// validateNodeConfig enforces structural constraints on the node parameters
// that end up verbatim inside main.cf / master.cf. Whitespace and control
// characters would allow directive injection, so they are rejected here.
func validateNodeConfig(node NodeConfig) error {
	if !validation.IsValidHostname(node.Hostname) {
		return fmt.Errorf("%w: invalid hostname %q", ErrInvalidConfig, node.Hostname)
	}
	if !validation.IsValidHostname(node.PrimaryDomain) {
		return fmt.Errorf("%w: invalid primary domain %q", ErrInvalidConfig, node.PrimaryDomain)
	}
	if err := validation.ValidateAbsolutePath(node.StorageRoot); err != nil {
		return fmt.Errorf("%w: storage root: %v", ErrInvalidConfig, err)
	}
	if err := validation.ValidateAbsolutePath(node.TLSCertFile); err != nil {
		return fmt.Errorf("%w: TLS certificate file: %v", ErrInvalidConfig, err)
	}
	if err := validation.ValidateAbsolutePath(node.TLSKeyFile); err != nil {
		return fmt.Errorf("%w: TLS key file: %v", ErrInvalidConfig, err)
	}
	if node.PolicyEnabled {
		if err := validation.ValidateSocketPath(node.PolicySocket); err != nil {
			return fmt.Errorf("%w: policy socket: %v", ErrInvalidConfig, err)
		}
	}
	if node.ArchiveEnabled {
		if err := validation.ValidateSocketPath(node.ArchiveSocket); err != nil {
			return fmt.Errorf("%w: archive socket: %v", ErrInvalidConfig, err)
		}
	}
	return nil
}

// Validate checks the node configuration and snapshot for consistency and correctness.
func Validate(node NodeConfig, snap Snapshot) error {
	// 1. Validate NodeConfig
	if err := validateNodeConfig(node); err != nil {
		return err
	}

	// 2. Validate Domains
	domainSet := make(map[string]bool, len(snap.Domains))
	for _, d := range snap.Domains {
		if d.Status != "active" {
			return fmt.Errorf("%w: domain %s has status %s", ErrInvalidStatus, d.FQDN, d.Status)
		}
		fqdn := strings.ToLower(d.FQDN)
		if !validation.IsValidHostname(fqdn) {
			return fmt.Errorf("%w: invalid domain FQDN %q", ErrInvalidDomain, d.FQDN)
		}
		if domainSet[fqdn] {
			return fmt.Errorf("%w: duplicate domain %q", ErrInvalidDomain, d.FQDN)
		}
		domainSet[fqdn] = true
	}

	// 3. Validate Mailboxes
	mailboxSet := make(map[string]bool, len(snap.Mailboxes))
	for _, m := range snap.Mailboxes {
		if m.Status != "active" {
			return fmt.Errorf("%w: mailbox %s has status %s", ErrInvalidStatus, m.Address, m.Status)
		}
		addr := strings.ToLower(m.Address)
		if !isValidEmail(addr) {
			return fmt.Errorf("%w: malformed address %q", ErrInvalidAddress, m.Address)
		}
		parts := strings.Split(addr, "@")
		domain := parts[1]
		if !domainSet[domain] {
			return fmt.Errorf("%w: mailbox %s belongs to domain %s not in active domains", ErrInvalidAddress, m.Address, domain)
		}
		if mailboxSet[addr] {
			return fmt.Errorf("%w: duplicate mailbox %q", ErrInvalidAddress, m.Address)
		}
		mailboxSet[addr] = true
	}

	// 4. Validate Aliases & Conflicts
	aliasKeySet := make(map[string]bool, len(snap.Aliases))
	aliasGraph := make(map[string][]string) // source_address -> []target_address

	for _, a := range snap.Aliases {
		domain := strings.ToLower(a.Domain)
		if !domainSet[domain] {
			return fmt.Errorf("%w: alias domain %s not in active domains", ErrInvalidDomain, a.Domain)
		}

		if a.Kind == "catchall" {
			if a.LocalPart != "" {
				return fmt.Errorf("%w: catchall alias LocalPart must be empty string, got %q", ErrInvalidAlias, a.LocalPart)
			}
		} else {
			if a.LocalPart == "" || !validation.LocalPartRegex.MatchString(a.LocalPart) {
				return fmt.Errorf("%w: invalid local_part %q", ErrInvalidAlias, a.LocalPart)
			}
		}

		// Target must be valid email
		target := strings.ToLower(a.Target)
		if !isValidEmail(target) {
			return fmt.Errorf("%w: invalid target %q", ErrInvalidTarget, a.Target)
		}

		// Conflict check: same (domain, local_part)
		aliasKey := fmt.Sprintf("%s@%s", strings.ToLower(a.LocalPart), domain)
		if aliasKeySet[aliasKey] {
			return fmt.Errorf("%w: duplicate alias for %s", ErrAliasConflict, aliasKey)
		}
		aliasKeySet[aliasKey] = true

		// Conflict check: alias with same name as a mailbox in the same domain
		if a.Kind != "catchall" {
			if mailboxSet[aliasKey] {
				return fmt.Errorf("%w: alias %s conflicts with existing mailbox", ErrAliasConflict, aliasKey)
			}
			aliasGraph[aliasKey] = append(aliasGraph[aliasKey], target)
		}
	}

	// 5. DFS Cycle Detection (excluding catchall)
	// Color map: 0 = unvisited, 1 = visiting (in recursion stack), 2 = visited
	visited := make(map[string]int)

	var checkCycle func(node string) error
	checkCycle = func(curr string) error {
		visited[curr] = 1 // in stack
		for _, next := range aliasGraph[curr] {
			state := visited[next]
			if state == 1 {
				return fmt.Errorf("%w: loop involving %s -> %s", ErrAliasCycle, curr, next)
			}
			if state == 0 {
				if err := checkCycle(next); err != nil {
					return err
				}
			}
		}
		visited[curr] = 2 // done
		return nil
	}

	for src := range aliasGraph {
		if visited[src] == 0 {
			if err := checkCycle(src); err != nil {
				return err
			}
		}
	}

	return nil
}
