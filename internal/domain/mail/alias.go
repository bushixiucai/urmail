package mail

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

var (
	ErrInvalidAlias    = errors.New("invalid alias")
	ErrAliasConflict   = errors.New("alias conflict")
	ErrAliasCycle      = errors.New("alias cycle detected")
	ErrInvalidAddress  = errors.New("invalid email address")
)

// AliasRule represents an alias for verification and graph validation.
type AliasRule struct {
	Domain    string
	LocalPart string // "" for catchall
	Target    string // local or external address
	Kind      string // alias|catchall|forward
}

// ValidateAliases checks a set of aliases for format, conflicts, and routing cycles.
// mailboxAddresses: all existing mailbox addresses in the scope (lowercase).
func ValidateAliases(aliases []AliasRule, mailboxAddresses []string) error {
	mailboxSet := make(map[string]bool, len(mailboxAddresses))
	for _, m := range mailboxAddresses {
		mailboxSet[strings.ToLower(m)] = true
	}

	aliasKeySet := make(map[string]bool, len(aliases))
	aliasGraph := make(map[string][]string)

	for _, a := range aliases {
		domain := strings.ToLower(a.Domain)
		if !validation.IsValidHostname(domain) {
			return fmt.Errorf("%w: invalid domain %q", ErrInvalidAlias, a.Domain)
		}

		if a.Kind == "catchall" {
			if a.LocalPart != "" {
				return fmt.Errorf("%w: catchall local_part must be empty string, got %q", ErrInvalidAlias, a.LocalPart)
			}
		} else {
			if a.LocalPart == "" || !validation.LocalPartRegex.MatchString(a.LocalPart) {
				return fmt.Errorf("%w: invalid local_part %q", ErrInvalidAlias, a.LocalPart)
			}
		}

		target := strings.ToLower(a.Target)
		parts := strings.Split(target, "@")
		if len(parts) != 2 || !validation.LocalPartRegex.MatchString(parts[0]) || !validation.IsValidHostname(parts[1]) {
			return fmt.Errorf("%w: invalid target %q", ErrInvalidAddress, a.Target)
		}

		// Conflict check: duplicate alias key
		aliasKey := fmt.Sprintf("%s@%s", strings.ToLower(a.LocalPart), domain)
		if aliasKeySet[aliasKey] {
			return fmt.Errorf("%w: duplicate alias for %s", ErrAliasConflict, aliasKey)
		}
		aliasKeySet[aliasKey] = true

		// Conflict check: alias name collides with existing mailbox
		if a.Kind != "catchall" {
			if mailboxSet[aliasKey] {
				return fmt.Errorf("%w: alias %s conflicts with existing mailbox", ErrAliasConflict, aliasKey)
			}
			aliasGraph[aliasKey] = append(aliasGraph[aliasKey], target)
		}
	}

	// DFS Cycle Detection (excluding catchall)
	// Color: 0 = unvisited, 1 = visiting (in recursion stack), 2 = visited
	visited := make(map[string]int)

	var checkCycle func(curr string) error
	checkCycle = func(curr string) error {
		visited[curr] = 1
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
		visited[curr] = 2
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
