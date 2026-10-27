package eas

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestEASRestoreRejectsOldSyncAndFolderSnapshots(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	router, _ := newEASRouterWithMail(t, f, sess, newFakeMail(11, msg(1, "persistent message", false)))
	request := func(key string) *Node {
		return Element(pageAirSync, tokCollection, Text(pageAirSync, tokSyncKey, key), Text(pageAirSync, tokCollectionID, "e:INBOX"))
	}
	initial := firstCollection(t, syncCollection(t, router, request("0")))
	old := initial.ChildText(tokSyncKey)
	if old == "" || initial.ChildText(tokStatus) != syncStatusSuccess {
		t.Fatal("initial sync failed")
	}
	if err := db.HoldRestoredSideEffects(context.Background(), f.platform.Config().ConnConfig.ConnString(), "eas-recovery-1"); err != nil {
		t.Fatal(err)
	}
	stale := firstCollection(t, syncCollection(t, router, request(old)))
	if stale.ChildText(tokStatus) != syncStatusInvalidKey {
		t.Fatalf("rewound sync accepted: %+v", stale)
	}
	fresh := firstCollection(t, syncCollection(t, router, request("0")))
	if fresh.ChildText(tokStatus) != syncStatusSuccess || fresh.ChildText(tokSyncKey) == old || len(commandChildren(t, fresh)) != 1 {
		t.Fatalf("full sync failed after recovery: %+v", fresh)
	}
	snapshots, err := LoadFolderSnapshot(t.Context(), f.platform, sess, "dev-a")
	if err != nil || len(snapshots) != 0 {
		t.Fatal("old folder snapshot survived")
	}
}
