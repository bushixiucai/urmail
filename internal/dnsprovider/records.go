package dnsprovider

import (
	"fmt"
	"strings"
)

// RecordType represents standard DNS resource record types.
type RecordType string

// Supported DNS record types.
const (
	RecordTypeMX    RecordType = "MX"
	RecordTypeTXT   RecordType = "TXT"
	RecordTypeCNAME RecordType = "CNAME"
	RecordTypeA     RecordType = "A"
	RecordTypeAAAA  RecordType = "AAAA"
)

// DefaultTTL is the default TTL in seconds for generated DNS records.
const DefaultTTL = 300

// Record represents a single DNS resource record.
type Record struct {
	Type     RecordType `json:"type"`
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Priority int        `json:"priority,omitempty"`
	TTL      int        `json:"ttl,omitempty"`
}

// RecordConflict captures an incompatible difference between existing and desired records.
type RecordConflict struct {
	Existing Record `json:"existing"`
	Desired  Record `json:"desired"`
	Reason   string `json:"reason"`
}

// RecordPlan describes changes required to reconcile existing DNS records to desired state.
type RecordPlan struct {
	ToCreate  []Record         `json:"to_create"`
	ToUpdate  []Record         `json:"to_update"`
	ToDelete  []Record         `json:"to_delete"`
	Conflicts []RecordConflict `json:"conflicts"`
}

// GenerateMailRecords generates standard DNS records required for mail service on a domain:
// - MX: @ -> mailHost with priority 10
// - SPF: @ -> "v=spf1 mx -all" (hard fail; must match the guidance shown to tenants)
// - DKIM: selector._domainkey.fqdn -> "v=DKIM1; k=rsa; p=..."
// - DMARC: _dmarc.fqdn -> "v=DMARC1; p=..."
// - autoconfig: CNAME -> mailHost
// - autodiscover: CNAME -> mailHost
func GenerateMailRecords(fqdn, mailHost, dkimSelector, dkimPubKey, dmarcPolicy string) []Record {
	cleanFQDN := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(fqdn), "."))
	cleanMailHost := strings.TrimSuffix(strings.TrimSpace(mailHost), ".")

	// DKIM TXT record value
	dkimVal := dkimPubKey
	if !strings.HasPrefix(dkimVal, "v=DKIM1") {
		dkimVal = fmt.Sprintf("v=DKIM1; k=rsa; p=%s", dkimPubKey)
	}

	// DMARC TXT record value. The default policy is quarantine so the record
	// matches what the tenant DNS guidance advertises (M5 review P3-2); an
	// operator can still pass an explicit policy.
	dmarcVal := dmarcPolicy
	if dmarcVal == "" {
		dmarcVal = "v=DMARC1; p=quarantine"
	} else if !strings.HasPrefix(dmarcVal, "v=DMARC1") {
		dmarcVal = fmt.Sprintf("v=DMARC1; p=%s", dmarcPolicy)
	}

	// Subdomain names
	var dkimName, dmarcName, autoconfigName, autodiscoverName string
	if cleanFQDN == "" || cleanFQDN == "@" {
		dkimName = fmt.Sprintf("%s._domainkey", dkimSelector)
		dmarcName = "_dmarc"
		autoconfigName = "autoconfig"
		autodiscoverName = "autodiscover"
	} else {
		dkimName = fmt.Sprintf("%s._domainkey.%s", dkimSelector, cleanFQDN)
		dmarcName = fmt.Sprintf("_dmarc.%s", cleanFQDN)
		autoconfigName = fmt.Sprintf("autoconfig.%s", cleanFQDN)
		autodiscoverName = fmt.Sprintf("autodiscover.%s", cleanFQDN)
	}

	return []Record{
		{
			Type:     RecordTypeMX,
			Name:     "@",
			Value:    cleanMailHost,
			Priority: 10,
			TTL:      DefaultTTL,
		},
		{
			Type:     RecordTypeTXT,
			Name:     "@",
			Value:    "v=spf1 mx -all",
			Priority: 0,
			TTL:      DefaultTTL,
		},
		{
			Type:     RecordTypeTXT,
			Name:     dkimName,
			Value:    dkimVal,
			Priority: 0,
			TTL:      DefaultTTL,
		},
		{
			Type:     RecordTypeTXT,
			Name:     dmarcName,
			Value:    dmarcVal,
			Priority: 0,
			TTL:      DefaultTTL,
		},
		{
			Type:     RecordTypeCNAME,
			Name:     autoconfigName,
			Value:    cleanMailHost,
			Priority: 0,
			TTL:      DefaultTTL,
		},
		{
			Type:     RecordTypeCNAME,
			Name:     autodiscoverName,
			Value:    cleanMailHost,
			Priority: 0,
			TTL:      DefaultTTL,
		},
	}
}

