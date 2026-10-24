package miadbinventory

import (
	"regexp"
	"sort"
	"strings"
)

type aliasReviewPolicies struct{ metadataOnlyPrivileges, sameTenantCrossDomain, equivalentDedup bool }

var reviewDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func boundReviewPolicies(snapshot string, selected mapping, report *Report, issue func(string, string)) aliasReviewPolicies {
	var result aliasReviewPolicies
	hasPolicy := selected.ReviewedSnapshotSHA256 != "" || selected.LegacyPrivileges != "" || selected.AllowSameTenantCrossDomainAliases || selected.AllowEquivalentAliasDedup
	if selected.Version != 2 {
		if hasPolicy {
			issue("REVIEW_POLICIES_REQUIRE_MAPPING_VERSION_2", "")
		}
		return result
	}
	if !reviewDigestPattern.MatchString(selected.ReviewedSnapshotSHA256) || selected.ReviewedSnapshotSHA256 != snapshot {
		issue("REVIEWED_SOURCE_FINGERPRINT_MISMATCH", "")
		return result
	}
	if selected.LegacyPrivileges != "" && selected.LegacyPrivileges != "preserve_metadata_no_authority" {
		issue("UNSUPPORTED_LEGACY_PRIVILEGE_POLICY", "")
		return result
	}
	report.ReviewSnapshotBound = true
	result.metadataOnlyPrivileges = selected.LegacyPrivileges == "preserve_metadata_no_authority"
	result.sameTenantCrossDomain = selected.AllowSameTenantCrossDomainAliases
	result.equivalentDedup = selected.AllowEquivalentAliasDedup
	if result.metadataOnlyPrivileges {
		report.BoundReviewPolicies = append(report.BoundReviewPolicies, "preserve_metadata_no_authority")
	}
	if result.sameTenantCrossDomain {
		report.BoundReviewPolicies = append(report.BoundReviewPolicies, "same_tenant_internal_cross_domain_aliases")
	}
	if result.equivalentDedup {
		report.BoundReviewPolicies = append(report.BoundReviewPolicies, "proven_equivalent_alias_dedup")
	}
	return result
}

type reviewedAliasRoute struct {
	ref, domain                 string
	targets                     []string
	valid, allowed, crossDomain bool
}
type aliasResolution struct {
	targets        map[string]bool
	valid, allowed bool
}

