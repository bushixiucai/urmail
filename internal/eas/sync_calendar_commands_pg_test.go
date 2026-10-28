package eas

import (
	"strconv"
	"testing"
	"time"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestSyncCalendarDeviceWritesAndCASPG(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, _ := newEASRouter(t, f, sess)
	collection := "cal:" + strconv.FormatInt(f.cal, 10)
	key := "0"
	round := func(commands ...*Node) *Node {
		col := Element(pageAirSync, tokCollection, Text(pageAirSync, tokSyncKey, key), Text(pageAirSync, tokCollectionID, collection))
		if len(commands) > 0 {
			col.Add(Element(pageAirSync, tokCommands, commands...))
		}
		result := firstCollection(t, syncCollection(t, r, col))
		if result.ChildText(tokStatus) != syncStatusSuccess {
			t.Fatalf("collection status=%s", result.ChildText(tokStatus))
		}
		key = result.ChildText(tokSyncKey)
		return result
	}
	round()
	loc, _ := time.LoadLocation("America/New_York")
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, loc)
	event := collab.Event{Summary: "Device meeting", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: loc.String(), RRule: "FREQ=WEEKLY;COUNT=5", Status: "confirmed"}
	rec, _ := parseRecurrence(event.RRule)
	add := Element(pageAirSync, tokAdd, Text(pageAirSync, tokClientID, "calendar-device-1"), calendarAppData(event, time.Time{}, &rec, "device-uid@example.test"))
	col := round(add)
	response := col.Child(tokResponses).Child(tokAdd)
	if response == nil || response.ChildText(tokStatus) != syncStatusSuccess {
		t.Fatalf("Add=%+v", response)
	}
	serverID := response.ChildText(tokServerID)
	id, _ := strconv.ParseInt(serverID, 10, 64)
	col = round(add)
	if got := col.Child(tokResponses).Child(tokAdd); got.ChildText(tokStatus) != syncStatusSuccess || got.ChildText(tokServerID) != serverID {
		t.Fatal("ClientId replay duplicated series")
	}
	// A different payload under the same ClientId must fail, never insert.
	changed := Element(pageAirSync, tokAdd, Text(pageAirSync, tokClientID, "calendar-device-1"), calendarAppData(collab.Event{Summary: "Different", StartsAt: start, EndsAt: start.Add(time.Hour), Timezone: loc.String()}, time.Time{}, nil, "different"))
	if got := round(changed).Child(tokResponses).Child(tokAdd).ChildText(tokStatus); got != syncStatusConflict {
		t.Fatalf("ClientId reuse status=%s", got)
	}
	q := store.New(f.platform.Pool)
	row, err := q.PlatformGetEvent(t.Context(), store.PlatformGetEventParams{TenantID: f.tenant, MailboxID: f.mailboxA, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := appcollab.EventFromRow(row)
	if err != nil || stored.Timezone != "America/New_York" || stored.RRule != "FREQ=WEEKLY;INTERVAL=1;COUNT=5;BYDAY=MO;WKST=MO" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	// A concurrent web edit wins over the device's version snapshot.
	svc := appcollab.Service{Q: q}
	principal := appcollab.Principal{TenantID: f.tenant, MailboxID: f.mailboxA}
	stored.Summary = "Web wins"
	if _, err = svc.UpdateEvent(t.Context(), principal, id, row.Version, stored); err != nil {
		t.Fatal(err)
	}
	change := Element(pageAirSync, tokChange, Text(pageAirSync, tokServerID, serverID), Element(pageAirSync, tokApplicationData, Text(pageCalendar, tokCalSubject, "Stale device")))
	if got := round(change).Child(tokResponses).Child(tokChange).ChildText(tokStatus); got != syncStatusConflict {
		t.Fatalf("stale device overwrote web: %s", got)
	}
	// The conflict round delivered the authoritative Change; the new key can
	// now update one occurrence while retaining the original IANA zone.
	ov := Element(pageCalendar, tokCalExceptions, Element(pageCalendar, tokCalException, Text(pageCalendar, tokCalExcStartTime, start.AddDate(0, 0, 7).UTC().Format(calendarTime)), Text(pageCalendar, tokCalStartTime, start.AddDate(0, 0, 7).Add(time.Hour).UTC().Format(calendarTime)), Text(pageCalendar, tokCalEndTime, start.AddDate(0, 0, 7).Add(2*time.Hour).UTC().Format(calendarTime)), Text(pageCalendar, tokCalSubject, "Device override")))
	change = Element(pageAirSync, tokChange, Text(pageAirSync, tokServerID, serverID), Element(pageAirSync, tokApplicationData, Text(pageCalendar, tokCalSubject, "Device update"), ov))
	if got := round(change).Child(tokResponses).Child(tokChange).ChildText(tokStatus); got != syncStatusSuccess {
		t.Fatalf("valid device change status=%s", got)
	}
	row, err = q.PlatformGetEvent(t.Context(), store.PlatformGetEventParams{TenantID: f.tenant, MailboxID: f.mailboxA, ID: id})
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err = appcollab.EventFromRow(row)
	if err != nil || stored.Summary != "Device update" || len(stored.Overrides) != 1 || stored.Overrides[0].Summary != "Device override" || stored.Timezone != "America/New_York" {
		t.Fatalf("updated series=%+v err=%v", stored, err)
	}
	// Guessed or other-mailbox identifiers have no synchronized snapshot.
	forged := Element(pageAirSync, tokDelete, Text(pageAirSync, tokServerID, "9999999"))
	if got := round(forged).Child(tokResponses).Child(tokDelete).ChildText(tokStatus); got != syncStatusObjectDeleted {
		t.Fatal("guessed event ID authorized")
	}
	del := Element(pageAirSync, tokDelete, Text(pageAirSync, tokServerID, serverID))
	if got := round(del).Child(tokResponses).Child(tokDelete).ChildText(tokStatus); got != syncStatusSuccess {
		t.Fatalf("delete status=%s", got)
	}
	if got := commandChildren(t, round()); len(got) != 0 {
		t.Fatal("deleted event resurrected")
	}
}
