// Package frombind implements the MAIL-04 authorization decision: an
// authenticated submission may only claim RFC5322 From addresses that the
// authoritative sender-login map grants to that login. The inputs are pure
// data so the decision can be unit-tested without Postfix or the database.
package frombind

import (
	"net/mail"
	"strings"

	"golang.org/x/net/idna"
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

// ExtractFromDomain parses exactly one RFC 5322 mailbox. Group/list syntax,
// control characters and unsupported SMTPUTF8 local parts fail closed.
func ExtractFromDomain(headerValue string) string {
	if len(headerValue) > 8192 || strings.ContainsAny(headerValue, "\x00\r\n") || hasGroupSyntax(headerValue) {
		return ""
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(headerValue))
	if err != nil {
		return ""
	}
	at := strings.LastIndexByte(parsed.Address, '@')
	if at <= 0 || at == len(parsed.Address)-1 {
		return ""
	}
	for _, ch := range parsed.Address[:at] {
		if ch < 0x21 || ch > 0x7e {
			return ""
		}
	}
	domain, err := idna.Lookup.ToASCII(strings.TrimSuffix(parsed.Address[at+1:], "."))
	if err != nil || domain == "" || strings.ContainsAny(domain, "[] \t") {
		return ""
	}
	return strings.ToLower(domain)
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

// The standard parser accepts a one-member RFC group as an Address. Groups
// are deliberately outside the submission contract, even with one mailbox.
func hasGroupSyntax(value string) bool {
	quoted, escaped, comments := false, false, 0
	for _, r := range value {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if comments > 0 {
			if r == '(' {
				comments++
			}
			if r == ')' {
				comments--
			}
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if r == '(' {
			comments++
			continue
		}
		if r == ':' || r == ';' {
			return true
		}
	}
	return false
}
