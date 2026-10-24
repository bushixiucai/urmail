package dnsprovider

import (
	"strings"
	"testing"
)

func TestGenerateMailRecords(t *testing.T) {
	t.Run("standard generation", func(t *testing.T) {
		fqdn := "example.com"
		mailHost := "box.urmail.cn"
		selector := "mail"
		pubKey := "MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQC3..."
		dmarcPolicy := "quarantine"

		records := GenerateMailRecords(fqdn, mailHost, selector, pubKey, dmarcPolicy)
		if len(records) != 6 {
			t.Fatalf("expected 6 records, got %d", len(records))
		}

		// MX
		mx := records[0]
		if mx.Type != RecordTypeMX || mx.Name != "@" || mx.Value != "box.urmail.cn" || mx.Priority != 10 {
			t.Errorf("unexpected MX record: %+v", mx)
		}

		// SPF
		spf := records[1]
		if spf.Type != RecordTypeTXT || spf.Name != "@" || spf.Value != "v=spf1 mx -all" {
			t.Errorf("unexpected SPF record: %+v", spf)
		}

		// DKIM
		dkim := records[2]
		expectedDKIMName := "mail._domainkey.example.com"
		expectedDKIMVal := "v=DKIM1; k=rsa; p=" + pubKey
		if dkim.Type != RecordTypeTXT || dkim.Name != expectedDKIMName || dkim.Value != expectedDKIMVal {
			t.Errorf("unexpected DKIM record: %+v", dkim)
		}

		// DMARC
		dmarc := records[3]
		expectedDMARCName := "_dmarc.example.com"
		expectedDMARCVal := "v=DMARC1; p=quarantine"
		if dmarc.Type != RecordTypeTXT || dmarc.Name != expectedDMARCName || dmarc.Value != expectedDMARCVal {
			t.Errorf("unexpected DMARC record: %+v", dmarc)
		}

		// autoconfig
		autoconfig := records[4]
		if autoconfig.Type != RecordTypeCNAME || autoconfig.Name != "autoconfig.example.com" || autoconfig.Value != "box.urmail.cn" {
			t.Errorf("unexpected autoconfig record: %+v", autoconfig)
		}

		// autodiscover
		autodiscover := records[5]
		if autodiscover.Type != RecordTypeCNAME || autodiscover.Name != "autodiscover.example.com" || autodiscover.Value != "box.urmail.cn" {
			t.Errorf("unexpected autodiscover record: %+v", autodiscover)
		}
	})

	t.Run("handles trailing dots and full tags", func(t *testing.T) {
		fqdn := "example.com."
		mailHost := "box.urmail.cn."
		selector := "default"
		pubKey := "v=DKIM1; k=rsa; p=MIGf..."
		dmarcPolicy := "v=DMARC1; p=reject; rua=mailto:dmarc@example.com"

		records := GenerateMailRecords(fqdn, mailHost, selector, pubKey, dmarcPolicy)
		if len(records) != 6 {
			t.Fatalf("expected 6 records, got %d", len(records))
		}

		if records[0].Value != "box.urmail.cn" {
			t.Errorf("mail host trailing dot not stripped: %s", records[0].Value)
		}
		if records[2].Name != "default._domainkey.example.com" {
			t.Errorf("dkim name incorrect: %s", records[2].Name)
		}
		if records[2].Value != pubKey {
			t.Errorf("dkim value duplicated tag: %s", records[2].Value)
		}
		if records[3].Name != "_dmarc.example.com" {
			t.Errorf("dmarc name incorrect: %s", records[3].Name)
		}
		if records[3].Value != dmarcPolicy {
			t.Errorf("dmarc value duplicated tag: %s", records[3].Value)
		}
		if records[4].Name != "autoconfig.example.com" || records[4].Value != "box.urmail.cn" {
			t.Errorf("autoconfig incorrect: %+v", records[4])
		}
	})

	t.Run("empty domain / root fallback", func(t *testing.T) {
		records := GenerateMailRecords("@", "box.urmail.cn", "s1", "abc", "")
		if records[2].Name != "s1._domainkey" {
			t.Errorf("expected relative dkim name, got %s", records[2].Name)
		}
		if records[3].Name != "_dmarc" {
			t.Errorf("expected relative dmarc name, got %s", records[3].Name)
		}
		if records[3].Value != "v=DMARC1; p=quarantine" {
			t.Errorf("expected default dmarc policy none, got %s", records[3].Value)
		}
		if records[4].Name != "autoconfig" {
			t.Errorf("expected relative autoconfig name, got %s", records[4].Name)
		}
	})
}