// MergeSPF parses an existing SPF record, preserves third-party mechanisms (e.g. include:..., ip4:...),
// ensures mx / ourMechanism is included without duplicates, and preserves the qualifier (~all or -all).
// Flags hard conflict if existing SPF contains redirect= modifier or has syntax errors.
func MergeSPF(existingSPF, ourMechanism string) (mergedSPF string, conflict bool, reason string) {
	trimmed := trimQuotes(existingSPF)
	if trimmed == "" {
		return "", true, "SPF record cannot be empty"
	}

	tokens := strings.Fields(trimmed)
	if len(tokens) == 0 {
		return "", true, "SPF record cannot be empty"
	}

	if strings.ToLower(tokens[0]) != "v=spf1" {
		return "", true, fmt.Sprintf("invalid SPF record: %q must start with v=spf1", tokens[0])
	}

	var existingMechs []string
	var modifiers []string
	var qualifier string
	hasAll := false

	for _, token := range tokens[1:] {
		tl := strings.ToLower(token)

		// Check for hard conflict: redirect=
		if strings.HasPrefix(tl, "redirect=") || strings.HasPrefix(tl, "redirect:") {
			return "", true, fmt.Sprintf("SPF record contains redirect modifier %q which requires manual resolution", token)
		}

		// Check for duplicate v=spf1
		if strings.HasPrefix(tl, "v=spf1") {
			return "", true, "SPF record contains duplicate v=spf1 header"
		}

		// Check for all mechanisms
		if tl == "all" || tl == "+all" || tl == "-all" || tl == "~all" || tl == "?all" {
			if hasAll {
				return "", true, "SPF record contains multiple all mechanisms"
			}
			hasAll = true
			qualifier = tl
			continue
		}

		if hasAll {
			return "", true, fmt.Sprintf("SPF record contains mechanism %q after all mechanism", token)
		}

		// Modifiers (like exp=)
		if strings.HasPrefix(tl, "exp=") {
			modifiers = append(modifiers, token)
			continue
		}

		// Validate mechanism syntax
		if !isValidSPFMechanism(token) {
			return "", true, fmt.Sprintf("invalid SPF mechanism: %q", token)
		}

		existingMechs = append(existingMechs, token)
	}

	// Determine mechanisms to ensure
	toEnsure := parseMechanismsToEnsure(ourMechanism)

	// Deduplicate and combine mechanisms
	seen := make(map[string]bool)
	var mergedMechs []string

	for _, m := range existingMechs {
		norm := normalizeSPFMechanism(m)
		if !seen[norm] {
			seen[norm] = true
			mergedMechs = append(mergedMechs, m)
		}
	}

	for _, m := range toEnsure {
		norm := normalizeSPFMechanism(m)
		if !seen[norm] {
			seen[norm] = true
			mergedMechs = append(mergedMechs, m)
		}
	}

	// Determine qualifier (~all or -all or existing)
	finalAll := qualifier
	if !hasAll || finalAll == "" {
		finalAll = "~all"
	}

	parts := make([]string, 0, 2+len(mergedMechs)+len(modifiers))
	parts = append(parts, "v=spf1")
	parts = append(parts, mergedMechs...)
	parts = append(parts, modifiers...)
	parts = append(parts, finalAll)

	return strings.Join(parts, " "), false, ""
}

