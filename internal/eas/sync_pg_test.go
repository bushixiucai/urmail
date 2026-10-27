package eas

// EAS-003 acceptance tests on real PostgreSQL (testdb). Cover: contact add
// appears as Add exactly once and never again, deletion as Delete exactly
// once, calendar EXDATE syncs, UIDVALIDITY change invalidates the sync key,
// cross-mailbox collections fail closed, and the item ceilings hold.

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// fakeMailSource is the seam stand-in used when no real Dovecot is available.
type fakeMailSource struct {
	mu      sync.Mutex
	uidv    uint32
	msgs    []MailMessage
	preview string
	deleted []uint32
	readOps map[uint32]bool
}

func newFakeMail(uidv uint32, msgs ...MailMessage) *fakeMailSource {
	return &fakeMailSource{uidv: uidv, msgs: msgs, preview: "preview text", readOps: map[uint32]bool{}}
}

func (f *fakeMailSource) UIDValidity(context.Context, string) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uidv, nil
}

func (f *fakeMailSource) ListWindow(_ context.Context, _ string, limit int) ([]MailMessage, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit > len(f.msgs) {
		limit = len(f.msgs)
	}
	// Newest first: the tail of the slice is the newest mail.
	return f.msgs[len(f.msgs)-limit:], len(f.msgs), nil
}

func (f *fakeMailSource) Details(context.Context, string, uint32, int) (string, []string, []string, error) {
	return f.preview, []string{"to@example.com"}, nil, nil
}

func (f *fakeMailSource) MarkRead(_ context.Context, _ string, uids []uint32, read bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range uids {
		f.readOps[u] = read
	}
	return nil
}

func (f *fakeMailSource) DeleteUIDs(_ context.Context, _ string, uids []uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, uids...)
	var keep []MailMessage
	for _, m := range f.msgs {
		drop := false
		for _, u := range uids {
			if m.UID == u {
				drop = true
			}
		}
		if !drop {
			keep = append(keep, m)
		}
	}
	f.msgs = keep
	return nil
}

func (f *fakeMailSource) Close() error { return nil }

// newEASRouterWithMail builds the router with a per-request mail source.
func newEASRouterWithMail(t *testing.T, f pgFixture, sess webmail.Session, mail MailSource) (*gin.Engine, *Server) {
	t.Helper()
	r, s := newEASRouter(t, f, sess)
	s.Mail = func(context.Context, Session) (MailSource, error) { return mail, nil }
	return r, s
}

// syncCollection posts one Sync collection and returns the response element.
func syncCollection(t *testing.T, r *gin.Engine, col *Node) *Node {
	t.Helper()
	req := Element(pageAirSync, tokSync,
		Element(pageAirSync, tokCollections, col))
	w := postWBXMLCmd(t, r, CmdSync, req)
	return decodeResponse(t, w)
}

// firstCollection returns the single response collection.
func firstCollection(t *testing.T, resp *Node) *Node {
	t.Helper()
	cols := resp.Child(tokCollections)
	if cols == nil || len(cols.Children) != 1 {
		t.Fatalf("expected one collection, got %+v", cols)
	}
	return cols.Children[0]
}

// commandChildren flattens the Commands element.
func commandChildren(t *testing.T, col *Node) []*Node {
	t.Helper()
	cmds := col.Child(tokCommands)
	if cmds == nil {
		return nil
	}
	return cmds.Children
}

func msg(uid uint32, subject string, read bool) MailMessage {
	return MailMessage{
		UID: uid, Subject: subject, From: []string{"sender@example.com"},
		Date: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), Read: read, Size: 10,
	}
}

