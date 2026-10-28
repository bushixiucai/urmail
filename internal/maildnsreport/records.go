package maildnsreport

import (
	"fmt"
	"strings"
)

// RequiredRecords returns the transport-security DNS records MAIL-008
// requires for a domain, ready to hand to the dnsprovider plan layer:
//
//   - _mta-sts TXT  : MTA-STS policy advertisement
//   - _smtp._tls TXT: TLS-RPT report destination
//
// SPF/DKIM/DMARC are intentionally NOT duplicated here: they already have a
// single source of truth in dnsprovider.GenerateMailRecords; compose the two
// planners at the reconcile layer. Pure function.
func RequiredRecords(domain, policyID, mode string, mx []string, tlsRptRUA string) ([]RecordSpec, error) {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" {
		return nil, fmt.Errorf("%w: domain is required", ErrInvalidMTASTS)
	}
	if strings.TrimSpace(policyID) == "" {
		return nil, fmt.Errorf("%w: MTA-STS policy id is required", ErrInvalidMTASTS)
	}
	policy := MTASTSPolicy{
		ID:   policyID,
		Mode: MTASTSPolicyMode(modeOrTesting(mode)),
		MX:   mx,
	}
	if _, err := RenderWellKnown(policy); err != nil {
		return nil, err
	}
	uri := strings.TrimPrefix(TLSRPTRecord(tlsRptRUA), tlsrptPrefix+"; rua=")
	if !ValidTLSReportURI(uri) {
		return nil, fmt.Errorf("%w: valid report destination is required", ErrInvalidMTASTS)
	}
	return []RecordSpec{
		{Name: "_mta-sts", Type: "TXT", Value: MTASTSTXTRecord(policyID), Reason: "MTA-STS policy advertisement (RFC 8461)"},
		{Name: "_smtp._tls", Type: "TXT", Value: TLSRPTRecord(tlsRptRUA), Reason: "TLS-RPT report destination (RFC 8460)"},
	}, nil
}

func modeOrTesting(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return string(MTASTSTesting)
	}
	return mode
}

// RecordSpec is a required record with the reason it is required.
type RecordSpec struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// WellKnownPath is the URL path the HTTP layer must serve the MTA-STS body
// at for every domain (documented wiring; handlers are owned elsewhere).
const WellKnownPath = "/.well-known/mta-sts.txt"
