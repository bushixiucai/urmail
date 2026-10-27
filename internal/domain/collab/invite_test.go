package collab

import (
	"context"
	"testing"
	"time"
)

func TestSaveDoesNotSendInvitations(t *testing.T) {
	called := false
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	_, sent, err := SaveEvent(context.Background(), Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), Attendees: []string{"ada@example.test"}}, func(context.Context, Invitation) error { called = true; return nil })
	if err != nil || sent || called {
		t.Fatal(sent, called, err)
	}
}

func TestInvitationRequiresConfirmation(t *testing.T) {
	called := false
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	inv := Invitation{UID: "uid-1", Method: MethodRequest, Organizer: "org@example.test", Event: Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), Attendees: []string{"ada@example.test"}}}
	if err := SendInvitations(context.Background(), inv, false, func(context.Context, Invitation) error { called = true; return nil }); err == nil || called {
		t.Fatal(err, called)
	}
	if err := SendInvitations(context.Background(), inv, true, nil); err == nil {
		t.Fatal("nil sender accepted")
	}
	called = false
	if err := SendInvitations(context.Background(), inv, true, func(context.Context, Invitation) error { called = true; return nil }); err != nil || !called {
		t.Fatal(err, called)
	}
}

func TestInvitationRefusesContradictions(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	base := Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour)}
	cases := []Invitation{
		{UID: "u", Method: "PUBLISH", Organizer: "o@example.test", Event: base},
		{UID: "", Method: MethodRequest, Organizer: "o@example.test", Event: base},
		{UID: "u", Method: MethodRequest, Organizer: "not-an-email", Event: base},
		{UID: "u", Method: MethodRequest, Organizer: "o@example.test", Event: base},
		{UID: "u", Method: MethodCancel, Organizer: "o@example.test", Event: base},
		{UID: "u", Method: MethodRequest, Organizer: "o@example.test", Event: Event{Summary: "A", Status: statusCancelled, StartsAt: start, EndsAt: start.Add(time.Hour), Attendees: []string{"a@example.test"}}},
	}
	for i, inv := range cases {
		if _, err := InvitationICS(inv); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}
