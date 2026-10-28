// Package onboarding coordinates verified domain DNS and durable provisioning.
package onboarding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/maildnsreport"
)

// Action is an immutable, explicit change. No plan deletes tenant records.
type Action struct {
	Kind   string                    `json:"kind"`
	Before *dnsprovider.RemoteRecord `json:"before,omitempty"`
	After  dnsprovider.Record        `json:"after"`
}

// Changes keeps conflicts reviewable; a conflicted plan cannot be confirmed.
type Changes struct {
	Actions   []Action `json:"actions"`
	Conflicts []string `json:"conflicts"`
}

// Digest hashes canonical input; it never includes a credential or private key.
func Digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func canonical(record dnsprovider.Record) dnsprovider.Record {
	record.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(record.Name), "."))
	record.Type = dnsprovider.RecordType(strings.ToUpper(string(record.Type)))
	if record.Type == dnsprovider.RecordTypeMX || record.Type == dnsprovider.RecordTypeCNAME || record.Type == "NS" {
		record.Value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(record.Value), "."))
	}
	return record
}

// SnapshotDigest is insensitive to provider pagination order while retaining
// upstream IDs, TTLs and every pre-existing website and third-party record.
func SnapshotDigest(records []dnsprovider.RemoteRecord) string {
	copyRecords := append([]dnsprovider.RemoteRecord(nil), records...)
	for i := range copyRecords {
		copyRecords[i].Record = canonical(copyRecords[i].Record)
	}
	sort.Slice(copyRecords, func(i, j int) bool { return Digest(copyRecords[i]) < Digest(copyRecords[j]) })
	return Digest(copyRecords)
}

// EqualValue compares record identity/data without forcing an existing TTL to
// change merely because the generated suggestion has a different TTL.
func EqualValue(a, b dnsprovider.Record) bool {
	a, b = canonical(a), canonical(b)
	a.TTL, b.TTL = 0, 0
	return a == b
}

// BuildChanges preserves unrelated records and existing SPF qualifiers. A
// different MX/DKIM/DMARC/CNAME requires manual resolution, never replacement.
func BuildChanges(domain string, existing []dnsprovider.RemoteRecord, desired []dnsprovider.Record) (Changes, error) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	out := Changes{Actions: []Action{}, Conflicts: []string{}}
	if domain == "" || len(existing) > 5000 || len(desired) > 1000 {
		return out, errors.New("DNS_PLAN_INVALID")
	}
	for _, wanted := range desired {
		if wanted.Name == "@" {
			wanted.Name = domain
		}
		wanted = canonical(wanted)
		if wanted.Name != domain && !strings.HasSuffix(wanted.Name, "."+domain) {
			return out, errors.New("DNS_RECORD_OUTSIDE_OWNED_DOMAIN")
		}
		if strings.TrimSpace(wanted.Value) == "" {
			return out, errors.New("DNS_RECORD_VALUE_REQUIRED")
		}
		if strings.HasPrefix(wanted.Name, "_urmail-verify.") || strings.ContainsAny(wanted.Name+wanted.Value, "\r\n") {
			return out, errors.New("DNS_OWNERSHIP_CHALLENGE_NOT_MANAGED")
		}
		var candidates []dnsprovider.RemoteRecord
		equal := false
		for _, raw := range existing {
			r := raw
			r.Record = canonical(r.Record)
			if r.Name != wanted.Name {
				continue
			}
			if (r.Proxied || r.Disabled) && (r.Type == wanted.Type || wanted.Type == "CNAME") {
				candidates = append(candidates, r)
				continue
			}
			if EqualValue(r.Record, wanted) || (wanted.Type == "TXT" && strings.HasPrefix(strings.ToLower(wanted.Value), "v=dmarc1") && validDMARC(r.Value)) {
				equal = true
				continue
			}
			if r.Type == dnsprovider.RecordTypeCNAME || wanted.Type == dnsprovider.RecordTypeCNAME {
				candidates = append(candidates, r)
				continue
			}
			if r.Type != wanted.Type {
				continue
			}
			if wanted.Type == dnsprovider.RecordTypeTXT {
				prefix := strings.ToLower(strings.Fields(wanted.Value)[0])
				value := strings.ToLower(strings.TrimSpace(r.Value))
				if prefix == "v=spf1" && !strings.HasPrefix(value, "v=spf1") {
					continue
				}
				if strings.HasPrefix(prefix, "v=dmarc1") && !strings.HasPrefix(value, "v=dmarc1") {
					continue
				}
			}
			candidates = append(candidates, r)
		}
		if equal && len(candidates) == 0 {
			continue
		}
		if equal {
			out.Conflicts = append(out.Conflicts, string(wanted.Type)+" "+wanted.Name+" has duplicate incompatible records")
			continue
		}
		if len(candidates) == 0 {
			out.Actions = append(out.Actions, Action{Kind: "create", After: wanted})
			continue
		}
		relative := strings.TrimSuffix(wanted.Name, "."+domain)
		if len(candidates) == 1 && !candidates[0].Proxied && !candidates[0].Disabled &&
			maildnsreport.ManagedTransportRecord(relative, string(wanted.Type), wanted.Value) &&
			maildnsreport.ManagedTransportRecord(relative, string(candidates[0].Type), candidates[0].Value) {
			old := candidates[0]
			out.Actions = append(out.Actions, Action{Kind: "update", Before: &old, After: wanted})
			continue
		}
		if wanted.Type == dnsprovider.RecordTypeTXT && strings.HasPrefix(strings.ToLower(wanted.Value), "v=spf1") && len(candidates) == 1 && candidates[0].Type == dnsprovider.RecordTypeTXT {
			merged, conflict, _ := dnsprovider.MergeSPF(candidates[0].Value, "mx")
			if !conflict {
				wanted.Value = merged
				if !EqualValue(candidates[0].Record, wanted) {
					old := candidates[0]
					out.Actions = append(out.Actions, Action{Kind: "update", Before: &old, After: wanted})
				}
				continue
			}
		}
		out.Conflicts = append(out.Conflicts, string(wanted.Type)+" "+wanted.Name+" requires manual resolution")
	}
	return out, nil
}

func validDMARC(value string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(value), ";")
	if !strings.EqualFold(strings.TrimSpace(first), "v=DMARC1") {
		return false
	}
	tags := map[string]string{}
	for _, part := range strings.Split(value, ";") {
		key, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if _, exists := tags[key]; exists {
			return false
		}
		tags[key] = strings.ToLower(strings.TrimSpace(v))
	}
	return tags["v"] == "dmarc1" && (tags["p"] == "quarantine" || tags["p"] == "reject")
}