// TestSyncEmailIncremental walks the full email diff cycle on PG state.
func TestSyncEmailIncremental(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	mail := newFakeMail(11, msg(1, "one", false), msg(2, "two", false))
	r, _ := newEASRouterWithMail(t, f, sess, mail)

	// Full sync: two Adds, key advances past 0.
	resp := syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "0"),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	col := firstCollection(t, resp)
	if col.ChildText(tokStatus) != syncStatusSuccess {
		t.Fatalf("status %q", col.ChildText(tokStatus))
	}
	key := col.ChildText(tokSyncKey)
	if key == "0" || key == "" {
		t.Fatalf("key %q", key)
	}
	if adds := countTokens(commandChildren(t, col), tokAdd); adds != 2 {
		t.Fatalf("adds %d", adds)
	}

	// Same state: nothing new, key stable.
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	col = firstCollection(t, resp)
	if got := commandChildren(t, col); len(got) != 0 {
		t.Fatalf("unexpected commands %+v", got)
	}
	key = col.ChildText(tokSyncKey)

	// Flag change on UID 1 surfaces as exactly one Change.
	mail.mu.Lock()
	mail.msgs[0].Read = true
	mail.mu.Unlock()
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	col = firstCollection(t, resp)
	if changes := countTokens(commandChildren(t, col), tokChange); changes != 1 {
		t.Fatalf("changes %d", changes)
	}
	key = col.ChildText(tokSyncKey)
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	if got := commandChildren(t, firstCollection(t, resp)); len(got) != 0 {
		t.Fatalf("change repeated: %+v", got)
	}
	key = firstCollection(t, resp).ChildText(tokSyncKey)

	// Client read flag maps to IMAP STORE \\Seen and is acknowledged.
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, "e:INBOX"),
		Element(pageAirSync, tokChange,
			Text(pageAirSync, tokServerID, "2"),
			Element(pageAirSync, tokApplicationData,
				Text(pageEmail, tokEmailRead, "1")))))
	col = firstCollection(t, resp)
	responses := col.Child(tokResponses)
	if responses == nil || len(responses.Children) != 1 ||
		responses.Children[0].ChildText(tokStatus) != syncStatusSuccess {
		t.Fatalf("client change response %+v", responses)
	}
	mail.mu.Lock()
	if !mail.readOps[2] {
		t.Fatal("IMAP store not called for \\Seen")
	}
	mail.mu.Unlock()

	// Server-side expunge of UID 2 (via client Delete) removes exactly that
	// UID, and the deletion surfaces once as Delete on the next sync.
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, col.ChildText(tokSyncKey)),
		Text(pageAirSync, tokCollectionID, "e:INBOX"),
		Element(pageAirSync, tokDelete, Text(pageAirSync, tokServerID, "2"))))
	col = firstCollection(t, resp)
	mail.mu.Lock()
	if len(mail.deleted) != 1 || mail.deleted[0] != 2 {
		t.Fatalf("expunged %v", mail.deleted)
	}
	mail.mu.Unlock()
	// UID 2 gone from the listing: the stored item is tombstoned immediately
	// by the delete itself, so no further Delete may appear.
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, col.ChildText(tokSyncKey)),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	if got := commandChildren(t, firstCollection(t, resp)); len(got) != 0 {
		fatalCmds(t, got)
	}
}

func fatalCmds(t *testing.T, got []*Node) {
	t.Helper()
	for _, c := range got {
		t.Errorf("unexpected command token=%02x serverID=%q", c.Token, c.ChildText(tokServerID))
	}
	t.FailNow()
}

func countTokens(nodes []*Node, token byte) int {
	n := 0
	for _, c := range nodes {
		if c.Token == token {
			n++
		}
	}
	return n
}

// TestSyncEmailUIDValidityInvalidation proves a UIDVALIDITY change answers
// Status 9 and resets the stored state so the next key-0 sync is full.
func TestSyncEmailUIDValidityInvalidation(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	mail := newFakeMail(11, msg(1, "one", false))
	r, sB := newEASRouterWithMail(t, f, sess, mail)
	sPool := sB.Pool

	resp := syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "0"),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	key := firstCollection(t, resp).ChildText(tokSyncKey)

	mail.mu.Lock()
	mail.uidv = 99
	mail.mu.Unlock()
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	if got := firstCollection(t, resp).ChildText(tokStatus); got != syncStatusInvalidKey {
		t.Fatalf("status %q, want 9", got)
	}
	// Stored state was invalidated: no items remain for the folder.
	state, err := loadCollectionState(t.Context(), sPool, sess, "dev-a", "e:INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if state.UIDValidity != 99 {
		t.Fatalf("uid validity %d", state.UIDValidity)
	}
	items, err := loadItems(t.Context(), sPool, sess, "dev-a", "e:INBOX")
	if err != nil || len(items) != 0 {
		t.Fatalf("items %v err %v", items, err)
	}
	// Full resync works again.
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "0"),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	if got := countTokens(commandChildren(t, firstCollection(t, resp)), tokAdd); got != 1 {
		t.Fatalf("adds after resync %d", got)
	}
}