func isValidSPFMechanism(m string) bool {
	bare := m
	if len(bare) > 0 && (bare[0] == '+' || bare[0] == '-' || bare[0] == '~' || bare[0] == '?') {
		bare = bare[1:]
	}
	if bare == "" {
		return false
	}
	bl := strings.ToLower(bare)

	if bl == "all" {
		return true
	}
	if strings.HasPrefix(bl, "include:") && len(bl) > 8 {
		return true
	}
	if strings.HasPrefix(bl, "ip4:") && len(bl) > 4 {
		return true
	}
	if strings.HasPrefix(bl, "ip6:") && len(bl) > 4 {
		return true
	}
	if bl == "a" || strings.HasPrefix(bl, "a:") || strings.HasPrefix(bl, "a/") {
		return true
	}
	if bl == "mx" || strings.HasPrefix(bl, "mx:") || strings.HasPrefix(bl, "mx/") {
		return true
	}
	if bl == "ptr" || strings.HasPrefix(bl, "ptr:") {
		return true
	}
	if strings.HasPrefix(bl, "exists:") && len(bl) > 7 {
		return true
	}
	return false
}

func normalizeSPFMechanism(m string) string {
	s := strings.TrimSpace(m)
	s = strings.TrimPrefix(s, "+")
	return strings.ToLower(s)
}

func parseMechanismsToEnsure(ourMechanism string) []string {
	trimmed := trimQuotes(ourMechanism)
	if trimmed == "" {
		return []string{"mx"}
	}

	// If ourMechanism is a full SPF string like "v=spf1 mx ~all", extract mechanisms
	fields := strings.Fields(trimmed)
	var mechs []string
	for _, f := range fields {
		fl := strings.ToLower(f)
		if fl == "v=spf1" || fl == "all" || fl == "+all" || fl == "-all" || fl == "~all" || fl == "?all" {
			continue
		}
		if strings.HasPrefix(fl, "redirect=") || strings.HasPrefix(fl, "exp=") {
			continue
		}
		mechs = append(mechs, f)
	}

	if len(mechs) == 0 {
		return []string{"mx"}
	}
	return mechs
}

// DiffRecords computes the RecordPlan required to reconcile existing DNS records to desired records.
// Identifies additions, updates, safe no-ops, and conflicts (e.g. conflicting MX or existing conflicting TXT).
func DiffRecords(existing, desired []Record) RecordPlan {
	plan := RecordPlan{
		ToCreate:  make([]Record, 0),
		ToUpdate:  make([]Record, 0),
		ToDelete:  make([]Record, 0),
		Conflicts: make([]RecordConflict, 0),
	}

	zoneDomain := inferZoneDomain(existing, desired)

	for _, d := range desired {
		dCanon := canonicalRecordName(d.Name, zoneDomain)

		switch d.Type {
		case RecordTypeMX:
			diffMX(existing, d, dCanon, zoneDomain, &plan)

		case RecordTypeTXT:
			diffTXT(existing, d, dCanon, zoneDomain, &plan)

		case RecordTypeCNAME:
			diffCNAME(existing, d, dCanon, zoneDomain, &plan)

		case RecordTypeA, RecordTypeAAAA:
			diffAddress(existing, d, dCanon, zoneDomain, &plan)

		default:
			diffGeneric(existing, d, dCanon, zoneDomain, &plan)
		}
	}

	return plan
}

