package maildnsreport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Publication is the public, immutable input shared by DNS and HTTPS renderers.
type Publication struct {
	Policy         MTASTSPolicy
	RUA            string
	Enabled        bool
	CacheSafeAfter time.Time
}

// Digest binds every public publication value used by an observation.
func (p Publication) Digest() string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CheckMXChange prevents a configured host change while senders may still hold
// an enforcing policy. Operators publish mode=none and drain the old max_age
// before changing the receiving host, then begin testing the new policy.
func (p Publication) CheckMXChange(host string, now time.Time) error {
	if !p.Enabled || slices.Contains(p.Policy.MX, host) {
		return nil
	}
	if p.Policy.Mode == MTASTSNone && !p.CacheSafeAfter.IsZero() && !now.Before(p.CacheSafeAfter) {
		return nil
	}
	return errors.New("TRANSPORT_POLICY_CACHE_DRAIN_REQUIRED")
}

// DNSRecords holds only the three public records owned by this publication.
func (p Publication) DNSRecords(host string) []RecordSpec {
	if !p.Enabled {
		return nil
	}
	return []RecordSpec{{Name: "_mta-sts", Type: "TXT", Value: MTASTSTXTRecord(p.Policy.ID)}, {Name: "_smtp._tls", Type: "TXT", Value: TLSRPTRecord(p.RUA)}, {Name: "mta-sts", Type: "CNAME", Value: strings.TrimSuffix(host, ".") + "."}}
}
