package marketingflow

import (
	"errors"
	"testing"
)

func TestUnsubscribeStopsBeforeOpenedBranch(t *testing.T) {
	flow, err := Normalize(Flow{Steps: []Step{{Key: "a", Kind: "send", Next: "b", WhenOpened: "c", WhenUnsubscribed: "stop"}, {Key: "b", Kind: "wait", Next: "stop"}, {Key: "c", Kind: "send", Next: "stop"}, {Key: "stop", Kind: "stop"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Advance(flow, "a", "unsubscribed")
	if err != nil || got != "stop" {
		t.Fatalf("%s %v", got, err)
	}
	got, err = Advance(flow, "a", "opened")
	if err != nil || got != "c" {
		t.Fatalf("%s %v", got, err)
	}
}

func TestRejectsUnknownBranch(t *testing.T) {
	if _, err := Normalize(Flow{Steps: []Step{{Key: "a", Kind: "send", Next: "missing"}}}); !errors.Is(err, ErrInvalidFlow) {
		t.Fatal(err)
	}
}