func diffMX(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	var matchingName []Record
	for _, ex := range existing {
		if ex.Type == RecordTypeMX && canonicalRecordName(ex.Name, zoneDomain) == dCanon {
			matchingName = append(matchingName, ex)
		}
	}

	if len(matchingName) == 0 {
		plan.ToCreate = append(plan.ToCreate, d)
		return
	}

	var hostMatch *Record
	for i := range matchingName {
		ex := matchingName[i]
		if normalizeHostname(ex.Value) == normalizeHostname(d.Value) {
			hostMatch = &ex
			break
		}
	}

	if hostMatch != nil {
		if hostMatch.Priority == d.Priority && (d.TTL == 0 || hostMatch.TTL == d.TTL) {
			// Exact match: safe no-op
			return
		}
		// Priority or TTL change on our mail server
		plan.ToUpdate = append(plan.ToUpdate, d)
		return
	}

	// No matching mail host found: existing MX records point elsewhere
	for _, ex := range matchingName {
		plan.Conflicts = append(plan.Conflicts, RecordConflict{
			Existing: ex,
			Desired:  d,
			Reason:   fmt.Sprintf("conflicting MX record points to %s (priority %d)", ex.Value, ex.Priority),
		})
	}
}

func diffTXT(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	if isSPFRecord(d) {
		diffSPF(existing, d, dCanon, zoneDomain, plan)
		return
	}

	if isDMARCRecord(d) {
		diffDMARC(existing, d, dCanon, zoneDomain, plan)
		return
	}

	if isDKIMRecord(d) {
		diffDKIM(existing, d, dCanon, zoneDomain, plan)
		return
	}

	// Generic TXT record
	diffGenericTXT(existing, d, dCanon, zoneDomain, plan)
}

func diffSPF(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	var existingSPF *Record
	for i := range existing {
		ex := existing[i]
		if ex.Type == RecordTypeTXT && canonicalRecordName(ex.Name, zoneDomain) == dCanon && isSPFRecord(ex) {
			existingSPF = &ex
			break
		}
	}

	if existingSPF == nil {
		plan.ToCreate = append(plan.ToCreate, d)
		return
	}

	if normalizeSPFString(existingSPF.Value) == normalizeSPFString(d.Value) {
		if d.TTL == 0 || existingSPF.TTL == d.TTL {
			// Exact match: safe no-op
			return
		}
		plan.ToUpdate = append(plan.ToUpdate, d)
		return
	}

	// Check if existing SPF has a hard conflict (syntax error or redirect)
	merged, conflict, reason := MergeSPF(existingSPF.Value, d.Value)
	if conflict {
		plan.Conflicts = append(plan.Conflicts, RecordConflict{
			Existing: *existingSPF,
			Desired:  d,
			Reason:   fmt.Sprintf("conflicting SPF record: %s", reason),
		})
		return
	}

	// If desired SPF preserves all existing mechanisms (i.e. matches merged output)
	if normalizeSPFString(merged) == normalizeSPFString(d.Value) {
		plan.ToUpdate = append(plan.ToUpdate, d)
		return
	}

	// Desired SPF drops existing third-party mechanisms
	plan.Conflicts = append(plan.Conflicts, RecordConflict{
		Existing: *existingSPF,
		Desired:  d,
		Reason:   "conflicting SPF record: existing third-party mechanisms would be overwritten without merging",
	})
}

func diffDMARC(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	var existingDMARC *Record
	for i := range existing {
		ex := existing[i]
		if ex.Type == RecordTypeTXT && canonicalRecordName(ex.Name, zoneDomain) == dCanon && isDMARCRecord(ex) {
			existingDMARC = &ex
			break
		}
	}

	if existingDMARC == nil {
		plan.ToCreate = append(plan.ToCreate, d)
		return
	}

	if normalizeDMARCString(existingDMARC.Value) == normalizeDMARCString(d.Value) {
		if d.TTL == 0 || existingDMARC.TTL == d.TTL {
			return
		}
		plan.ToUpdate = append(plan.ToUpdate, d)
		return
	}

	plan.Conflicts = append(plan.Conflicts, RecordConflict{
		Existing: *existingDMARC,
		Desired:  d,
		Reason:   fmt.Sprintf("conflicting DMARC record exists at %s", existingDMARC.Name),
	})
}

