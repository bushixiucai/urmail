package panelpassword

import "testing"

// BenchmarkPanelPasswordCost reports the bounded policy cost, not server capacity.
func BenchmarkPanelPasswordCost(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Hash("bounded-benchmark-password"); err != nil {
			b.Fatal(err)
		}
	}
}
