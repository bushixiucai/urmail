package distribution_test

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/domain/distribution"
)

func TestCalculateCommission(t *testing.T) {
	tests := []struct {
		name        string
		amountCents int64
		bps         int32
		expected    int64
		expectErr   bool
	}{
		{"10% of 1000.00 CNY", 100000, 1000, 10000, false},
		{"15.5% of 200.00 CNY", 20000, 1550, 3100, false},
		{"0% rate", 10000, 0, 0, false},
		{"0 amount", 0, 2000, 0, false},
		{"Invalid BPS negative", 10000, -1, 0, true},
		{"Invalid BPS > 10000", 10000, 10001, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := distribution.CalculateCommission(tc.amountCents, tc.bps)
			if tc.expectErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, got)
			}
		})
	}
}

func TestCommissionRejectsNegativeAndAvoidsOverflow(t *testing.T) {
	if _, err := distribution.CalculateCommission(-1, 100); err == nil {
		t.Fatal("negative amount accepted")
	}
	const maximumAmount int64 = 1<<63 - 1
	got, err := distribution.CalculateCommission(maximumAmount, 10000)
	if err != nil || got != maximumAmount {
		t.Fatalf("full commission overflow: %d %v", got, err)
	}
}
