package postfix

import (
	"fmt"
	"strings"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// isValidEmail delegates to the shared canonical grammar so the generator can
// never diverge from what the API accepts (audit finding MAIL-01).
func isValidEmail(addr string) bool {
	return validation.IsValidEmail(addr)
}

// validateNodeConfig enforces structural constraints on the node parameters
// that end up verbatim inside main.cf / master.cf. Whitespace and control
// characters would allow directive injection, so they are rejected here.
func validateNodeConfig(node NodeConfig) error {
	if node.V80Preserve && !node.PolicyEnabled {
		return fmt.Errorf("%w: v80 preservation requires the SaaS policy service", ErrInvalidConfig)
	}
	if !validation.IsValidHostname(node.Hostname) {
		return fmt.Errorf("%w: invalid hostname %q", ErrInvalidConfig, node.Hostname)
	}
	if !validation.IsValidHostname(node.PrimaryDomain) {
		return fmt.Errorf("%w: invalid primary domain %q", ErrInvalidConfig, node.PrimaryDomain)
	}
	if err := validation.ValidateAbsolutePath(node.StorageRoot); err != nil {
		return fmt.Errorf("%w: storage root: %w", ErrInvalidConfig, err)
	}
	if err := validation.ValidateAbsolutePath(node.TLSCertFile); err != nil {
		return fmt.Errorf("%w: TLS certificate file: %w", ErrInvalidConfig, err)
	}
	if err := validation.ValidateAbsolutePath(node.TLSKeyFile); err != nil {
		return fmt.Errorf("%w: TLS key file: %w", ErrInvalidConfig, err)
	}
	if node.PolicyEnabled {
		if strings.ContainsFunc(node.PolicySocket, func(r rune) bool {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
				return false
			default:
				return !strings.ContainsRune("/._-", r)
			}
		}) {
			return fmt.Errorf("%w: policy socket must be a literal Unix path", ErrInvalidConfig)
		}
		if err := validation.ValidateSocketPath(node.PolicySocket); err != nil {
			return fmt.Errorf("%w: policy socket: %w", ErrInvalidConfig, err)
		}
	}
	// The archive transport is a pipe with an argv path, not a socket; the
	// legacy ArchiveSocket field is no longer required.
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

		// Explicit sender grants must name active mailboxes in this snapshot.
		if len(a.SenderLogins) > 100 || (a.Kind == "catchall" && len(a.SenderLogins) > 0) {
			return fmt.Errorf("%w: invalid alias sender grants", ErrInvalidAlias)
		}
		for _, login := range a.SenderLogins {
			if login != strings.ToLower(login) || !mailboxSet[login] {
				return fmt.Errorf("%w: sender grant must name an active mailbox", ErrInvalidAlias)
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