// TestSyncContactsIncremental proves collab contact changes appear exactly
// once: Add on the next sync, never again; Delete exactly once; and the
// object never resurrects.
func TestSyncContactsIncremental(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, _ := newEASRouter(t, f, sess)
	svc := appcollab.Service{Q: store.New(f.platform.Pool)}
	principal := appcollab.Principal{TenantID: f.tenant, MailboxID: f.mailboxA}

	row, err := svc.CreateContact(t.Context(), principal, f.book, collab.Contact{
		DisplayName: "Ada", GivenName: "Ada", FamilyName: "Lovelace",
		Emails: []collab.Email{{Address: "ada@example.com"}},
		Phones: []collab.Phone{{Number: "+15550001", Label: "mobile"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverID := strconv.FormatInt(row.ID, 10)
	collectionID := "ab:" + strconv.FormatInt(f.book, 10)

	// Full sync: one Add.
	resp := syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "0"),
		Text(pageAirSync, tokCollectionID, collectionID)))
	col := firstCollection(t, resp)
	if col.ChildText(tokStatus) != syncStatusSuccess {
		t.Fatalf("status %q", col.ChildText(tokStatus))
	}
	key := col.ChildText(tokSyncKey)
	cmds := commandChildren(t, col)
	if countTokens(cmds, tokAdd) != 1 || cmds[0].ChildText(tokServerID) != serverID {
		t.Fatalf("commands %+v", cmds)
	}
	app := cmds[0].Child(tokApplicationData)
	if app == nil || app.ChildText(tokContactEmail1Address) != "ada@example.com" {
		t.Fatalf("app data %+v", app)
	}

	// Next sync: nothing.
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, collectionID)))
	col = firstCollection(t, resp)
	if got := commandChildren(t, col); len(got) != 0 {
		t.Fatalf("add repeated %+v", got)
	}
	key = col.ChildText(tokSyncKey)

	// Update: one Change.
	if _, err = svc.UpdateContact(t.Context(), principal, row.ID, row.Version, collab.Contact{
		DisplayName: "Ada King", GivenName: "Ada", FamilyName: "King",
		Emails: []collab.Email{{Address: "ada@example.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, collectionID)))
	col = firstCollection(t, resp)
	cmds = commandChildren(t, col)
	if countTokens(cmds, tokChange) != 1 {
		t.Fatalf("change commands %+v", cmds)
	}
	key = col.ChildText(tokSyncKey)

	// Delete: exactly one Delete, never resurrected.
	if _, err = svc.Q.PlatformDeleteContact(t.Context(), store.PlatformDeleteContactParams{
		TenantID: f.tenant, MailboxID: f.mailboxA, ID: row.ID, Version: row.Version + 1,
	}); err != nil {
		t.Fatal(err)
	}
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, collectionID)))
	col = firstCollection(t, resp)
	cmds = commandChildren(t, col)
	if countTokens(cmds, tokDelete) != 1 || cmds[0].ChildText(tokServerID) != serverID {
		t.Fatalf("delete commands %+v", cmds)
	}
	key = col.ChildText(tokSyncKey)
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, key),
		Text(pageAirSync, tokCollectionID, collectionID)))
	if got := commandChildren(t, firstCollection(t, resp)); len(got) != 0 {
		t.Fatalf("tombstone resurrected: %+v", got)
	}
}

// TestSyncCalendarEXDATE proves a recurring event with an EXDATE syncs with
// a Recurrence element plus the deleted-occurrence exception.
func TestSyncCalendarEXDATE(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, _ := newEASRouter(t, f, sess)
	svc := appcollab.Service{Q: store.New(f.platform.Pool)}
	principal := appcollab.Principal{TenantID: f.tenant, MailboxID: f.mailboxA}

	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	exdate := start.AddDate(0, 0, 3)
	rrule := "FREQ=DAILY;EXDATE=" + exdate.UTC().Format("20060102T150405Z")
	if _, err := svc.CreateEvent(t.Context(), principal, f.cal, "exdate-uid@urmail", collab.Event{
		Summary: "Daily", StartsAt: start, EndsAt: start.Add(30 * time.Minute),
		RRule: rrule, Status: "confirmed",
	}); err != nil {
		t.Fatal(err)
	}
	collectionID := "cal:" + strconv.FormatInt(f.cal, 10)
	resp := syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "0"),
		Text(pageAirSync, tokCollectionID, collectionID)))
	col := firstCollection(t, resp)
	if col.ChildText(tokStatus) != syncStatusSuccess {
		t.Fatalf("status %q", col.ChildText(tokStatus))
	}
	cmds := commandChildren(t, col)
	if countTokens(cmds, tokAdd) != 1 {
		t.Fatalf("commands %+v", cmds)
	}
	app := cmds[0].Child(tokApplicationData)
	if app == nil {
		t.Fatal("no application data")
	}
	if got := app.ChildText(tokCalUID); got != "exdate-uid@urmail" {
		t.Fatalf("uid %q", got)
	}
	recNode := app.Child(tokCalRecurrence)
	if recNode == nil {
		t.Fatal("recurrence missing")
	}
	if got := recNode.ChildText(tokCalRecType); got != "0" {
		t.Fatalf("rec type %q", got)
	}
	exs := recNode.Child(tokCalExceptions)
	if exs == nil {
		t.Fatal("exceptions missing")
	}
	ex := exs.Child(tokCalException)
	if ex == nil || ex.ChildText(tokCalExcDeleted) != "1" ||
		ex.ChildText(tokCalExcStartTime) != exdate.UTC().Format(easTime) {
		t.Fatalf("exception %+v", ex)
	}
}

