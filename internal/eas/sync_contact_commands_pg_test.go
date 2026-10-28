package eas

import (
	"strconv"
	"testing"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestContactsBidirectionalDurableCommandsPG(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	r, _ := newEASRouter(t, f, sess)
	collection := "ab:" + strconv.FormatInt(f.book, 10)
	key := "0"
	sync := func(cmd *Node) *Node {
		t.Helper()
		col := Element(pageAirSync, tokCollection, Text(pageAirSync, tokCollectionID, collection), Text(pageAirSync, tokSyncKey, key))
		if cmd != nil {
			col.Add(Element(pageAirSync, tokCommands, cmd))
		}
		result := firstCollection(t, syncCollection(t, r, col))
		if result.ChildText(tokStatus) != syncStatusSuccess {
			t.Fatalf("collection status %s", result.ChildText(tokStatus))
		}
		key = result.ChildText(tokSyncKey)
		return result
	}
	response := func(result *Node, want string) *Node {
		t.Helper()
		responses := result.Child(tokResponses)
		if responses == nil || len(responses.Children) != 1 {
			t.Fatalf("missing command response %+v", result)
		}
		n := responses.Children[0]
		if n.ChildText(tokStatus) != want {
			t.Fatalf("command status got %s want %s", n.ChildText(tokStatus), want)
		}
		return n
	}
	sync(nil)
	contact := collab.Contact{DisplayName: "Device Ada", GivenName: "Ada", FamilyName: "Lovelace", Emails: []collab.Email{{Address: "ada@example.test"}, {Address: "two@example.test"}}, Phones: []collab.Phone{{Number: "123", Label: "mobile"}}, Note: "device note"}
	add := func(c collab.Contact) *Node {
		return Element(pageAirSync, tokAdd, Text(pageAirSync, tokClientID, "stable-client-id"), contactAppData(c))
	}
	id := response(sync(add(contact)), syncStatusSuccess).ChildText(tokServerID)
	if id == "" {
		t.Fatal("missing durable server id")
	}
	// A process/router restart loses every in-memory value but not the receipt.
	r, _ = newEASRouter(t, f, sess)
	if replay := response(sync(add(contact)), syncStatusSuccess).ChildText(tokServerID); replay != id {
		t.Fatalf("replay id changed %s %s", id, replay)
	}
	contact.DisplayName = "different payload"
	response(sync(add(contact)), "7")
	var count int
	if err := f.platform.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_contacts WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL", f.tenant, f.mailboxA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate contact %d %v", count, err)
	}
	change := func(name string) *Node {
		return Element(pageAirSync, tokChange, Text(pageAirSync, tokServerID, id), Element(pageAirSync, tokApplicationData, Text(pageContacts, tokContactFirstName, name)))
	}
	response(sync(change("Augusta")), syncStatusSuccess)
	numericID, _ := strconv.ParseInt(id, 10, 64)
	q := store.New(f.platform.Pool)
	row, err := q.PlatformGetContact(t.Context(), store.PlatformGetContactParams{TenantID: f.tenant, MailboxID: f.mailboxA, ID: numericID})
	if err != nil || row.GivenName != "Augusta" || row.Note != "device note" {
		t.Fatalf("contact write/read %+v %v", row, err)
	}
	svc := appcollab.Service{Q: q}
	_, err = svc.UpdateContact(t.Context(), appcollab.Principal{TenantID: f.tenant, MailboxID: f.mailboxA}, row.ID, row.Version, collab.Contact{DisplayName: "Concurrent browser edit", Emails: []collab.Email{{Address: "web@example.test"}}, Note: "must survive stale device"})
	if err != nil {
		t.Fatal(err)
	}
	// Device fingerprint still refers to the older version: server wins, no lost update.
	response(sync(change("stale overwrite")), "7")
	response(sync(change("Fresh after receiving server change")), syncStatusSuccess)
	response(sync(Element(pageAirSync, tokChange, Text(pageAirSync, tokServerID, id), Element(pageAirSync, tokApplicationData, Text(pageContacts, 0x19, "unsupported company")))), "6")
	// A different principal cannot use this collection or modify its server ID.
	other, _ := newEASRouter(t, f, webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxB})
	foreign := firstCollection(t, syncCollection(t, other, Element(pageAirSync, tokCollection, Text(pageAirSync, tokCollectionID, collection), Text(pageAirSync, tokSyncKey, "0"), Element(pageAirSync, tokCommands, change("intruder")))))
	if foreign.ChildText(tokStatus) != syncStatusObjectDeleted {
		t.Fatalf("foreign collection accepted %+v", foreign)
	}
	response(sync(Element(pageAirSync, tokDelete, Text(pageAirSync, tokServerID, id))), syncStatusSuccess)
	if _, err = q.PlatformGetContact(t.Context(), store.PlatformGetContactParams{TenantID: f.tenant, MailboxID: f.mailboxA, ID: numericID}); err == nil {
		t.Fatal("delete did not tombstone")
	}
	// Receipt persists after deletion: replay never resurrects the deleted item.
	contact.DisplayName = "Device Ada"
	response(sync(add(contact)), syncStatusSuccess)
	if err := f.platform.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_contacts WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL", f.tenant, f.mailboxA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("replay resurrected deleted item %d %v", count, err)
	}
}

