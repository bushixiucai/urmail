package miadbinventory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func reviewedSourceFixture(t *testing.T) (Options, mapping) {
	t.Helper()
	options := fixture(t, "")
	script := `import sqlite3,sys
c=sqlite3.connect(sys.argv[1]);c.execute('DELETE FROM users')
for i in range(43):
 c.execute('INSERT INTO users(id,email,password,privileges)VALUES(?,?,?,?)',(i+1,'member_%d@d%d.fixture.test'%(i,i%6),'{SHA512-CRYPT}$6$fixturesalt$'+'A'*86,'旧运维标签' if i==6 else ''))
c.execute("INSERT INTO aliases(id,source,destination)VALUES(3,'ops@d0.fixture.test','member_0@d0.fixture.test')")
c.execute("INSERT INTO aliases(id,source,destination)VALUES(4,'custom@d1.fixture.test','member_1@d1.fixture.test')")
c.execute("INSERT INTO auto_aliases(id,source,destination)VALUES(1573,'ops@d0.fixture.test','rootroute@host.fixture.test')")
c.execute("INSERT INTO auto_aliases(id,source,destination)VALUES(1574,'rootroute@host.fixture.test','member_0@d0.fixture.test')")
for i in range(20):c.execute('INSERT INTO auto_aliases(source,destination)VALUES(?,?)',('postmaster%d@d%d.fixture.test'%(i,i%6),'member_0@d0.fixture.test'))
c.commit();c.close()`
	if err := exec.CommandContext(context.Background(), "/usr/bin/python3", "-I", "-c", script, options.SQLitePath).Run(); err != nil {
		t.Fatal("create reviewed private source-shape fixture failed")
	}
	selected := mapping{Version: 1}
	for _, domain := range []string{"d0.fixture.test", "d1.fixture.test", "d2.fixture.test", "d3.fixture.test", "d4.fixture.test", "d5.fixture.test", "host.fixture.test"} {
		selected.Domains = append(selected.Domains, domainMapping{Domain: domain, TenantRef: "private-same-tenant"})
	}
	writeReviewMapping(t, options, selected)
	return options, selected
}

func writeReviewMapping(t *testing.T, options Options, selected mapping) {
	t.Helper()
	raw, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(options.MappingPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
func bindReviewedMapping(t *testing.T, options Options, selected mapping) mapping {
	t.Helper()
	unbound := options
	unbound.MappingPath = ""
	source, err := Inspect(context.Background(), unbound)
	if err != nil || source.SnapshotSHA256 == "" {
		t.Fatal("source fingerprint unavailable")
	}
	selected.Version = 2
	selected.ReviewedSnapshotSHA256 = source.SnapshotSHA256
	selected.LegacyPrivileges = "preserve_metadata_no_authority"
	selected.AllowSameTenantCrossDomainAliases = true
	selected.AllowEquivalentAliasDedup = true
	writeReviewMapping(t, options, selected)
	return selected
}
func mutateReviewedFixture(t *testing.T, options Options, sql string) {
	t.Helper()
	script := `import sqlite3,sys
c=sqlite3.connect(sys.argv[1]);c.executescript(sys.argv[2]);c.commit();c.close()`
	if err := exec.CommandContext(context.Background(), "/usr/bin/python3", "-I", "-c", script, options.SQLitePath, sql).Run(); err != nil {
		t.Fatal("mutate private review fixture failed")
	}
}
func hasWarning(report Report, code, ref string) bool {
	for _, warning := range report.Warnings {
		if warning.Code == code && (ref == "" || warning.SourceRef == ref) {
			return true
		}
	}
	return false
}

func TestVersionTwoReviewBindsRealShapePoliciesWithoutGrantingAuthority(t *testing.T) {
	options, selected := reviewedSourceFixture(t)
	strict, err := Inspect(context.Background(), options)
	if err != nil || !hasIssue(strict, "UNKNOWN_LEGACY_PRIVILEGES") || !hasIssue(strict, "CROSS_DOMAIN_ALIAS_REVIEW_REQUIRED") || !hasIssue(strict, "DUPLICATE_ALIAS_SOURCE") {
		t.Fatalf("v1 strict baseline lost: %+v %v", strict, err)
	}
	selected = bindReviewedMapping(t, options, selected)
	approved, err := Inspect(context.Background(), options)
	if err != nil || approved.Status != "dry_run_complete" || !approved.ReviewSnapshotBound || approved.MappingVersion != 2 || approved.ReviewedEquivalentAliasGroups != 1 || approved.TargetTenantGroups != 1 || len(approved.Domains) != 7 || approved.UnlimitedQuotaMailboxes != 43 || approved.Counts["aliases"] != 2 || approved.Counts["auto_aliases"] != 22 {
		t.Fatalf("bound review failed: %+v %v", approved, err)
	}
	if approved.Changed || approved.ApplyAvailable || approved.CutoverReady {
		t.Fatal("read-only review granted an apply or cutover capability")
	}
	if !hasWarning(approved, "LEGACY_PRIVILEGES_METADATA_ONLY_REVIEWED", "users:7") || !hasWarning(approved, "EQUIVALENT_ALIAS_DUPLICATE_REVIEWED", "aliases:3") || !hasWarning(approved, "EQUIVALENT_ALIAS_DUPLICATE_REVIEWED", "auto_aliases:1573") {
		t.Fatal("opaque source provenance was lost")
	}
	raw, _ := json.Marshal(approved)
	for _, secret := range []string{"member_", "fixture.test", "旧运维标签", "private-same-tenant", "SHA512", "fixturesalt"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("approved review leaked source metadata")
		}
	}
	// Review fields are not valid v1 bypasses even with a correct source digest.
	selected.Version = 1
	writeReviewMapping(t, options, selected)
	refused, err := Inspect(context.Background(), options)
	if err != nil || refused.ReviewSnapshotBound || !hasIssue(refused, "REVIEW_POLICIES_REQUIRE_MAPPING_VERSION_2") || !hasIssue(refused, "DUPLICATE_ALIAS_SOURCE") {
		t.Fatal("v1 accepted v2 policy switches")
	}
}

func TestVersionTwoPoliciesDoNotClearUnprovedGraphsOrSourceChanges(t *testing.T) {
	tests := []struct {
		name, sql, code                            string
		splitTenant, staleDigest, duplicateMapping bool
	}{
		{name: "cross-tenant", code: "CROSS_TENANT_ALIAS_REVIEW_REQUIRED", splitTenant: true},
		{name: "duplicate-mapping", code: "DUPLICATE_DOMAIN_MAPPING", duplicateMapping: true},
		{name: "different-duplicate-target", sql: "UPDATE aliases SET destination='member_1@d1.fixture.test' WHERE id=3;", code: "DUPLICATE_ALIAS_SOURCE"},
		{name: "cross-domain-cycle", sql: "UPDATE auto_aliases SET destination='ops@d0.fixture.test' WHERE id=1574;", code: "ALIAS_CYCLE_OR_DEPTH_REVIEW_REQUIRED"},
		{name: "missing-internal-leaf", sql: "UPDATE auto_aliases SET destination='missing@d0.fixture.test' WHERE id=1574;", code: "UNRESOLVED_INTERNAL_ALIAS_TARGET"},
		{name: "unknown-external-target", sql: "UPDATE auto_aliases SET destination='private@outside.fixture.test' WHERE id=1574;", code: "CROSS_DOMAIN_ALIAS_REVIEW_REQUIRED"},
		{name: "unrelated-hash-policy", sql: "UPDATE users SET password='{UNKNOWN}private-unsupported' WHERE id=1;", code: "UNSUPPORTED_PASSWORD_HASH"},
		{name: "source-label-changed", sql: "UPDATE users SET privileges='新标签不可沿旧review' WHERE id=7;", code: "REVIEWED_SOURCE_FINGERPRINT_MISMATCH", staleDigest: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, selected := reviewedSourceFixture(t)
			selected = bindReviewedMapping(t, options, selected)
			if test.sql != "" {
				mutateReviewedFixture(t, options, test.sql)
			}
			if test.splitTenant {
				selected.Domains[len(selected.Domains)-1].TenantRef = "other-private-tenant"
			}
			if test.duplicateMapping {
				selected.Domains = append(selected.Domains, selected.Domains[0])
			}
			if !test.staleDigest {
				selected = bindReviewedMapping(t, options, selected)
			} else {
				writeReviewMapping(t, options, selected)
			}
			report, err := Inspect(context.Background(), options)
			if err != nil || !hasIssue(report, test.code) || report.Status != "blocked" || (test.name != "unrelated-hash-policy" && report.ReviewedEquivalentAliasGroups != 0) {
				t.Fatalf("unproved review accepted code=%s report=%+v err=%v", test.code, report, err)
			}
			if test.staleDigest && (report.ReviewSnapshotBound || len(report.BoundReviewPolicies) > 0 || !hasIssue(report, "UNKNOWN_LEGACY_PRIVILEGES")) {
				t.Fatal("stale digest activated review policies")
			}
		})
	}
}

