// Package frombind implements the MAIL-04 authorization decision: an
// authenticated submission may only claim RFC5322 From addresses that the
// authoritative sender-login map grants to that login. The inputs are pure
// data so the decision can be unit-tested without Postfix or the database.
package frombind

import (
	"strings"
)

// ParseSenderLoginMap parses the generated sender_login.map (address whitespace logins)
// and returns the reverse index: login -> set of lowercase addresses it may
// send as. Blank lines and comment lines are skipped.
func ParseSenderLoginMap(raw string) map[string]map[string]bool {
	allowed := make(map[string]map[string]bool)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		separator := strings.IndexAny(line, " \t")
		if separator < 0 {
			continue
		}
		address := strings.ToLower(line[:separator])
		logins := strings.TrimSpace(line[separator:])
		// The generator joins logins with commas (postfix maps.go).
		for _, login := range strings.Split(logins, ",") {
			login = strings.ToLower(strings.TrimSpace(login))
			if login == "" {
				continue
			}
			if allowed[login] == nil {
				allowed[login] = make(map[string]bool)
			}
			allowed[login][address] = true
		}
	}
	return allowed
}

// ExtractFromDomain returns the domain of a From header value (everything
// after the last @) lowercased, or "" when the value has no domain part.
// Both bare addresses ("user@domain") and display-name forms
// ("Name <user@domain>") are accepted; anything after a closing ">" is
// discarded.
func ExtractFromDomain(headerValue string) string {
	v := strings.TrimSpace(headerValue)
	if gt := strings.IndexByte(v, '>'); gt >= 0 {
		v = v[:gt]
	}
	at := strings.LastIndex(v, "@")
	if at < 0 || at == len(v)-1 {
		return ""
	}
	return strings.ToLower(v[at+1:])
}

// AllowedFromDomains narrows a login's allowed addresses to the set of
// domains they imply.
func AllowedFromDomains(allowedAddresses map[string]bool) map[string]bool {
	domains := make(map[string]bool)
	for address := range allowedAddresses {
		if d := ExtractFromDomain(address); d != "" {
			domains[d] = true
		}
	}
	return domains
}
