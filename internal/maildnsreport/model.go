package maildnsreport

import (
	"net/mail"
	"strings"
)

// Record is one observed DNS record of a domain. Names are lowercase,
// relative to the domain apex ("@" for apex).
type Record struct {
	Name  string // "@", "_dmarc", "selector._domainkey", "_mta-sts", "_smtp._tls", ...
	Type  string // TXT, MX, CNAME, A, AAAA
	Value string // raw record value; for TXT the joined string content
}

// DKIMSelector is a required DKIM selector the tenant must publish.
type DKIMSelector struct {
	Name string // selector label, e.g. "mail"
}

// DomainState is the input snapshot for one domain.
type DomainState struct {
	Domain string
	// Records observed at the authoritative/public DNS.
	Records []Record
	// RequiredDKIMSelectors lists the selectors that must have a
	// <selector>._domainkey TXT record.
	RequiredDKIMSelectors []DKIMSelector
	// MTASTSWellKnownLive reports whether
	// https://mta-sts.<domain>/.well-known/mta-sts.txt returns the current
	// policy. nil means not checked yet (from DNS-only data).
	MTASTSWellKnownLive *bool
	// ReportingAddress is the RUA mailbox for TLS-RPT/DMARC aggregate
	// reports; defaults to dmarc-reports@<primary platform domain> when
	// the caller leaves it empty. It must be a valid mailbox.
	ReportingAddress string
}

// CheckStatus classifies one completeness check result.
type CheckStatus string

// Check classification values reported per completeness check.
const (
	StatusOK           CheckStatus = "ok"
	StatusMissing      CheckStatus = "missing"
	StatusPresentWrong CheckStatus = "present_wrong"
	StatusUnknown      CheckStatus = "unknown"
)

// Check is one classified requirement.
type Check struct {
	ID          string      `json:"id"`
	Requirement string      `json:"requirement"`
	Status      CheckStatus `json:"status"`
	Detail      string      `json:"detail"`
	Expected    []string    `json:"expected,omitempty"`
	Found       []string    `json:"found,omitempty"`
}

// Report is the completeness report for one domain.
type Report struct {
	Domain   string  `json:"domain"`
	Complete bool    `json:"complete"`
	Checks   []Check `json:"checks"`
}

// desired values published by the platform; reports compare against these.
const (
	spfRecord    = "v=spf1 mx -all"
	mtastsPrefix = "v=STSv1"
	tlsrptPrefix = "v=TLSRPTv1"
	dmarcPrefix  = "v=DMARC1"
)

func txtValues(s DomainState, name, rtype string) []string {
	var out []string
	for _, r := range s.Records {
		if strings.EqualFold(r.Name, name) && strings.EqualFold(r.Type, rtype) {
			out = append(out, r.Value)
		}
	}
	return out
}

// assessSPF checks the apex SPF TXT record. Exactly one v=spf1 record must
// exist and match the platform policy (multiple SPF records are an error).
func assessSPF(s DomainState) Check {
	found := txtValues(s, "@", "TXT")
	var spf []string
	for _, v := range found {
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(strings.ToLower(trimmed), "v=spf1") {
			spf = append(spf, trimmed)
		}
	}
	check := Check{ID: "spf", Requirement: "Apex TXT SPF record " + spfRecord, Found: spf}
	switch {
	case len(spf) == 0:
		check.Status = StatusMissing
		check.Detail = "No SPF record at the apex; receiving servers cannot authorize this domain's senders."
	case len(spf) > 1:
		check.Status = StatusPresentWrong
		check.Detail = "Multiple SPF records at the apex; RFC 7208 requires exactly one, so the policy is invalid."
	case strings.TrimSpace(spf[0]) != spfRecord:
		check.Status = StatusPresentWrong
		check.Detail = "SPF record does not match the platform policy " + spfRecord + "."
		check.Expected = []string{spfRecord}
	default:
		check.Status = StatusOK
		check.Detail = "SPF record matches the platform policy."
	}
	return check
}

// assessDKIM checks each required selector has a DKIM key TXT record.
func assessDKIM(s DomainState) Check {
	expected := make([]string, 0, len(s.RequiredDKIMSelectors))
	var found, missing []string
	for _, sel := range s.RequiredDKIMSelectors {
		name := strings.ToLower(sel.Name) + "._domainkey"
		expected = append(expected, name+" TXT v=DKIM1; ...")
		vals := txtValues(s, name, "TXT")
		ok := false
		for _, v := range vals {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "v=dkim1") {
				found = append(found, name+" ("+v+"...")
				ok = true
				break
			}
		}
		if !ok {
			missing = append(missing, name)
		}
	}
	check := Check{ID: "dkim", Requirement: "DKIM TXT record per selector at <selector>._domainkey", Expected: expected, Found: found}
	switch {
	case len(expected) == 0:
		check.Status = StatusUnknown
		check.Detail = "No DKIM selectors configured for this domain; nothing to verify."
	case len(missing) > 0:
		check.Status = StatusMissing
		check.Detail = "Missing DKIM records for selector(s): " + strings.Join(missing, ", ") + "."
	default:
		check.Status = StatusOK
		check.Detail = "All required DKIM selectors publish a v=DKIM1 record."
	}
	return check
}

// assessDMARC checks the _dmarc record: present, v=DMARC1, explicit p= that
// is at least quarantine, and a valid rua mailbox.
func assessDMARC(s DomainState) Check {
	found := txtValues(s, "_dmarc", "TXT")
	check := Check{ID: "dmarc", Requirement: "DMARC record at _dmarc with p=quarantine or p=reject and a valid rua", Found: found}
	if len(found) == 0 {
		check.Status = StatusMissing
		check.Detail = "No DMARC record at _dmarc."
		return check
	}
	policy := dmarcTag(found[0], "p")
	rua := dmarcTag(found[0], "rua")
	switch {
	case !strings.HasPrefix(strings.TrimSpace(found[0]), dmarcPrefix):
		check.Status = StatusPresentWrong
		check.Detail = "DMARC record does not start with " + dmarcPrefix + "."
	case policy == "":
		check.Status = StatusPresentWrong
		check.Detail = "DMARC record has no explicit p= tag; receivers treat it as p=none and no policy is enforced."
	case policy == "none":
		check.Status = StatusPresentWrong
		check.Detail = "DMARC policy p=none is monitoring only; the platform requires p=quarantine or p=reject."
	case policy != "quarantine" && policy != "reject":
		check.Status = StatusPresentWrong
		check.Detail = "DMARC policy p=" + policy + " is not a valid policy value."
	case rua != "" && !validMailbox(rua):
		check.Status = StatusPresentWrong
		check.Detail = "DMARC rua=" + rua + " is not a valid mailto: aggregate report address."
	default:
		check.Status = StatusOK
		check.Detail = "DMARC record present with policy p=" + policy + "."
	}
	return check
}

// dmarcTag extracts a tag value from a DMARC record body.
func dmarcTag(record, tag string) string {
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if eq := strings.IndexByte(part, '='); eq > 0 && strings.EqualFold(strings.TrimSpace(part[:eq]), tag) {
			return strings.TrimSpace(part[eq+1:])
		}
	}
	return ""
}

func validMailbox(addr string) bool {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "mailto:")
	// Strip size/time suffixes like "addr!10m".
	if bang := strings.IndexByte(addr, '!'); bang > 0 {
		addr = addr[:bang]
	}
	_, err := mail.ParseAddress(addr)
	return err == nil
}