func diffDKIM(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	var existingDKIM *Record
	for i := range existing {
		ex := existing[i]
		if ex.Type == RecordTypeTXT && canonicalRecordName(ex.Name, zoneDomain) == dCanon {
			existingDKIM = &ex
			break
		}
	}

	if existingDKIM == nil {
		plan.ToCreate = append(plan.ToCreate, d)
		return
	}

	if normalizeDKIMString(existingDKIM.Value) == normalizeDKIMString(d.Value) {
		if d.TTL == 0 || existingDKIM.TTL == d.TTL {
			return
		}
		plan.ToUpdate = append(plan.ToUpdate, d)
		return
	}

	plan.Conflicts = append(plan.Conflicts, RecordConflict{
		Existing: *existingDKIM,
		Desired:  d,
		Reason:   "conflicting DKIM record exists for selector with different public key",
	})
}

func diffGenericTXT(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	var foundExact bool
	for _, ex := range existing {
		if ex.Type == RecordTypeTXT && canonicalRecordName(ex.Name, zoneDomain) == dCanon {
			if strings.TrimSpace(ex.Value) == strings.TrimSpace(d.Value) {
				foundExact = true
				if d.TTL != 0 && ex.TTL != d.TTL {
					plan.ToUpdate = append(plan.ToUpdate, d)
				}
				break
			}
		}
	}

	if !foundExact {
		plan.ToCreate = append(plan.ToCreate, d)
	}
}

func diffCNAME(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	var recordsAtName []Record
	for _, ex := range existing {
		if canonicalRecordName(ex.Name, zoneDomain) == dCanon {
			recordsAtName = append(recordsAtName, ex)
		}
	}

	if len(recordsAtName) == 0 {
		plan.ToCreate = append(plan.ToCreate, d)
		return
	}

	ex := recordsAtName[0]
	if ex.Type != RecordTypeCNAME {
		plan.Conflicts = append(plan.Conflicts, RecordConflict{
			Existing: ex,
			Desired:  d,
			Reason:   fmt.Sprintf("cannot create CNAME record: conflicting %s record exists at %s", ex.Type, ex.Name),
		})
		return
	}

	if normalizeHostname(ex.Value) == normalizeHostname(d.Value) {
		if d.TTL != 0 && ex.TTL != d.TTL {
			plan.ToUpdate = append(plan.ToUpdate, d)
		}
		return
	}

	// Differing CNAME target
	plan.Conflicts = append(plan.Conflicts, RecordConflict{
		Existing: ex,
		Desired:  d,
		Reason:   fmt.Sprintf("conflicting CNAME record points to %s", ex.Value),
	})
}

func diffAddress(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	var recordsAtName []Record
	for _, ex := range existing {
		if canonicalRecordName(ex.Name, zoneDomain) == dCanon {
			recordsAtName = append(recordsAtName, ex)
		}
	}

	if len(recordsAtName) == 0 {
		plan.ToCreate = append(plan.ToCreate, d)
		return
	}

	for _, ex := range recordsAtName {
		if ex.Type == RecordTypeCNAME {
			plan.Conflicts = append(plan.Conflicts, RecordConflict{
				Existing: ex,
				Desired:  d,
				Reason:   fmt.Sprintf("cannot create %s record: conflicting CNAME record exists at %s", d.Type, ex.Name),
			})
			return
		}
	}

	for _, ex := range recordsAtName {
		if ex.Type == d.Type && strings.TrimSpace(ex.Value) == strings.TrimSpace(d.Value) {
			if d.TTL == 0 || ex.TTL == d.TTL {
				return
			}
			plan.ToUpdate = append(plan.ToUpdate, d)
			return
		}
	}

	// At apex (@), differing A/AAAA is a conflict
	if dCanon == "@" {
		for _, ex := range recordsAtName {
			if ex.Type == d.Type {
				plan.Conflicts = append(plan.Conflicts, RecordConflict{
					Existing: ex,
					Desired:  d,
					Reason:   fmt.Sprintf("conflicting %s record exists at @ pointing to %s", d.Type, ex.Value),
				})
				return
			}
		}
	}

	plan.ToCreate = append(plan.ToCreate, d)
}

