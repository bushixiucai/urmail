package miadbinventory

import (
	"fmt"
	"net/mail"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/idna"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

func canonicalDomain(value string) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00/\\:@ ") {
		return "", false
	}
	domain, err := idna.Lookup.ToASCII(strings.ToLower(value))
	if err != nil || len(domain) > 253 || strings.HasSuffix(domain, ".") {
		return "", false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", false
		}
		for _, r := range label {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789-", r) {
				return "", false
			}
		}
	}
	return domain, true
}

func canonicalAddress(value string, allowDomain bool) (string, string, bool) {
	if strings.TrimSpace(value) != value || len(value) > 320 || strings.ContainsAny(value, "\r\n\x00") {
		return "", "", false
	}
	local, domain, ok := strings.Cut(value, "@")
	if !ok || strings.Contains(domain, "@") {
		return "", "", false
	}
	canonical, ok := canonicalDomain(domain)
	if !ok {
		return "", "", false
	}
	if local == "" {
		return "@" + canonical, canonical, allowDomain
	}
	if len(local) > 64 || !validation.LocalPartRegex.MatchString(strings.ToLower(local)) || strings.ContainsAny(local, "*?/\\") || local == "." || local == ".." {
		return "", "", false
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value || strings.ContainsAny(local, " \"\\") {
		return "", "", false
	}
	return strings.ToLower(local) + "@" + canonical, canonical, true
}

func analyze(source snapshot, selected mapping) Report {
	report := Report{Version: 1, Mode: "dry_run", Status: "blocked", Schema: "unknown", Counts: source.Counts, SnapshotSHA256: source.SnapshotSHA256, Domains: []Domain{}, Blockers: []Issue{}, Warnings: []Issue{}, ExistingServicePolicy: "preserve_nonpaid_grandfather", UnlimitedQuotaMapping: "source_zero_to_target_minus_one"}
	issue := func(code, ref string) { report.Blockers = append(report.Blockers, Issue{Code: code, SourceRef: ref}) }
	if source.ObjectLimitExceeded {
		issue("SOURCE_SCHEMA_OBJECT_LIMIT_EXCEEDED", "")
		return report
	}
	if !source.SchemaKnown {
		issue("UNKNOWN_SOURCE_SCHEMA", "")
		return report
	}
	report.Schema = "miab_v80_users_v1"
	report.MappingVersion = selected.Version
	report.BoundReviewPolicies = []string{}
	if source.LimitExceeded {
		issue("SOURCE_ROW_LIMIT_EXCEEDED", "")
		return report
	}
	review := boundReviewPolicies(source.SnapshotSHA256, selected, &report, issue)
	users := map[int64]bool{}
	addresses := map[string]string{}
	domains := map[string]int{}
	for _, user := range source.Users {
		ref := rowRef("users", user.ID)
		users[user.ID] = true
		address, domain, ok := canonicalAddress(user.Email, false)
		if !ok {
			issue("INVALID_MAILBOX_ADDRESS", ref)
		} else {
			if _, exists := addresses[address]; exists {
				issue("DUPLICATE_MAILBOX_ADDRESS", ref)
				addresses[address] = ""
			} else {
				addresses[address] = ref
			}
			domains[domain]++
		}
		if !user.HashSupported {
			issue("UNSUPPORTED_PASSWORD_HASH", ref)
		}
		quota, err := strconv.ParseInt(user.Quota, 10, 64)
		if err != nil || quota < 0 {
			issue("UNSUPPORTED_QUOTA", ref)
		} else if quota == 0 {
			report.UnlimitedQuotaMailboxes++
		}
		if user.ExtraPresent {
			issue("EXTRA_METADATA_REVIEW_REQUIRED", ref)
		}
		switch user.Privileges {
		case "":
		case "admin":
			report.Warnings = append(report.Warnings, Issue{Code: "LEGACY_ADMIN_NOT_PLATFORM_AUTHORITY", SourceRef: ref})
		default:
			if review.metadataOnlyPrivileges {
				report.Warnings = append(report.Warnings, Issue{Code: "LEGACY_PRIVILEGES_METADATA_ONLY_REVIEWED", SourceRef: ref})
			} else {
				issue("UNKNOWN_LEGACY_PRIVILEGES", ref)
			}
		}
	}
	for _, alias := range source.Aliases {
		ref := rowRef(alias.Table, alias.ID)
		_, domain, valid := canonicalAddress(alias.Source, true)
		if !valid {
			issue("INVALID_ALIAS_SOURCE", ref)
			continue
		}
		if _, exists := domains[domain]; !exists {
			domains[domain] = 0
		}
	}
	for _, mfa := range source.MFA {
		ref := rowRef("mfa", mfa.ID)
		if !users[mfa.UserID] {
			issue("ORPHAN_MFA_REFERENCE", ref)
		}
		// Legacy MFA cannot silently become a new SPA/mailbox authorization grant.
		issue("MFA_REENROLLMENT_REVIEW_REQUIRED", ref)
	}
	mappings := map[string]string{}
	tenantRefs := map[string]string{}
	for i, entry := range selected.Domains {
		ref := fmt.Sprintf("mapping:%d", i+1)
		domain, valid := canonicalDomain(entry.Domain)
		if !valid || entry.TenantRef == "" || len(entry.TenantRef) > 64 || !tenantReference.MatchString(entry.TenantRef) {
			issue("INVALID_EXPLICIT_MAPPING", ref)
			continue
		}
		if _, exists := mappings[domain]; exists {
			issue("DUPLICATE_DOMAIN_MAPPING", ref)
			tenantRefs[domain] = ""
			continue
		}
		mappings[domain] = ref
		tenantRefs[domain] = entry.TenantRef
		if _, exists := domains[domain]; !exists {
			issue("MAPPING_DOMAIN_NOT_IN_SOURCE", ref)
		}
	}
	keys := make([]string, 0, len(domains))
	for domain := range domains {
		keys = append(keys, domain)
	}
	sort.Strings(keys)
	groups := map[string]string{}
	for i, domain := range keys {
		ref := fmt.Sprintf("domain:%d", i+1)
		target := mappings[domain]
		if tenantRefs[domain] == "" {
			target = ""
		}
		if target == "" {
			issue("MISSING_EXPLICIT_TENANT_MAPPING", ref)
		}
		group := ""
		if target != "" {
			group = groups[tenantRefs[domain]]
			if group == "" {
				group = fmt.Sprintf("tenant:%d", len(groups)+1)
				groups[tenantRefs[domain]] = group
			}
		}
		report.Domains = append(report.Domains, Domain{SourceRef: ref, MappingRef: target, TargetGroupRef: group, Mailboxes: domains[domain]})
	}
	report.TargetTenantGroups = len(groups)
	if len(groups) > 1 {
		issue("MULTIPLE_TARGET_TENANTS_REQUIRE_REVIEW", "")
	}
	analyzeAliasGraph(source.Aliases, addresses, tenantRefs, review, &report, issue)
	report.Warnings = append(report.Warnings, Issue{Code: "MAILDIR_GROUPWARE_DNS_TLS_NOT_INSPECTED"}, Issue{Code: "IMPORT_APPLY_NOT_IMPLEMENTED"})
	if len(report.Blockers) == 0 {
		report.Status = "dry_run_complete"
	}
	return report
}
