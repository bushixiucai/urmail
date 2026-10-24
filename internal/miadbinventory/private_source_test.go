package miadbinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

func authenticatedPrivateFixture(t *testing.T) (Options, mapping, string) {
	t.Helper()
	options, selected := reviewedSourceFixture(t)
	hash, err := domainMail.HashPassword("private-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	script := `import sqlite3,sys
c=sqlite3.connect(sys.argv[1]);c.execute('UPDATE users SET password=?',(sys.argv[2],));c.commit();c.close()`
	if err = exec.CommandContext(context.Background(), "/usr/bin/python3", "-I", "-c", script, options.SQLitePath, hash).Run(); err != nil {
		t.Fatal("set private fixture credentials failed")
	}
	selected = bindReviewedMapping(t, options, selected)
	return options, selected, hash
}
func loadPrivateFixture(t *testing.T, options Options, selected mapping) *ReviewedSource {
	t.Helper()
	source, err := LoadReviewedSource(context.Background(), ReviewedSourceOptions{Options: options, ExpectedSnapshotSHA256: selected.ReviewedSnapshotSHA256})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestPrivateReviewedSourcePreservesCredentialsAndSeparateSenderAuthority(t *testing.T) {
	options, selected, hash := authenticatedPrivateFixture(t)
	source := loadPrivateFixture(t, options, selected)
	if source.Fingerprint() != selected.ReviewedSnapshotSHA256 || len(source.PrivateProofSHA256()) != 64 || !source.CoreOnly() || !source.SievePending() || len(source.PendingProofs()) == 0 {
		t.Fatal("private source proof or pending boundary missing")
	}
	if len(source.Domains()) != 7 || len(source.Mailboxes()) != 43 || len(source.Aliases()) != 23 {
		t.Fatal("private source counts differ from reviewed graph")
	}
	for _, mailbox := range source.Mailboxes() {
		if mailbox.PasswordHash != hash || mailbox.QuotaBytes != -1 || !domainMail.VerifyPassword("private-fixture-password", mailbox.PasswordHash) {
			t.Fatal("original mailbox credential or unlimited quota changed")
		}
		if mailbox.SourceID == 7 && mailbox.LegacyPrivileges != "旧运维标签" {
			t.Fatal("private legacy metadata provenance was discarded")
		}
	}
	duplicateFound, autoOnlyFound := false, false
	for _, alias := range source.Aliases() {
		if alias.FQDN == "d0.fixture.test" && alias.LocalPart == "ops" {
			duplicateFound = true
			if alias.Target != "member_0@d0.fixture.test" || len(alias.SourceRefs) != 2 || len(alias.SenderLogins) != 1 || alias.SenderLogins[0] != "member_0@d0.fixture.test" {
				t.Fatal("equivalent receive dedup lost manual direct sender provenance")
			}
		}
		if alias.FQDN == "host.fixture.test" && alias.LocalPart == "rootroute" {
			autoOnlyFound = true
			if len(alias.SenderLogins) != 0 {
				t.Fatal("automatic receive alias gained send authority")
			}
		}
	}
	if !duplicateFound || !autoOnlyFound {
		t.Fatal("expected private routing facts missing")
	}
	if err := source.ValidateUnchanged(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateSourceNeverExpandsSenderLoginAlongReceiveGraph(t *testing.T) {
	options, selected, _ := authenticatedPrivateFixture(t)
	mutateReviewedFixture(t, options, "INSERT INTO aliases(source,destination)VALUES('manual-chain@d1.fixture.test','rootroute@host.fixture.test');")
	selected = bindReviewedMapping(t, options, selected)
	source := loadPrivateFixture(t, options, selected)
	found := false
	for _, alias := range source.Aliases() {
		if alias.LocalPart == "manual-chain" {
			found = true
			if alias.Target != "member_0@d0.fixture.test" || len(alias.SenderLogins) != 0 {
				t.Fatal("manual sender-login was recursively broadened")
			}
		}
	}
	if !found {
		t.Fatal("manual direct source alias was lost")
	}
}

func TestPrivateSourceGettersAreDetachedAndDebugJSONAreRedacted(t *testing.T) {
	options, selected, hash := authenticatedPrivateFixture(t)
	source := loadPrivateFixture(t, options, selected)
	originalMailbox := source.Mailboxes()[0]
	mutableMailboxes := source.Mailboxes()
	mutableMailboxes[0].PasswordHash = "mutated"
	mutableMailboxes[0].ExtraJSON[0] = 'X'
	if source.Mailboxes()[0].PasswordHash != originalMailbox.PasswordHash || string(source.Mailboxes()[0].ExtraJSON) != string(originalMailbox.ExtraJSON) {
		t.Fatal("mailbox getter mutated reviewed private state")
	}
	aliases := source.Aliases()
	aliases[0].Target = "mutated"
	aliases[0].SourceRefs[0] = "mutated"
	if len(aliases[0].SenderLogins) > 0 {
		aliases[0].SenderLogins[0] = "mutated"
	}
	if source.Aliases()[0].Target == "mutated" || source.Aliases()[0].SourceRefs[0] == "mutated" {
		t.Fatal("alias getter mutated private state")
	}
	if len(source.Aliases()[0].SenderLogins) > 0 && source.Aliases()[0].SenderLogins[0] == "mutated" {
		t.Fatal("sender getter mutated private state")
	}
	domains := source.Domains()
	domains[0].FQDN = "mutated"
	if source.Domains()[0].FQDN == "mutated" {
		t.Fatal("domain getter mutated private state")
	}
	summary := source.Summary()
	summary.Counts["users"] = 999
	summary.Domains[0].SourceRef = "mutated"
	if source.Summary().Counts["users"] != 43 || source.Summary().Domains[0].SourceRef == "mutated" {
		t.Fatal("public summary aliases internal review slices")
	}
	for _, value := range []any{source, source.Mailboxes(), source.Aliases(), source.Domains()} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		formatted := fmt.Sprintf("%v %+v %#v", value, value, value) + string(encoded)
		for _, secret := range []string{hash, "private-fixture-password", "member_", "fixture.test", "旧运维标签", "private-same-tenant"} {
			if strings.Contains(formatted, secret) {
				t.Fatal("debug or JSON output exposed private reviewed source data")
			}
		}
	}
}

func TestPrivateProofBindsAllSourceBytesAndReviewFiles(t *testing.T) {
	for _, scenario := range []string{"mapping-whitespace", "config-comment", "sqlite-sequence"} {
		t.Run(scenario, func(t *testing.T) {
			options, selected, _ := authenticatedPrivateFixture(t)
			source := loadPrivateFixture(t, options, selected)
			switch scenario {
			case "mapping-whitespace":
				raw, err := os.ReadFile(options.MappingPath)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(options.MappingPath, append(raw, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			case "config-comment":
				file, err := os.OpenFile(options.ConfigPath, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = file.WriteString("# private proof changed\n"); err != nil {
					t.Fatal(err)
				}
				if err = file.Close(); err != nil {
					t.Fatal(err)
				}
			case "sqlite-sequence":
				mutateReviewedFixture(t, options, "UPDATE sqlite_sequence SET seq=seq+100 WHERE name='users';")
			}
			// These changes do not alter the public reviewed core digest, but the
			// complete byte proof must prevent this object from entering a target TX.
			report, err := Inspect(context.Background(), options)
			if err != nil || report.SnapshotSHA256 != source.Fingerprint() {
				t.Fatal("fixture unexpectedly changed reviewed core facts")
			}
			if source.ValidateUnchanged(context.Background()) == nil {
				t.Fatal("complete source bytes or review-file change escaped private proof")
			}
			refreshed := loadPrivateFixture(t, options, selected)
			if refreshed.PrivateProofSHA256() == source.PrivateProofSHA256() {
				t.Fatal("new loader did not bind the changed full source bytes")
			}
		})
	}
}

func TestPrivateLoaderRejectsUnsupportedOrUnboundSource(t *testing.T) {
	for _, scenario := range []string{"wrong-expected", "version-one", "mfa", "extra", "permitted-senders"} {
		t.Run(scenario, func(t *testing.T) {
			options, selected, _ := authenticatedPrivateFixture(t)
			expected := selected.ReviewedSnapshotSHA256
			switch scenario {
			case "wrong-expected":
				expected = strings.Repeat("0", 64)
			case "version-one":
				selected.Version = 1
				selected.ReviewedSnapshotSHA256 = ""
				selected.LegacyPrivileges = ""
				selected.AllowSameTenantCrossDomainAliases = false
				selected.AllowEquivalentAliasDedup = false
				writeReviewMapping(t, options, selected)
			case "mfa":
				mutateReviewedFixture(t, options, "INSERT INTO mfa(user_id,type,secret)VALUES(1,'totp','never-output-seed');")
			case "extra":
				mutateReviewedFixture(t, options, "UPDATE users SET extra='never-output-private-extra' WHERE id=1;")
			case "permitted-senders":
				mutateReviewedFixture(t, options, "UPDATE aliases SET permitted_senders='member_0@d0.fixture.test' WHERE id=3;")
			}
			if _, err := LoadReviewedSource(context.Background(), ReviewedSourceOptions{Options: options, ExpectedSnapshotSHA256: expected}); err == nil {
				t.Fatal("unreviewed private source was loaded")
			}
		})
	}
}

func TestPrivateLoaderPreservesFiniteQuotaAndEmptyExtraType(t *testing.T) {
	options, selected, _ := authenticatedPrivateFixture(t)
	mutateReviewedFixture(t, options, "UPDATE users SET quota='8192' WHERE id=2;UPDATE users SET extra='' WHERE id=3;")
	selected = bindReviewedMapping(t, options, selected)
	source := loadPrivateFixture(t, options, selected)
	finite, empty, null := false, false, false
	for _, mailbox := range source.Mailboxes() {
		switch mailbox.SourceID {
		case 2:
			finite = mailbox.QuotaBytes == 8192
		case 3:
			empty = string(mailbox.ExtraJSON) == `""`
		case 1:
			null = string(mailbox.ExtraJSON) == "null"
		}
	}
	if !finite || !empty || !null {
		t.Fatal("finite quota or original NULL/empty metadata type was lost")
	}
}
