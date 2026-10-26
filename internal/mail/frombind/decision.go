package frombind

// Decision is the outcome of the MAIL-04 header authorization check.
type Decision int

const (
	// Allow submits the message.
	Allow Decision = iota
	// RejectMissingFrom decides the submission carried no From header at all
	// (RFC 5322 requires exactly one).
	RejectMissingFrom
	// RejectMultipleFrom decides more than one From header was present (RFC
	// 5322 §3.6.2 allows exactly one; multiple headers are a smuggling vector).
	RejectMultipleFrom
	// RejectDomain decides the From domain is not granted to the
	// authenticated login.
	RejectDomain
	// AllowUnauthenticated decides the session is not authenticated; inbound
	// mail legitimately carries foreign From domains and is filtered by
	// SPF/DMARC at the recipient, not here.
	AllowUnauthenticated
)

// Reason returns the audit-ready machine reason for a decision.
func (d Decision) Reason() string {
	switch d {
	case RejectMissingFrom:
		return "missing_from_header"
	case RejectMultipleFrom:
		return "multiple_from_headers"
	case RejectDomain:
		return "from_domain_not_authorized"
	case AllowUnauthenticated:
		return "unauthenticated_inbound"
	default:
		return "allowed"
	}
}

// Decide is the pure MAIL-04 policy: for an authenticated session, there must
// be exactly one From header and its domain must be granted to the login.
// authenticated=false short-circuits to AllowUnauthenticated because inbound
// mail legitimately carries any From domain (envelope-side relay control and
// SPF/DMARC are handled elsewhere).
func Decide(authenticated bool, allowedDomains map[string]bool, fromHeaders []string) (Decision, string) {
	if !authenticated {
		return AllowUnauthenticated, "unauthenticated_inbound"
	}
	switch len(fromHeaders) {
	case 0:
		return RejectMissingFrom, "missing_from_header"
	case 1:
	default:
		return RejectMultipleFrom, "multiple_from_headers"
	}
	domain := ExtractFromDomain(fromHeaders[0])
	if domain == "" || !allowedDomains[domain] {
		return RejectDomain, "from_domain_not_authorized"
	}
	return Allow, "ok"
}
