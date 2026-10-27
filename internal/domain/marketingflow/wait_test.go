package marketingflow

import (
	"testing"
	"time"
)

func TestWaitDoesNotSkipEarly(t *testing.T) {
	flow := Flow{Steps: []Step{{Key: "wait", Kind: "wait", Next: "send"}, {Key: "send", Kind: "send"}}}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	next, ready, err := NextAfterWait(flow, "wait", now.Add(time.Hour), now)
	if err != nil || ready || next != "wait" {
		t.Fatalf("%s %v %v", next, ready, err)
	}
}

func TestWaitAdvancesWhenDue(t *testing.T) {
	flow := Flow{Steps: []Step{{Key: "wait", Kind: "wait", Next: "send"}, {Key: "send", Kind: "send"}}}
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	next, ready, err := NextAfterWait(flow, "wait", now, now)
	if err != nil || !ready || next != "send" {
		t.Fatalf("%s %v %v", next, ready, err)
	}
}
