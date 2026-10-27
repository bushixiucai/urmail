package eas

import (
	"context"
	"strconv"
	"testing"

	"github.com/bushixiucai/urmail/internal/webmail"
)

func TestEmailDeepPaginationAndBoundedDeletes(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	var initial []MailMessage
	for i := 1; i <= 150; i++ {
		initial = append(initial, msg(uint32(i), "message "+strconv.Itoa(i), false))
	}
	source := newFakeMail(71, initial...)
	r, s := newEASRouterWithMail(t, f, sess, source)
	s.Sync.(*SyncBackend).MaxItems = 23
	key := "0"
	seen := map[string]bool{}
	rounds := 0
	round := func() *Node {
		t.Helper()
		col := firstCollection(t, syncCollection(t, r, Element(0, tokCollection, Text(0, tokSyncKey, key), Text(0, tokCollectionID, "e:INBOX"), Text(0, tokWindowSize, "23"))))
		if col.ChildText(tokStatus) != syncStatusSuccess {
			t.Fatalf("round %d status %s", rounds, col.ChildText(tokStatus))
		}
		key = col.ChildText(tokSyncKey)
		rounds++
		return col
	}
	for {
		col := round()
		commands := commandChildren(t, col)
		if len(commands) > 23 {
			t.Fatal("window exceeded")
		}
		for _, cmd := range commands {
			if cmd.Token != tokAdd {
				t.Fatalf("false delete/change during initial scan: %+v", cmd)
			}
			id := cmd.ChildText(tokServerID)
			if seen[id] {
				t.Fatalf("duplicate add %s", id)
			}
			seen[id] = true
		}
		if col.Child(tokMoreAvailable) == nil {
			break
		}
		if rounds > 20 {
			t.Fatal("MoreAvailable never cleared")
		}
		// Replace the backend mid-scan: no in-memory cursor is needed to resume.
		if rounds == 2 {
			s.Sync = NewSyncBackend(f.platform)
		}
	}
	if len(seen) != 150 {
		t.Fatalf("lost deep mail: %d", len(seen))
	}
	if len(commandChildren(t, round())) != 0 {
		t.Fatal("unchanged pass emitted messages")
	}
	// Remove 131 UIDs. No old item can be deleted until the complete cursor
	// traversal has proved absence; every delete response respects WindowSize.
	source.mu.Lock()
	source.msgs = source.msgs[:19]
	source.mu.Unlock()
	deleted := map[string]bool{}
	for n := 0; n < 20; n++ {
		col := round()
		commands := commandChildren(t, col)
		if len(commands) > 23 {
			t.Fatal("delete window exceeded")
		}
		for _, cmd := range commands {
			if cmd.Token != tokDelete {
				t.Fatal("unexpected command")
			}
			id := cmd.ChildText(tokServerID)
			if deleted[id] {
				t.Fatal("duplicate delete")
			}
			deleted[id] = true
		}
		if col.Child(tokMoreAvailable) == nil {
			break
		}
		if n == 19 {
			t.Fatal("delete backlog did not drain")
		}
	}
	if len(deleted) != 131 {
		t.Fatalf("deleted %d want131", len(deleted))
	}
	items, err := loadItems(t.Context(), f.platform, sess, "dev-a", "e:INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 19 {
		t.Fatalf("scan markers or deleted UIDs retained: %d", len(items))
	}
	// A subsequent delivery is discovered after the prior pass completes.
	source.mu.Lock()
	source.msgs = append(source.msgs, msg(500, "new delivery", false))
	source.mu.Unlock()
	col := round()
	if countTokens(commandChildren(t, col), tokAdd) != 1 {
		t.Fatal("new arrival missed")
	}
	// A small byte budget splits responses without advancing past an omitted
	// item. Restoring the budget must deliver the pending mail once.
	source.mu.Lock()
	source.msgs = append(source.msgs, msg(501, "more delivery", false))
	source.mu.Unlock()
	s.Sync.(*SyncBackend).MaxResponseBytes = 32
	col = firstCollection(t, syncCollection(t, r, Element(0, tokCollection, Text(0, tokSyncKey, key), Text(0, tokCollectionID, "e:INBOX"))))
	if col.ChildText(tokStatus) != syncStatusServerError {
		t.Fatal("unrepresentable item claimed successful progress")
	}
	s.Sync.(*SyncBackend).MaxResponseBytes = DefaultMaxResponseBytes
	col = round()
	if countTokens(commandChildren(t, col), tokAdd) != 1 {
		t.Fatal("omitted item lost")
	}
}

type contentMailSource struct{ *fakeMailSource }

func (s contentMailSource) FullMessage(_ context.Context, _ string, uid uint32) (*webmail.Message, error) {
	return &webmail.Message{MessageSummary: webmail.MessageSummary{UID: uid}, UIDValidity: s.uidv, Text: "body", Attachments: []webmail.AttachmentMeta{{Filename: "file.bin", PartID: "2", Size: 3}}}, nil
}

func TestEmailSyncAddsAdvertiseAttachmentReferences(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	mail := contentMailSource{newFakeMail(42, msg(1, "one", false), msg(2, "two", false))}
	r, _ := newEASRouterWithMail(t, f, sess, mail)
	col := firstCollection(t, syncCollection(t, r, Element(0, tokCollection, Text(0, tokSyncKey, "0"), Text(0, tokCollectionID, "e:INBOX"))))
	commands := commandChildren(t, col)
	if len(commands) != 2 {
		t.Fatal("adds missing")
	}
	for _, cmd := range commands {
		app := cmd.Child(tokApplicationData)
		attachments := nodeOnPage(app, 17, 0x0E)
		if attachments == nil || len(attachments.Children) != 1 || pageText(attachments.Children[0], 17, 0x11) == "" {
			t.Fatal("Add omitted attachment identity")
		}
	}
}