func TestMergeSPF(t *testing.T) {
	tests := []struct {
		name         string
		existing     string
		ourMech      string
		wantMerged   string
		wantConflict bool
		reasonSubstr string
	}{
		{
			name:         "preserves google include and adds mx with ~all",
			existing:     "v=spf1 include:_spf.google.com ~all",
			ourMech:      "mx",
			wantMerged:   "v=spf1 include:_spf.google.com mx ~all",
			wantConflict: false,
		},
		{
			name:         "preserves ip4 and -all qualifier",
			existing:     "v=spf1 ip4:198.51.100.1/24 -all",
			ourMech:      "mx",
			wantMerged:   "v=spf1 ip4:198.51.100.1/24 mx -all",
			wantConflict: false,
		},
		{
			name:         "no duplicate if mx already present",
			existing:     "v=spf1 include:_spf.google.com mx ~all",
			ourMech:      "mx",
			wantMerged:   "v=spf1 include:_spf.google.com mx ~all",
			wantConflict: false,
		},
		{
			name:         "no duplicate if +mx is present",
			existing:     "v=spf1 +mx -all",
			ourMech:      "mx",
			wantMerged:   "v=spf1 +mx -all",
			wantConflict: false,
		},
		{
			name:         "preserves multiple mechanisms and ?all",
			existing:     "v=spf1 a mx ip4:10.0.0.1 include:relay.mail.com ?all",
			ourMech:      "mx",
			wantMerged:   "v=spf1 a mx ip4:10.0.0.1 include:relay.mail.com ?all",
			wantConflict: false,
		},
		{
			name:         "custom include mechanism",
			existing:     "v=spf1 ip4:1.2.3.4 ~all",
			ourMech:      "include:_spf.urmail.cn",
			wantMerged:   "v=spf1 ip4:1.2.3.4 include:_spf.urmail.cn ~all",
			wantConflict: false,
		},
		{
			name:         "empty ourMech defaults to mx",
			existing:     "v=spf1 include:_spf.google.com ~all",
			ourMech:      "",
			wantMerged:   "v=spf1 include:_spf.google.com mx ~all",
			wantConflict: false,
		},
		{
			name:         "no all mechanism in existing defaults to ~all",
			existing:     "v=spf1 include:_spf.google.com",
			ourMech:      "mx",
			wantMerged:   "v=spf1 include:_spf.google.com mx ~all",
			wantConflict: false,
		},
		{
			name:         "full SPF string as ourMech extracts mechanisms",
			existing:     "v=spf1 include:_spf.google.com ~all",
			ourMech:      "v=spf1 mx ~all",
			wantMerged:   "v=spf1 include:_spf.google.com mx ~all",
			wantConflict: false,
		},
		{
			name:         "quoted existing SPF string",
			existing:     `"v=spf1 ip4:1.1.1.1 -all"`,
			ourMech:      "mx",
			wantMerged:   "v=spf1 ip4:1.1.1.1 mx -all",
			wantConflict: false,
		},
		// Conflict test cases
		{
			name:         "hard conflict on redirect modifier",
			existing:     "v=spf1 redirect=_spf.example.com",
			ourMech:      "mx",
			wantConflict: true,
			reasonSubstr: "redirect",
		},
		{
			name:         "hard conflict on empty SPF",
			existing:     "",
			ourMech:      "mx",
			wantConflict: true,
			reasonSubstr: "empty",
		},
		{
			name:         "hard conflict on invalid header",
			existing:     "v=spf2.0/pra ip4:1.2.3.4",
			ourMech:      "mx",
			wantConflict: true,
			reasonSubstr: "must start with v=spf1",
		},
		{
			name:         "hard conflict on multiple all mechanisms",
			existing:     "v=spf1 -all ~all",
			ourMech:      "mx",
			wantConflict: true,
			reasonSubstr: "multiple all",
		},
		{
			name:         "hard conflict on mechanism after all",
			existing:     "v=spf1 ~all mx",
			ourMech:      "mx",
			wantConflict: true,
			reasonSubstr: "after all",
		},
		{
			name:         "hard conflict on syntax error",
			existing:     "v=spf1 invalid_garbage_token ~all",
			ourMech:      "mx",
			wantConflict: true,
			reasonSubstr: "invalid SPF mechanism",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			merged, conflict, reason := MergeSPF(tc.existing, tc.ourMech)
			if conflict != tc.wantConflict {
				t.Fatalf("expected conflict=%v, got %v (reason: %s)", tc.wantConflict, conflict, reason)
			}
			if tc.wantConflict {
				if tc.reasonSubstr != "" && !strings.Contains(strings.ToLower(reason), strings.ToLower(tc.reasonSubstr)) {
					t.Errorf("expected reason to contain %q, got %q", tc.reasonSubstr, reason)
				}
			} else {
				if merged != tc.wantMerged {
					t.Errorf("merged mismatch: got %s, want %s", merged, tc.wantMerged)
				}
			}
		})
	}
}