func TestCrossDomainCyclesAreCheckedEvenWithoutReviewPolicies(t *testing.T) {
	options, _ := reviewedSourceFixture(t)
	mutateReviewedFixture(t, options, "UPDATE auto_aliases SET destination='ops@d0.fixture.test' WHERE id=1574;")
	report, err := Inspect(context.Background(), options)
	if err != nil || !hasIssue(report, "ALIAS_CYCLE_OR_DEPTH_REVIEW_REQUIRED") {
		t.Fatalf("strict v1 omitted cross-domain graph cycle: %+v %v", report, err)
	}
}

func TestVersionTwoReviewPoliciesAreIndependentAndRequireExactBinding(t *testing.T) {
	tests := []struct {
		name, code string
		edit       func(*mapping)
	}{
		{"metadata-not-approved", "UNKNOWN_LEGACY_PRIVILEGES", func(m *mapping) { m.LegacyPrivileges = "" }},
		{"cross-domain-not-approved", "CROSS_DOMAIN_ALIAS_REVIEW_REQUIRED", func(m *mapping) { m.AllowSameTenantCrossDomainAliases = false }},
		{"dedup-not-approved", "DUPLICATE_ALIAS_SOURCE", func(m *mapping) { m.AllowEquivalentAliasDedup = false }},
		{"wrong-fingerprint", "REVIEWED_SOURCE_FINGERPRINT_MISMATCH", func(m *mapping) { m.ReviewedSnapshotSHA256 = strings.Repeat("0", 64) }},
		{"unknown-policy", "UNSUPPORTED_LEGACY_PRIVILEGE_POLICY", func(m *mapping) { m.LegacyPrivileges = "private-invalid-policy" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, selected := reviewedSourceFixture(t)
			selected = bindReviewedMapping(t, options, selected)
			test.edit(&selected)
			writeReviewMapping(t, options, selected)
			report, err := Inspect(context.Background(), options)
			if err != nil || report.Status != "blocked" || !hasIssue(report, test.code) {
				t.Fatalf("independent policy bypassed %s: %+v %v", test.code, report, err)
			}
			if report.Changed || report.ApplyAvailable || report.CutoverReady {
				t.Fatal("review enabled a mutating operation")
			}
		})
	}
}
