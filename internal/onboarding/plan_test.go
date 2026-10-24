package onboarding

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/dnsprovider"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
)

func TestPlanPreservesWebsiteAndThirdPartySPF(t *testing.T) {
	before := []dnsprovider.RemoteRecord{{ID: "site", Record: dnsprovider.Record{Type: "A", Name: "example.test", Value: "192.0.2.55", TTL: 3600}}, {ID: "spf", Record: dnsprovider.Record{Type: "TXT", Name: "example.test", Value: "v=spf1 include:thirdparty.example ~all", TTL: 3600}}}
	wanted := []dnsprovider.Record{{Type: "MX", Name: "@", Value: "mail.example.test", Priority: 10, TTL: 300}, {Type: "TXT", Name: "@", Value: "v=spf1 mx -all", TTL: 300}}
	changes, err := BuildChanges("example.test", before, wanted)
	if err != nil || len(changes.Conflicts) != 0 || len(changes.Actions) != 2 {
		t.Fatalf("%+v %v", changes, err)
	}
	if changes.Actions[1].Kind != "update" || changes.Actions[1].Before.ID != "spf" || changes.Actions[1].After.Value != "v=spf1 include:thirdparty.example mx ~all" {
		t.Fatalf("thirdparty SPF was overwritten: %+v", changes.Actions[1])
	}
	records, err := ManagedRecords("example.test", before, changes)
	if err != nil {
		t.Fatal(err)
	}
	files, err := nsd.Generate(nsd.NodeConfig{ListenAddresses: []string{"127.0.0.1"}, Hostname: "mail.example.test", PrimaryDomain: "example.test", PublicIPv4: "192.0.2.1", Nameservers: []string{"ns.example.test"}}, nsd.Snapshot{Zones: []nsd.Zone{{Domain: "example.test", Mode: "managed", Serial: 1, Explicit: true, Records: records}}})
	if err != nil {
		t.Fatal(err)
	}
	for path, body := range files {
		if strings.HasSuffix(path, ".zone") && (strings.Contains(body, "@ IN A 192.0.2.1") || !strings.Contains(body, "192.0.2.55")) {
			t.Fatalf("website takeover: %s", body)
		}
	}
}
func TestPlanRejectsConflictsAndOwnershipWrites(t *testing.T) {
	cases := []struct {
		name    string
		before  []dnsprovider.RemoteRecord
		wanted  []dnsprovider.Record
		wantErr bool
	}{
		{"foreign MX", []dnsprovider.RemoteRecord{{ID: "foreign", Record: dnsprovider.Record{Type: "MX", Name: "example.test", Value: "foreign.example.test", Priority: 10}}}, []dnsprovider.Record{{Type: "MX", Name: "@", Value: "mail.example.test", Priority: 10}}, false},
		{"duplicate SPF despite one exact", []dnsprovider.RemoteRecord{{ID: "one", Record: dnsprovider.Record{Type: "TXT", Name: "example.test", Value: "v=spf1 mx -all"}}, {ID: "two", Record: dnsprovider.Record{Type: "TXT", Name: "example.test", Value: "v=spf1 include:foreign.example ~all"}}}, []dnsprovider.Record{{Type: "TXT", Name: "@", Value: "v=spf1 mx -all"}}, false},
		{"ownership challenge", nil, []dnsprovider.Record{{Type: "TXT", Name: "_urmail-verify.example.test", Value: "forged"}}, true},
		{"foreign namespace", nil, []dnsprovider.Record{{Type: "TXT", Name: "other.test", Value: "forged"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := BuildChanges("example.test", tc.before, tc.wanted)
			if tc.wantErr {
				if err == nil {
					t.Fatal("unsafe plan accepted")
				}
			} else if err != nil || len(plan.Conflicts) == 0 || len(plan.Actions) != 0 {
				t.Fatalf("conflict was overwritten %+v %v", plan, err)
			}
		})
	}
}