func TestContactsWindowBudgetAndStaleKeyPG(t *testing.T) {
	f := newPGFixture(t)
	r, _ := newEASRouter(t, f, webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA})
	svc := appcollab.Service{Q: store.New(f.platform.Pool)}
	for _, name := range []string{"One", "Two", "Three"} {
		if _, err := svc.CreateContact(t.Context(), appcollab.Principal{TenantID: f.tenant, MailboxID: f.mailboxA}, f.book, collab.Contact{DisplayName: name}); err != nil {
			t.Fatal(err)
		}
	}
	collection := "ab:" + strconv.FormatInt(f.book, 10)
	key := "0"
	seen := map[string]bool{}
	initialKey := ""
	for round := 0; round < 5; round++ {
		result := firstCollection(t, syncCollection(t, r, Element(pageAirSync, tokCollection, Text(pageAirSync, tokCollectionID, collection), Text(pageAirSync, tokSyncKey, key), Text(pageAirSync, tokWindowSize, "1"))))
		if result.ChildText(tokStatus) != syncStatusSuccess {
			t.Fatal("window sync failed")
		}
		key = result.ChildText(tokSyncKey)
		if initialKey == "" {
			initialKey = key
		}
		commands := commandChildren(t, result)
		if len(commands) > 1 {
			t.Fatalf("window exceeded: %d", len(commands))
		}
		for _, command := range commands {
			id := command.ChildText(tokServerID)
			if seen[id] {
				t.Fatalf("duplicate across pages %s", id)
			}
			seen[id] = true
		}
		if result.Child(tokMoreAvailable) == nil {
			break
		}
	}
	if len(seen) != 3 {
		t.Fatalf("window lost contacts: %+v", seen)
	}
	// A previously accepted key cannot authorize writes after cursor advancement.
	result := firstCollection(t, syncCollection(t, r, Element(pageAirSync, tokCollection, Text(pageAirSync, tokCollectionID, collection), Text(pageAirSync, tokSyncKey, initialKey), Element(pageAirSync, tokCommands, Element(pageAirSync, tokAdd, Text(pageAirSync, tokClientID, "stale-add"), contactAppData(collab.Contact{DisplayName: "Must not insert"}))))))
	if result.ChildText(tokStatus) != syncStatusInvalidKey {
		t.Fatalf("stale key authorized command: %+v", result)
	}
	var count int
	if err := f.platform.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_contacts WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL", f.tenant, f.mailboxA).Scan(&count); err != nil || count != 3 {
		t.Fatalf("stale key wrote data %d %v", count, err)
	}
}
