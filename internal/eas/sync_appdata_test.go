package eas

// Golden WBXML tests for the EAS-003 application data encoders. Each test
// encodes, decodes and inspects the node structure so the wire bytes stay
// pinned against regressions (acceptance a).

import (
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/collab"
)

// childrenOf flattens the decoded ApplicationData element.
func childrenOf(t *testing.T, app *Node) []*Node {
	t.Helper()
	if app == nil {
		t.Fatal("no application data")
	}
	return app.Children
}

func findChild(t *testing.T, nodes []*Node, page, token byte) *Node {
	t.Helper()
	for _, n := range nodes {
		if n.Page == page && n.Token == token {
			return n
		}
	}
	return nil
}

func TestGoldenEmailAppData(t *testing.T) {
	preview := "hello world"
	app := emailAppData(MailMessage{
		UID: 7, Subject: "Greetings", From: []string{"Alice <alice@example.com>"},
		To:   []string{"bob@example.com", "carol@example.com"},
		Cc:   []string{"dave@example.com"},
		Date: time.Date(2026, 10, 27, 8, 30, 0, 0, time.UTC),
		Read: true, Flagged: true, Size: 100,
	}, preview)
	decoded, err := Decode(Encode(app))
	if err != nil {
		t.Fatal(err)
	}
	kids := childrenOf(t, decoded)
	if got := findChild(t, kids, pageEmail, tokEmailSubject); got == nil || got.Text != "Greetings" {
		t.Fatalf("subject %+v", got)
	}
	reads := 0
	for _, n := range kids {
		if n.Page == pageEmail && n.Token == tokEmailTo && n.Text == "bob@example.com, carol@example.com" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("expected one combined To field, got %d", reads)
	}
	if got := findChild(t, kids, pageEmail, tokEmailCc); got == nil || got.Text != "dave@example.com" {
		t.Fatalf("cc %+v", got)
	}
	if got := findChild(t, kids, pageEmail, tokEmailFrom); got == nil || got.Text != "Alice <alice@example.com>" {
		t.Fatalf("from %+v", got)
	}
	if got := findChild(t, kids, pageEmail, tokEmailDateReceived); got == nil || got.Text != "2026-10-27T08:30:00.000Z" {
		t.Fatalf("date %+v", got)
	}
	if got := findChild(t, kids, pageEmail, tokEmailRead); got == nil || got.Text != "1" {
		t.Fatalf("read %+v", got)
	}
	flag := findChild(t, kids, pageEmail, tokEmailFlag)
	if flag == nil {
		t.Fatal("flag missing")
	}
	if got := flag.ChildText(tokEmailFlagStatus); got != "2" {
		t.Fatalf("flag status %q", got)
	}
	body := findChild(t, kids, pageAirSyncBase, tokBaseBody)
	if body == nil {
		t.Fatal("body missing")
	}
	if got := body.ChildText(tokBaseType); got != "1" {
		t.Fatalf("body type %q", got)
	}
	if got := body.ChildText(tokBaseData); got != preview {
		t.Fatalf("body data %q", got)
	}
}

// Email without a fetched preview must advertise Truncated, never an empty
// body that a client could mistake for complete content.
func TestGoldenEmailAppDataTruncatedBody(t *testing.T) {
	app := emailAppData(MailMessage{UID: 1, Subject: "s"}, "")
	decoded, err := Decode(Encode(app))
	if err != nil {
		t.Fatal(err)
	}
	body := findChild(t, decoded.Children, pageAirSyncBase, tokBaseBody)
	if body == nil {
		t.Fatal("body missing")
	}
	if got := body.ChildText(tokBaseTruncated); got != "1" {
		t.Fatalf("truncated %q", got)
	}
	if body.ChildText(tokBaseData) != "" {
		t.Fatal("unexpected body data")
	}
}

func TestGoldenContactAppData(t *testing.T) {
	app := contactAppData(collab.Contact{
		DisplayName: "Ada Lovelace", GivenName: "Ada", FamilyName: "Lovelace",
		Emails: []collab.Email{
			{Address: "ada@example.com", Label: "home"},
			{Address: "ada.work@example.com", Label: "work"},
		},
		Phones: []collab.Phone{
			{Number: "+15550001", Label: "mobile"},
			{Number: "+15550002", Label: "home"},
			{Number: "+15550003", Label: "work"},
		},
		Note: "first programmer",
	})
	decoded, err := Decode(Encode(app))
	if err != nil {
		t.Fatal(err)
	}
	kids := childrenOf(t, decoded)
	checks := []struct {
		token byte
		want  string
	}{{tokContactFirstName, "Ada"}, {tokContactLastName, "Lovelace"},
		{tokContactFirstName, "Ada"},
		{tokContactLastName, "Lovelace"},
		{tokContactFileAs, "Ada Lovelace"},
		{tokContactEmail1Address, "ada@example.com"},
		{tokContactEmail2Address, "ada.work@example.com"},
		{tokContactMobilePhoneNumber, "+15550001"},
		{tokContactHomePhoneNumber, "+15550002"},
		{tokContactBusinessPhoneNumber, "+15550003"},
	}
	for _, c := range checks {
		got := findChild(t, kids, pageContacts, c.token)
		if got == nil || got.Text != c.want {
			t.Fatalf("token %02x: got %+v want %q", c.token, got, c.want)
		}
	}
	body := findChild(t, kids, pageAirSyncBase, tokBaseBody)
	if body == nil || body.ChildText(tokBaseData) != "first programmer" {
		t.Fatalf("note body %+v", body)
	}
}