// TestSyncCrossMailboxFailClosed proves mailbox B cannot sync mailbox A's
// collections: the RLS-scoped state resolves nothing and no A data leaks.
func TestSyncCrossMailboxFailClosed(t *testing.T) {
	f := newPGFixture(t)
	sessB := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxB}
	r, _ := newEASRouter(t, f, sessB)
	svc := appcollab.Service{Q: store.New(f.platform.Pool)}
	_, err := svc.CreateContact(context.Background(), appcollab.Principal{TenantID: f.tenant, MailboxID: f.mailboxA}, f.book, collab.Contact{
		DisplayName: "Secret", Emails: []collab.Email{{Address: "secret@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectionID := "ab:" + strconv.FormatInt(f.book, 10)
	resp := syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "0"),
		Text(pageAirSync, tokCollectionID, collectionID)))
	col := firstCollection(t, resp)
	cmds := commandChildren(t, col)
	if len(cmds) != 0 {
		t.Fatalf("cross-mailbox data leaked: %+v", cmds)
	}
	// An unknown collection id fails closed with Status 8.
	resp = syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "0"),
		Text(pageAirSync, tokCollectionID, "ab:999999")))
	col = firstCollection(t, resp)
	if col.ChildText(tokStatus) != syncStatusObjectDeleted {
		t.Fatalf("unknown collection status %q", col.ChildText(tokStatus))
	}
}

// TestSyncCeilings proves the server item cap truncates with MoreAvailable
// and the remainder arrives on the following sync.
func TestSyncCeilings(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	mail := newFakeMail(11, msg(1, "one", false), msg(2, "two", false), msg(3, "three", false))
	r, s := newEASRouterWithMail(t, f, sess, mail)
	backend := s.Sync.(*SyncBackend)
	backend.MaxItems = 2

	resp := syncCollection(t, r, Element(pageAirSync, tokCollection,
		Text(pageAirSync, tokSyncKey, "0"),
		Text(pageAirSync, tokCollectionID, "e:INBOX")))
	col := firstCollection(t, resp)
	if col.Child(tokMoreAvailable) == nil {
		t.Fatal("MoreAvailable missing")
	}
	if got := countTokens(commandChildren(t, col), tokAdd); got != 2 {
		t.Fatalf("adds %d", got)
	}
	// The following windows expose the remainder instead of losing older mail.
	seen := countTokens(commandChildren(t, col), tokAdd)
	for round := 0; col.Child(tokMoreAvailable) != nil && round < 10; round++ {
		resp = syncCollection(t, r, Element(pageAirSync, tokCollection, Text(pageAirSync, tokSyncKey, col.ChildText(tokSyncKey)), Text(pageAirSync, tokCollectionID, "e:INBOX")))
		col = firstCollection(t, resp)
		seen += countTokens(commandChildren(t, col), tokAdd)
	}
	if col.Child(tokMoreAvailable) != nil || seen != 3 {
		t.Fatalf("did not drain all three messages: adds=%d more=%v", seen, col.Child(tokMoreAvailable) != nil)
	}
}

func (f *fakeMailSource) ListPage(_ context.Context, _ string, cursor uint32, limit int) ([]MailMessage, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var messages []MailMessage
	for _, m := range f.msgs {
		if cursor == 0 || m.UID < cursor {
			messages = append(messages, m)
		}
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].UID > messages[j].UID })
	var next uint32
	if len(messages) > limit {
		messages = messages[:limit]
		next = messages[len(messages)-1].UID
	}
	return messages, next, nil
}
