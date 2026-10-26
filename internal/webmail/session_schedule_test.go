package webmail

import "testing"

func TestMemoryVaultScheduleSeal(t *testing.T) {
	v := NewMemoryVault()
	v.PutSchedule(7, "s3cret")
	got, ok := v.GetSchedule(7)
	if !ok || got != "s3cret" {
		t.Fatalf("got=%q ok=%v", got, ok)
	}
	v.DeleteSchedule(7)
	if _, ok = v.GetSchedule(7); ok {
		t.Fatal("expected delete")
	}
}