func TestGoldenCalendarAppData(t *testing.T) {
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	exdate := start.AddDate(0, 0, 7)
	rec, ok := parseRecurrence("FREQ=WEEKLY;EXDATE=" + exdate.UTC().Format("20060102T150405Z"))
	if !ok {
		t.Fatal("weekly rule should be representable")
	}
	app := calendarAppData(collab.Event{
		Summary: "Standup", Location: "Room 1", StartsAt: start, EndsAt: end,
		Status: "confirmed", RRule: "FREQ=WEEKLY;EXDATE=" + exdate.UTC().Format("20060102T150405Z"),
	}, time.Time{}, &rec, "series-uid@urmail")
	decoded, err := Decode(Encode(app))
	if err != nil {
		t.Fatal(err)
	}
	kids := childrenOf(t, decoded)
	if got := findChild(t, kids, pageCalendar, tokCalSubject); got == nil || got.Text != "Standup" {
		t.Fatalf("subject %+v", got)
	}
	if got := findChild(t, kids, pageCalendar, tokCalStartTime); got == nil || got.Text != "2026-11-02T09:00:00.000Z" {
		t.Fatalf("start %+v", got)
	}
	if got := findChild(t, kids, pageCalendar, tokCalEndTime); got == nil || got.Text != "2026-11-02T10:00:00.000Z" {
		t.Fatalf("end %+v", got)
	}
	if got := findChild(t, kids, pageCalendar, tokCalUID); got == nil || got.Text != "series-uid@urmail" {
		t.Fatalf("uid %+v", got)
	}
	if got := findChild(t, kids, pageCalendar, tokCalBusyStatus); got == nil || got.Text != "2" {
		t.Fatalf("busy %+v", got)
	}
	recNode := findChild(t, kids, pageCalendar, tokCalRecurrence)
	if recNode == nil {
		t.Fatal("recurrence missing")
	}
	if got := recNode.ChildText(tokCalRecType); got != "1" {
		t.Fatalf("rec type %q", got)
	}
	if got := recNode.ChildText(tokCalRecInterval); got != "1" {
		t.Fatalf("interval %q", got)
	}
	exs := recNode.Child(tokCalExceptions)
	if exs == nil {
		t.Fatal("exceptions missing")
	}
	ex := exs.Child(tokCalException)
	if ex == nil {
		t.Fatal("exception missing")
	}
	if got := ex.ChildText(tokCalExcDeleted); got != "1" {
		t.Fatalf("exc deleted %q", got)
	}
	if got := ex.ChildText(tokCalExcStartTime); got != "2026-11-09T09:00:00.000Z" {
		t.Fatalf("exc start %q", got)
	}
}

// Unrepresentable rules must be detected so callers expand occurrences.
func TestParseRecurrenceRejectsUnmappedRules(t *testing.T) {
	for _, rule := range []string{"", "FREQ=SECONDLY", "FREQ=DAILY;BYDAY=MO,TU", "FREQ=WEEKLY;INTERVAL=2"} {
		if _, ok := parseRecurrence(rule); ok {
			t.Fatalf("rule %q unexpectedly representable", rule)
		}
	}
	rec, ok := parseRecurrence("FREQ=DAILY;INTERVAL=3;COUNT=5")
	if !ok || rec.typ != recTypeDaily || rec.interval != 3 || rec.count != 5 {
		t.Fatalf("daily rule %+v ok=%v", rec, ok)
	}
	rec, ok = parseRecurrence("FREQ=YEARLY")
	if !ok || rec.typ != recTypeYearly {
		t.Fatalf("yearly rule %+v ok=%v", rec, ok)
	}
}

func TestTruncateRunes(t *testing.T) {
	s := "aä€b" // multi-byte runes
	if got := truncateRunes(s, 2); got != "aä" {
		t.Fatalf("got %q", got)
	}
	if got := truncateRunes("  padded  ", 20); got != "padded" {
		t.Fatalf("got %q", got)
	}
	if got := truncateRunes(s, 0); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSyncKeyRoundTrip(t *testing.T) {
	if e, s, full, ok := parseSyncKey("0"); !ok || !full || e != 0 || s != 0 {
		t.Fatal("key 0 must be a full sync")
	}
	if _, _, _, ok := parseSyncKey("garbage"); ok {
		t.Fatal("garbage accepted")
	}
	if _, _, _, ok := parseSyncKey("0-5"); ok {
		t.Fatal("epoch 0 accepted")
	}
	e, s, full, ok := parseSyncKey(formatSyncKey(3, 42))
	if !ok || full || e != 3 || s != 42 {
		t.Fatalf("round trip %d %d %v %v", e, s, full, ok)
	}
}
