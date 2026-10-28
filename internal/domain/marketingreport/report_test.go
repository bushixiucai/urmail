package marketingreport

import (
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSnapshotAndCSVPreserveFactsAndNeutralizeSpreadsheetFormulas(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	scope := Scope{Kind: "flows", ResourceID: 1, TenantID: 2, From: now.Add(-time.Hour), Until: now}
	rows := []Fact{{RecordKind: "execution", RecordID: "9", Email: "=HYPERLINK(\"evil\")@fixture.test", State: "submitted", OccurredAt: now, Weight: 1}, {RecordKind: "instance", RecordID: "8", Email: " +cmd@fixture.test", State: "stopped", Uncertain: true, OccurredAt: now, Weight: 1}}
	snapshot, err := Build(scope, "@unsafe title", rows)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []Fact{rows[1], rows[0]}
	same, _ := Build(scope, "@unsafe title", reversed)
	if snapshot.Digest != same.Digest {
		t.Fatal("ordering changed snapshot")
	}
	if snapshot.Counts["execution/submitted"] != 1 || snapshot.Counts["instance/unknown_outcome"] != 1 {
		t.Fatal("unknown/submitted facts lost")
	}
	raw, err := CSV(snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(rows)+2 || records[1][16] != "2" {
		t.Fatal("export silently truncated or metadata missing")
	}
	for _, r := range records[1:] {
		if r[3] != "'@unsafe title" {
			t.Fatal("unsafe resource name")
		}
	}
	if records[2][12] != "'=HYPERLINK(\"evil\")@fixture.test" || records[3][12] != "' +cmd@fixture.test" {
		t.Fatal("CSV formula not escaped")
	}
	rows[0].State = "uncertain"
	changed, _ := Build(scope, "@unsafe title", rows)
	if changed.Digest == snapshot.Digest {
		t.Fatal("changed outcome retained digest")
	}
	scope.TenantID++
	other, _ := Build(scope, "@unsafe title", reversed)
	if other.Digest == snapshot.Digest {
		t.Fatal("cross tenant snapshot digest reused")
	}
}
func TestReportBudgetsRejectPartialOutput(t *testing.T) {
	if _, err := Build(Scope{}, "", make([]Fact, MaxFacts+1)); !errors.Is(err, ErrBudget) {
		t.Fatal("row budget bypass")
	}
	snapshot := Snapshot{Name: strings.Repeat("a", MaxCSVBytes), Scope: Scope{}}
	if _, err := CSV(snapshot, time.Now()); !errors.Is(err, ErrBudget) {
		t.Fatal("byte budget bypass")
	}
}
