package frombind

import (
	"os"
	"strings"
)

// MapFileAuthorizer is the production Handler: it re-reads the generated
// sender_login.map on every decision. The map is small (one line per
// address), the read is cheap, and re-reading per decision means alias or
// mailbox changes take effect on the next submission without a reload signal.
type MapFileAuthorizer struct {
	// Path is the absolute path of the generated sender_login.map
	// (/etc/postfix/urmail/sender_login.map in production).
	Path string
}

// Authorize implements the milter Handler contract. A read failure yields an
// empty allowed set, which fails closed for authenticated submissions while
// still continuing for unauthenticated inbound mail.
func (a MapFileAuthorizer) Authorize(login string, fromHeaders []string) (reject bool, reason string) {
	raw, err := os.ReadFile(a.Path)
	if err != nil {
		raw = nil
	}
	allowedAddresses := ParseSenderLoginMap(string(raw))[strings.ToLower(login)]
	allowed := AllowedFromDomains(allowedAddresses)
	decision, reason := Decide(login != "", allowed, fromHeaders)
	return decision == RejectMissingFrom || decision == RejectMultipleFrom || decision == RejectDomain, reason
}
