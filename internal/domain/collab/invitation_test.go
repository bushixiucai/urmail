package collab

import (
	"strings"
	"testing"
	"time"
)

func inviteFixture(sequence int, method string) Invitation {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	return Invitation{
		UID: "uid-1@urmail", Sequence: sequence, Method: method,
		Organizer: "organizer@example.test", Stamp: time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC),
		Event: Event{
			Summary: "Standup", Location: "Room 5", StartsAt: start, EndsAt: start.Add(time.Hour),
			Attendees:      []string{"ada@example.test", "bob@example.test"},
			AttendeeStatus: map[string]string{"ada@example.test": PartStatAccepted},
		},
	}
}

func TestInvitationICSGoldenRequest(t *testing.T) {
	body, err := InvitationICS(inviteFixture(0, MethodRequest))
	if err != nil {
		t.Fatal(err)
	}
	want := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//urMail//Calendar//EN\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:uid-1@urmail\r\n" +
		"SEQUENCE:0\r\n" +
		"DTSTAMP:20260228T120000Z\r\n" +
		"DTSTART:20260301T090000Z\r\n" +
		"DTEND:20260301T100000Z\r\n" +
		"SUMMARY:Standup\r\n" +
		"LOCATION:Room 5\r\n" +
		"STATUS:CONFIRMED\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED;RSVP=TRUE:mailto:ada@example.test\r\n" +
		"ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.test\r\n" +
		"ORGANIZER:mailto:organizer@example.test\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	if body != want {
		t.Fatalf("golden mismatch:\n got: %q\nwant: %q", body, want)
	}
}

func TestInvitationICSSequenceIncrementsOnUpdate(t *testing.T) {
	first, err := InvitationICS(inviteFixture(0, MethodRequest))
	if err != nil {
		t.Fatal(err)
	}
	second, err := InvitationICS(inviteFixture(1, MethodRequest))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "SEQUENCE:0") || !strings.Contains(second, "SEQUENCE:1") {
		t.Fatalf("sequences: %q / %q", first, second)
	}
}

func TestInvitationICSGoldenCancel(t *testing.T) {
	inv := inviteFixture(2, MethodCancel)
	inv.Event.Status = "cancelled" //nolint:misspell // RFC 5545 spelling
	body, err := InvitationICS(inv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "METHOD:CANCEL\r\n") || !strings.Contains(body, "STATUS:CANCELLED\r\n") { //nolint:misspell // RFC 5545 spelling
		t.Fatalf("cancel body: %q", body)
	}
	if strings.Contains(body, "RSVP=TRUE") {
		t.Fatalf("cancel must not request RSVP: %q", body)
	}
	if strings.Count(body, "ATTENDEE") != 2 {
		t.Fatalf("attendees: %q", body)
	}
}

func replyFixture() string {
	return "BEGIN:VCALENDAR\r\n" +
		"METHOD:REPLY\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:uid-1@urmail\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED:mailto:Bob@Example.test\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}

func TestParseReplyAcceptsMatchingAttendee(t *testing.T) {
	reply, err := ParseReply(replyFixture())
	if err != nil {
		t.Fatal(err)
	}
	if reply.UID != "uid-1@urmail" || reply.Replier != "bob@example.test" || reply.PartStat != PartStatAccepted {
		t.Fatalf("%+v", reply)
	}
}

func TestParseReplyRefusesForgedShapes(t *testing.T) {
	cases := map[string]string{
		"wrong method": strings.Replace(replyFixture(), "METHOD:REPLY", "METHOD:REQUEST", 1),
		"two attendees": strings.Replace(replyFixture(),
			"END:VEVENT", "ATTENDEE;PARTSTAT=DECLINED:mailto:ada@example.test\r\nEND:VEVENT", 1),
		"no uid":       strings.Replace(replyFixture(), "UID:uid-1@urmail\r\n", "", 1),
		"bad partstat": strings.Replace(replyFixture(), "PARTSTAT=ACCEPTED", "PARTSTAT=ORGANIZER-APPROVED", 1),
		"no attendee":  strings.Replace(replyFixture(), "ATTENDEE;PARTSTAT=ACCEPTED:mailto:Bob@Example.test\r\n", "", 1),
	}
	for name, body := range cases {
		if _, err := ParseReply(body); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestApplyReplyUpdatesOnlyMatchingAttendee(t *testing.T) {
	event := Event{
		Summary: "Standup", StartsAt: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
		EndsAt:         time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		Attendees:      []string{"ada@example.test", "bob@example.test"},
		AttendeeStatus: map[string]string{"ada@example.test": PartStatDeclined},
	}
	reply, err := ParseReply(replyFixture())
	if err != nil {
		t.Fatal(err)
	}
	next, err := ApplyReply(event, reply)
	if err != nil {
		t.Fatal(err)
	}
	states := AttendeeStates(next)
	if states[0].Email != "ada@example.test" || states[0].PartStat != PartStatDeclined {
		t.Fatalf("ada status changed: %+v", states)
	}
	if states[1].Email != "bob@example.test" || states[1].PartStat != PartStatAccepted {
		t.Fatalf("bob not updated: %+v", states)
	}
}

func TestApplyReplyRefusesForgeryAndUnknown(t *testing.T) {
	event := Event{
		Summary: "Standup", StartsAt: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
		EndsAt:    time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		Attendees: []string{"ada@example.test"},
	}
	reply, err := ParseReply(replyFixture()) // replier bob is not an attendee
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyReply(event, reply); err == nil {
		t.Fatal("unknown attendee accepted")
	}
	if _, err := ApplyReply(event, Reply{UID: "u", Replier: "ada@example.test", PartStat: "approved"}); err == nil {
		t.Fatal("bad partstat accepted")
	}
	if _, err := ApplyReply(event, Reply{UID: "u", Replier: "organizer@example.test", PartStat: PartStatAccepted}); err == nil {
		t.Fatal("organizer-style forgery accepted")
	}
}
