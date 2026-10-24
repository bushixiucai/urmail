package billing

import (
	"testing"
	"time"
)

func TestUpgradePreservesDiscountAndRoundsOnce(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	for _, tc := range []struct {
		old, next int64
		at        time.Time
		want      int64
	}{
		{10000, 20000, start.Add(15 * 24 * time.Hour), 5000},
		{1, 2, start.Add(15 * 24 * time.Hour), 1},
		{20000, 30000, start.Add(15 * 24 * time.Hour), 5000},
	} {
		got, err := UpgradeDifference(tc.old, tc.next, start, end, tc.at)
		if err != nil || got != tc.want {
			t.Fatalf("got %d %v, want %d", got, err, tc.want)
		}
	}
}

func TestUpgradeRequiresRelationshipAndAllCapabilities(t *testing.T) {
	old := []byte(`{"archive":true,"ai":{"tokens":100}}`)
	for _, tc := range []struct {
		next string
		want bool
	}{
		{`{"archive":true,"ai":{"tokens":200}}`, false},
		{`{"upgrade_from":["pro"],"archive":false,"ai":{"tokens":200}}`, false},
		{`{"upgrade_from":["pro"],"archive":true,"ai":{"tokens":50}}`, false},
		{`{"upgrade_from":["pro"],"archive":true,"ai":{"tokens":200}}`, true},
	} {
		if got := UpgradeCompatible("pro", old, []byte(tc.next)); got != tc.want {
			t.Errorf("%s: %v", tc.next, got)
		}
	}
}
