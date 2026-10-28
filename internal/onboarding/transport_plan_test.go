package onboarding

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/dnsprovider"
)

func TestExplicitTransportPlanUpdatesOnlyRecognizedReservedRecords(t *testing.T) {
	before := []dnsprovider.RemoteRecord{{ID: "policy-owned-by-tenant", Record: dnsprovider.Record{Name: "_mta-sts.customer.test", Type: "TXT", Value: "v=STSv1; id=oldpolicy", TTL: 3600}}, {ID: "website", Record: dnsprovider.Record{Name: "customer.test", Type: "A", Value: "192.0.2.8"}}}
	desired := []dnsprovider.Record{{Name: "_mta-sts.customer.test", Type: "TXT", Value: "v=STSv1; id=newpolicy", TTL: 300}}
	plan, err := BuildChanges("customer.test", before, desired)
	if err != nil || len(plan.Conflicts) != 0 || len(plan.Actions) != 1 || plan.Actions[0].Kind != "update" || plan.Actions[0].Before.ID != before[0].ID {
		t.Fatalf("explicit before/after transport update %+v %v", plan, err)
	}
	if plan.Actions[0].Before.Value != "v=STSv1; id=oldpolicy" {
		t.Fatal("rollback lost prior policy")
	}
	duplicate := append(append([]dnsprovider.RemoteRecord{}, before...), before[0])
	duplicate[2].ID = "duplicate"
	plan, err = BuildChanges("customer.test", duplicate, desired)
	if err != nil || len(plan.Conflicts) != 1 || len(plan.Actions) != 0 {
		t.Fatal("duplicate transport records were overwritten")
	}
	before[0].Value = "unrelated tenant content"
	plan, err = BuildChanges("customer.test", before, desired)
	if err != nil || len(plan.Conflicts) != 1 || len(plan.Actions) != 0 {
		t.Fatal("unrecognized TXT content was overwritten")
	}
}