func TestDiffRecords(t *testing.T) {
	desired := GenerateMailRecords("example.com", "box.urmail.cn", "mail", "PUBKEY123", "none")

	t.Run("all additions when existing is empty", func(t *testing.T) {
		plan := DiffRecords(nil, desired)
		if len(plan.ToCreate) != len(desired) {
			t.Errorf("expected %d creations, got %d", len(desired), len(plan.ToCreate))
		}
		if len(plan.ToUpdate) != 0 || len(plan.Conflicts) != 0 {
			t.Errorf("expected 0 updates and 0 conflicts, got updates=%d conflicts=%d", len(plan.ToUpdate), len(plan.Conflicts))
		}
	})

	t.Run("unrelated existing records are safe no-ops (not deleted or conflicting)", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeA, Name: "@", Value: "198.51.100.42", TTL: 300},
			{Type: RecordTypeTXT, Name: "@", Value: "google-site-verification=abc", TTL: 300},
			{Type: RecordTypeCNAME, Name: "www", Value: "example.com", TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		// All 6 mail records should still be created
		if len(plan.ToCreate) != 6 {
			t.Errorf("expected 6 creates, got %d", len(plan.ToCreate))
		}
		if len(plan.Conflicts) != 0 {
			t.Errorf("expected 0 conflicts, got %d", len(plan.Conflicts))
		}
		if len(plan.ToDelete) != 0 {
			t.Errorf("expected 0 deletes (unrelated records preserved), got %d", len(plan.ToDelete))
		}
	})

	t.Run("exact match is safe no-op", func(t *testing.T) {
		plan := DiffRecords(desired, desired)
		if len(plan.ToCreate) != 0 || len(plan.ToUpdate) != 0 || len(plan.Conflicts) != 0 {
			t.Errorf("expected empty plan for exact match, got creates=%d updates=%d conflicts=%d",
				len(plan.ToCreate), len(plan.ToUpdate), len(plan.Conflicts))
		}
	})

	t.Run("normalizes names: relative vs FQDN and trailing dots", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeMX, Name: "example.com.", Value: "box.urmail.cn.", Priority: 10, TTL: 300},
			{Type: RecordTypeTXT, Name: "example.com", Value: "\"v=spf1 mx -all\"", TTL: 300},
			{Type: RecordTypeTXT, Name: "mail._domainkey.example.com.", Value: "v=DKIM1; k=rsa; p=PUBKEY123", TTL: 300},
			{Type: RecordTypeTXT, Name: "_dmarc.example.com", Value: "v=DMARC1; p=none;", TTL: 300},
			{Type: RecordTypeCNAME, Name: "autoconfig.example.com.", Value: "box.urmail.cn", TTL: 300},
			{Type: RecordTypeCNAME, Name: "autodiscover.example.com", Value: "box.urmail.cn.", TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		if len(plan.ToCreate) != 0 || len(plan.ToUpdate) != 0 || len(plan.Conflicts) != 0 {
			t.Errorf("expected all no-ops with normalization, got creates=%d updates=%d conflicts=%d",
				len(plan.ToCreate), len(plan.ToUpdate), len(plan.Conflicts))
		}
	})

	t.Run("safe update on MX priority change", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeMX, Name: "@", Value: "box.urmail.cn", Priority: 20, TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		var mxUpdateFound bool
		for _, u := range plan.ToUpdate {
			if u.Type == RecordTypeMX && u.Priority == 10 {
				mxUpdateFound = true
			}
		}
		if !mxUpdateFound {
			t.Errorf("expected MX priority update to 10, got updates: %+v", plan.ToUpdate)
		}
		if len(plan.Conflicts) != 0 {
			t.Errorf("expected 0 conflicts, got %d", len(plan.Conflicts))
		}
	})

	t.Run("safe update on TTL difference", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeCNAME, Name: "autoconfig.example.com", Value: "box.urmail.cn", TTL: 3600},
		}

		plan := DiffRecords(existing, desired)
		var cnameUpdated bool
		for _, u := range plan.ToUpdate {
			if u.Type == RecordTypeCNAME && u.TTL == DefaultTTL {
				cnameUpdated = true
			}
		}
		if !cnameUpdated {
			t.Errorf("expected CNAME TTL update, got updates: %+v", plan.ToUpdate)
		}
	})

	t.Run("safe update when desired SPF is properly merged with existing third-party SPF", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeTXT, Name: "@", Value: "v=spf1 include:_spf.google.com ~all", TTL: 300},
		}

		// Desired SPF has been merged using MergeSPF:
		mergedSPF, _, _ := MergeSPF(existing[0].Value, "mx")
		desiredMerged := GenerateMailRecords("example.com", "box.urmail.cn", "mail", "PUBKEY123", "none")
		for i := range desiredMerged {
			if isSPFRecord(desiredMerged[i]) {
				desiredMerged[i].Value = mergedSPF
			}
		}

		plan := DiffRecords(existing, desiredMerged)
		var spfUpdated bool
		for _, u := range plan.ToUpdate {
			if isSPFRecord(u) && strings.Contains(u.Value, "include:_spf.google.com") {
				spfUpdated = true
			}
		}
		if !spfUpdated {
			t.Errorf("expected merged SPF to be in ToUpdate, got updates: %+v", plan.ToUpdate)
		}
		if len(plan.Conflicts) != 0 {
			t.Errorf("expected no conflict for properly merged SPF, got %d conflicts: %+v", len(plan.Conflicts), plan.Conflicts)
		}
	})

	t.Run("conflict on conflicting MX pointing to another mail provider", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeMX, Name: "@", Value: "aspmx.l.google.com", Priority: 1, TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		if len(plan.Conflicts) == 0 {
			t.Fatalf("expected conflict for foreign MX, got 0")
		}

		var mxConflictFound bool
		for _, c := range plan.Conflicts {
			if c.Desired.Type == RecordTypeMX && c.Existing.Value == "aspmx.l.google.com" {
				mxConflictFound = true
			}
		}
		if !mxConflictFound {
			t.Errorf("expected MX conflict on aspmx.l.google.com, got conflicts: %+v", plan.Conflicts)
		}
	})

	t.Run("conflict on unmerged existing SPF with third-party mechanisms", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeTXT, Name: "@", Value: "v=spf1 include:_spf.google.com -all", TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		var spfConflictFound bool
		for _, c := range plan.Conflicts {
			if isSPFRecord(c.Desired) {
				spfConflictFound = true
			}
		}
		if !spfConflictFound {
			t.Errorf("expected conflict when desired SPF drops third-party Google include, got %+v", plan.Conflicts)
		}
	})

	t.Run("conflict on existing SPF with redirect modifier", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeTXT, Name: "@", Value: "v=spf1 redirect=_spf.example.com", TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		var spfConflictFound bool
		for _, c := range plan.Conflicts {
			if isSPFRecord(c.Desired) && strings.Contains(c.Reason, "redirect") {
				spfConflictFound = true
			}
		}
		if !spfConflictFound {
			t.Errorf("expected conflict on redirect= SPF, got conflicts: %+v", plan.Conflicts)
		}
	})

	t.Run("conflict on existing DKIM with different public key", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeTXT, Name: "mail._domainkey.example.com", Value: "v=DKIM1; k=rsa; p=OLD_KEY_XYZ", TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		var dkimConflictFound bool
		for _, c := range plan.Conflicts {
			if isDKIMRecord(c.Desired) {
				dkimConflictFound = true
			}
		}
		if !dkimConflictFound {
			t.Errorf("expected conflict for differing DKIM public key, got %+v", plan.Conflicts)
		}
	})

	t.Run("conflict on existing DMARC with different policy", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeTXT, Name: "_dmarc.example.com", Value: "v=DMARC1; p=reject; rua=mailto:reports@example.com", TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		var dmarcConflictFound bool
		for _, c := range plan.Conflicts {
			if isDMARCRecord(c.Desired) {
				dmarcConflictFound = true
			}
		}
		if !dmarcConflictFound {
			t.Errorf("expected conflict for differing DMARC record, got %+v", plan.Conflicts)
		}
	})

	t.Run("conflict on conflicting CNAME pointing elsewhere", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeCNAME, Name: "autoconfig.example.com", Value: "autodiscover.outlook.com", TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		var cnameConflictFound bool
		for _, c := range plan.Conflicts {
			if c.Desired.Type == RecordTypeCNAME && c.Existing.Value == "autodiscover.outlook.com" {
				cnameConflictFound = true
			}
		}
		if !cnameConflictFound {
			t.Errorf("expected conflict on autoconfig CNAME pointing to outlook, got %+v", plan.Conflicts)
		}
	})

	t.Run("conflict when CNAME collides with existing A record", func(t *testing.T) {
		existing := []Record{
			{Type: RecordTypeA, Name: "autoconfig.example.com", Value: "1.2.3.4", TTL: 300},
		}

		plan := DiffRecords(existing, desired)
		var collisionFound bool
		for _, c := range plan.Conflicts {
			if c.Desired.Type == RecordTypeCNAME && c.Existing.Type == RecordTypeA {
				collisionFound = true
			}
		}
		if !collisionFound {
			t.Errorf("expected collision conflict between CNAME and A record, got %+v", plan.Conflicts)
		}
	})
}
