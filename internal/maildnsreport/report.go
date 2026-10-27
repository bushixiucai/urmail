package maildnsreport

import (
	"fmt"
	"sort"
	"strings"
)

// Assess produces the MAIL-008 completeness report for one domain snapshot.
// Checks cover (in fixed order): SPF, DKIM, DMARC, MTA-STS TXT, TLS-RPT and
// the MTA-STS HTTPS well-known requirement. The result is a pure function of
// the snapshot; DANE/TLSA and SSHFP stay DEFERRED and are not assessed.
func Assess(s DomainState) Report {
	s.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s.Domain), "."))
	if s.ReportingAddress == "" {
		s.ReportingAddress = "dmarc-reports@" + s.Domain
	}

	checks := []Check{
		assessSPF(s),
		assessDKIM(s),
		assessDMARC(s),
		assessMTASTS(s),
		assessTLSRPT(s),
		assessMTASTSWellKnown(s),
	}
	complete := true
	for _, c := range checks {
		if c.Status != StatusOK {
			complete = false
		}
	}
	return Report{Domain: s.Domain, Complete: complete, Checks: checks}
}

// assessMTASTS checks the _mta-sts TXT record: v=STSv1 with a non-empty id.
func assessMTASTS(s DomainState) Check {
	found := txtValues(s, "_mta-sts", "TXT")
	check := Check{
		ID:          "mta_sts",
		Requirement: "MTA-STS TXT record at _mta-sts (" + mtastsPrefix + " id=<policy id>)",
		Found:       found,
		Expected:    []string{mtastsPrefix + " id=<yyyyymmddhhmmss>"},
	}
	if len(found) == 0 {
		check.Status = StatusMissing
		check.Detail = "No MTA-STS record at _mta-sts; sending MXs will not enforce SMTP TLS for this domain."
		return check
	}
	value := strings.TrimSpace(found[0])
	id := stsTag(value, "id")
	switch {
	case !strings.HasPrefix(strings.ToLower(value), strings.ToLower(mtastsPrefix)):
		check.Status = StatusPresentWrong
		check.Detail = "MTA-STS record does not start with " + mtastsPrefix + "."
	case id == "":
		check.Status = StatusPresentWrong
		check.Detail = "MTA-STS record has an empty id=; without a policy id the record is invalid per RFC 8460."
	default:
		check.Status = StatusOK
		check.Detail = "MTA-STS record present with policy id " + id + "."
	}
	return check
}

// assessTLSRPT checks the _smtp._tls TLS-RPT record.
func assessTLSRPT(s DomainState) Check {
	found := txtValues(s, "_smtp._tls", "TXT")
	check := Check{
		ID:          "tls_rpt",
		Requirement: "TLS-RPT record at _smtp._tls (" + tlsrptPrefix + " rua=...)",
		Found:       found,
		Expected:    []string{tlsrptPrefix + " rua=mailto:<report address>"},
	}
	if len(found) == 0 {
		check.Status = StatusMissing
		check.Detail = "No TLS-RPT record at _smtp._tls; TLS failures by sending MXs stay invisible."
		return check
	}
	value := strings.TrimSpace(found[0])
	rua := stsTag(value, "rua")
	switch {
	case !strings.HasPrefix(strings.ToLower(value), strings.ToLower(tlsrptPrefix)):
		check.Status = StatusPresentWrong
		check.Detail = "TLS-RPT record does not start with " + tlsrptPrefix + "."
	case rua == "":
		check.Status = StatusPresentWrong
		check.Detail = "TLS-RPT record has no rua= report destination."
	case !validMailbox(strings.TrimPrefix(rua, "mailto:")):
		check.Status = StatusPresentWrong
		check.Detail = "TLS-RPT rua=" + rua + " is not a valid mailto: report address."
	default:
		check.Status = StatusOK
		check.Detail = "TLS-RPT record present with report destination " + rua + "."
	}
	return check
}

// assessMTASTSWellKnown reports the HTTPS well-known requirement. From DNS
// data alone it is unknown; when the caller supplies the liveness probe
// result the check classifies it.
func assessMTASTSWellKnown(s DomainState) Check {
	url := "https://mta-sts." + s.Domain + "/.well-known/mta-sts.txt"
	check := Check{
		ID:          "mta_sts_well_known",
		Requirement: "MTA-STS policy served over HTTPS at " + url,
		Expected:    []string{"version: STSv1 + mode + mx + max_age, matching the _mta-sts id"},
	}
	if s.MTASTSWellKnownLive == nil {
		check.Status = StatusUnknown
		check.Detail = "Well-known liveness not probed in this snapshot; verify the HTTPS endpoint publishes the policy."
		return check
	}
	if !*s.MTASTSWellKnownLive {
		check.Status = StatusMissing
		check.Detail = "The _mta-sts TXT record advertises a policy but the HTTPS well-known body is missing or stale; per RFC 8460 senders must fail closed, so publish the policy file."
		return check
	}
	check.Status = StatusOK
	check.Detail = "MTA-STS well-known policy is reachable over HTTPS."
	return check
}

// stsTag extracts a key=value tag from an STS/TLSRPT record body.
func stsTag(record, tag string) string {
	for _, part := range strings.Split(record, ";") {
		for _, token := range strings.Fields(part) {
			if eq := strings.IndexByte(token, '='); eq > 0 && strings.EqualFold(token[:eq], tag) {
				return token[eq+1:]
			}
		}
	}
	return ""
}

// FormatTable renders the report as a fixed-width text table for CLI output.
// Pure function of the report.
func (r Report) FormatTable() string {
	var b strings.Builder
	status := "COMPLETE"
	if !r.Complete {
		status = "INCOMPLETE"
	}
	fmt.Fprintf(&b, "DNS policy report for %s: %s\n", r.Domain, status)
	fmt.Fprintf(&b, "%-22s %-14s %s\n", "CHECK", "STATUS", "DETAIL")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%-22s %-14s %s\n", c.ID, c.Status, c.Detail)
	}
	return b.String()
}

// SortChecks orders checks by ID (defensive copy) for stable rendering.
func (r Report) SortChecks() []Check {
	out := make([]Check, len(r.Checks))
	copy(out, r.Checks)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
