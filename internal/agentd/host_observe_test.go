package agentd

import "testing"

func TestHostCountersRejectMissingAndKeepFixedUnits(t *testing.T) {
	value, err := parseHostCounters("cpu 1 2 3 80 10 4 0 0 7 0\n", "MemTotal: 100 kB\nMemAvailable: 40 kB\n")
	if err != nil || value.CPUTotal != 100 || value.CPUIdle != 90 || value.MemoryTotal != 102400 || value.MemoryAvailable != 40960 {
		t.Fatalf("bad counters: %+v %v", value, err)
	}
	for _, cpu := range []string{"", "cpu nope 0 0 0", "notcpu 1 2 3 4"} {
		if _, err = parseHostCounters(cpu, "MemTotal: 100 kB"); err == nil {
			t.Fatal("invalid host data accepted")
		}
	}
}

func TestHostObservationRejectsCallerPathsBeforeAnyProbe(t *testing.T) {
	var manager *ApplyManager
	for _, raw := range []string{`{"path":"/etc/shadow"}`, `{"unit":"bad.service"}`, `{} {}`, `{"command":"id"}`} {
		if _, _, err := manager.ObserveHost([]byte(raw)); err == nil {
			t.Fatal("untrusted probe input accepted")
		}
	}
}