func diffGeneric(existing []Record, d Record, dCanon, zoneDomain string, plan *RecordPlan) {
	for _, ex := range existing {
		if ex.Type == d.Type && canonicalRecordName(ex.Name, zoneDomain) == dCanon {
			if strings.TrimSpace(ex.Value) == strings.TrimSpace(d.Value) {
				if d.TTL == 0 || ex.TTL == d.TTL {
					return
				}
				plan.ToUpdate = append(plan.ToUpdate, d)
				return
			}
		}
	}
	plan.ToCreate = append(plan.ToCreate, d)
}

func isSPFRecord(r Record) bool {
	v := strings.ToLower(trimQuotes(r.Value))
	return strings.HasPrefix(v, "v=spf1")
}

func isDMARCRecord(r Record) bool {
	n := strings.ToLower(r.Name)
	v := strings.ToLower(trimQuotes(r.Value))
	return strings.HasPrefix(n, "_dmarc") || strings.HasPrefix(v, "v=dmarc1")
}

func isDKIMRecord(r Record) bool {
	n := strings.ToLower(r.Name)
	v := strings.ToLower(trimQuotes(r.Value))
	return strings.Contains(n, "._domainkey") || strings.HasPrefix(v, "v=dkim1")
}

func inferZoneDomain(existing, desired []Record) string {
	for _, r := range desired {
		cn := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Name), "."))
		if strings.HasPrefix(cn, "_dmarc.") {
			return strings.TrimPrefix(cn, "_dmarc.")
		}
		if strings.HasPrefix(cn, "autoconfig.") {
			return strings.TrimPrefix(cn, "autoconfig.")
		}
		if idx := strings.Index(cn, "._domainkey."); idx != -1 {
			return cn[idx+len("._domainkey."):]
		}
	}
	for _, r := range existing {
		cn := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Name), "."))
		if strings.HasPrefix(cn, "_dmarc.") {
			return strings.TrimPrefix(cn, "_dmarc.")
		}
		if strings.HasPrefix(cn, "autoconfig.") {
			return strings.TrimPrefix(cn, "autoconfig.")
		}
		if idx := strings.Index(cn, "._domainkey."); idx != -1 {
			return cn[idx+len("._domainkey."):]
		}
	}
	return ""
}

func canonicalRecordName(name, zoneDomain string) string {
	cn := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if cn == "" || cn == "@" {
		return "@"
	}
	if zoneDomain != "" {
		zd := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zoneDomain), "."))
		if cn == zd {
			return "@"
		}
		if strings.HasSuffix(cn, "."+zd) {
			return strings.TrimSuffix(cn, "."+zd)
		}
	}
	return cn
}

func normalizeHostname(h string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
}

func normalizeSPFString(s string) string {
	clean := trimQuotes(s)
	fields := strings.Fields(strings.ToLower(clean))
	return strings.Join(fields, " ")
}

func normalizeDKIMString(s string) string {
	clean := trimQuotes(s)
	parts := strings.Split(clean, ";")
	var tags []string
	for _, p := range parts {
		pt := strings.TrimSpace(p)
		if pt != "" {
			tags = append(tags, pt)
		}
	}
	return strings.Join(tags, "; ")
}

func normalizeDMARCString(s string) string {
	clean := trimQuotes(s)
	parts := strings.Split(clean, ";")
	var tags []string
	for _, p := range parts {
		pt := strings.TrimSpace(p)
		if pt != "" {
			tags = append(tags, strings.ToLower(pt))
		}
	}
	return strings.Join(tags, "; ")
}

func trimQuotes(s string) string {
	return strings.TrimFunc(strings.TrimSpace(s), func(r rune) bool {
		return r == '"' || r == '\'' || r == '\t' || r == '\r' || r == '\n'
	})
}