// analyzeAliasGraph retains every source branch. It never chooses a manual or
// automatic alias by precedence. A duplicate is reviewable only when every
// branch resolves, without a cycle, to the very same unique source mailbox.
func analyzeAliasGraph(aliases []sourceAlias, mailboxes map[string]string, tenants map[string]string, review aliasReviewPolicies, report *Report, issue func(string, string)) {
	routes := map[string][]reviewedAliasRoute{}
	for _, alias := range aliases {
		address, domain, valid := canonicalAddress(alias.Source, true)
		if !valid {
			continue
		} // The caller already reported the malformed source.
		route := reviewedAliasRoute{ref: rowRef(alias.Table, alias.ID), domain: domain, valid: true, allowed: tenants[domain] != ""}
		if _, collision := mailboxes[address]; collision {
			issue("MAILBOX_ALIAS_COLLISION", route.ref)
			route.valid = false
		}
		values := strings.Split(alias.Destination, ",")
		if len(values) != 1 {
			issue("MULTI_RECIPIENT_ALIAS_REVIEW_REQUIRED", route.ref)
			route.valid = false
		}
		for _, value := range values {
			target, targetDomain, ok := canonicalAddress(strings.TrimSpace(value), true)
			if !ok {
				issue("UNSUPPORTED_ALIAS_DESTINATION", route.ref)
				route.valid = false
				continue
			}
			route.targets = append(route.targets, target)
			if strings.HasPrefix(target, "@") {
				issue("DOMAIN_ALIAS_REVIEW_REQUIRED", route.ref)
				route.valid = false
			}
			if targetDomain != domain {
				route.crossDomain = true
				sameTenant := tenants[domain] != "" && tenants[targetDomain] != "" && tenants[domain] == tenants[targetDomain]
				if !review.sameTenantCrossDomain || !sameTenant {
					code := "CROSS_DOMAIN_ALIAS_REVIEW_REQUIRED"
					if review.sameTenantCrossDomain && tenants[domain] != "" && tenants[targetDomain] != "" && tenants[domain] != tenants[targetDomain] {
						code = "CROSS_TENANT_ALIAS_REVIEW_REQUIRED"
					}
					issue(code, route.ref)
					route.allowed = false
				}
			}
			if tenants[targetDomain] == "" || tenants[domain] == "" || tenants[targetDomain] != tenants[domain] {
				route.allowed = false
			}
		}
		if alias.PermittedSenders != nil {
			issue("SENDER_RESTRICTIONS_REVIEW_REQUIRED", route.ref)
			route.valid = false
		}
		routes[address] = append(routes[address], route)
	}
	keys := make([]string, 0, len(routes))
	for address := range routes {
		keys = append(keys, address)
	}
	sort.Strings(keys)
	for _, address := range keys {
		for _, route := range routes[address] {
			for _, target := range route.targets {
				if _, mailbox := mailboxes[target]; !mailbox {
					if _, alias := routes[target]; !alias {
						_, domain, _ := canonicalAddress(target, true)
						code := "UNRESOLVED_LOCAL_ALIAS_TARGET"
						if domain != route.domain {
							code = "UNRESOLVED_INTERNAL_ALIAS_TARGET"
						}
						issue(code, route.ref)
					}
				}
			}
		}
	}
	colors := map[string]int{}
	cache := map[string]aliasResolution{}
	cycleReported := false
	var resolve func(string, int) aliasResolution
	resolve = func(address string, depth int) aliasResolution {
		if result, found := cache[address]; found {
			return result
		}
		rows, isAlias := routes[address]
		if ref, isMailbox := mailboxes[address]; isMailbox {
			return aliasResolution{targets: map[string]bool{address: true}, valid: ref != "" && !isAlias, allowed: ref != "" && !isAlias}
		}
		if !isAlias {
			return aliasResolution{}
		}
		if colors[address] == 1 || depth > 1024 {
			if !cycleReported {
				issue("ALIAS_CYCLE_OR_DEPTH_REVIEW_REQUIRED", rows[0].ref)
				cycleReported = true
			}
			return aliasResolution{}
		}
		colors[address] = 1
		result := aliasResolution{targets: map[string]bool{}, valid: true, allowed: true}
		for _, row := range rows {
			result.valid = result.valid && row.valid
			result.allowed = result.allowed && row.allowed
			for _, target := range row.targets {
				child := resolve(target, depth+1)
				result.valid = result.valid && child.valid
				result.allowed = result.allowed && child.allowed
				// Only singleton equality matters. Cap each union at two distinct leaves
				// so a branching source graph cannot create quadratic target-set memory.
				for leaf := range child.targets {
					if len(result.targets) < 2 {
						result.targets[leaf] = true
					}
				}
			}
		}
		colors[address] = 2
		cache[address] = result
		return result
	}
	for _, address := range keys {
		resolve(address, 0)
	} // Includes cross-domain edges even in strict v1.
	for _, address := range keys {
		rows := routes[address]
		result := cache[address]
		if len(rows) > 1 {
			if review.equivalentDedup && result.valid && result.allowed && len(result.targets) == 1 {
				report.ReviewedEquivalentAliasGroups++
				for _, row := range rows {
					report.Warnings = append(report.Warnings, Issue{Code: "EQUIVALENT_ALIAS_DUPLICATE_REVIEWED", SourceRef: row.ref})
				}
			} else {
				for _, row := range rows[1:] {
					issue("DUPLICATE_ALIAS_SOURCE", row.ref)
				}
			}
		}
		if review.sameTenantCrossDomain && result.valid && result.allowed && len(result.targets) == 1 {
			for _, row := range rows {
				if row.crossDomain {
					report.Warnings = append(report.Warnings, Issue{Code: "SAME_TENANT_INTERNAL_ALIAS_REVIEWED", SourceRef: row.ref})
				}
			}
		}
	}
}
