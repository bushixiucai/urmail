package eas

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type countedMailSource struct {
	MailSource
	writes  atomic.Int32
	deleted atomic.Int32
}

func (s *countedMailSource) DeleteUIDs(ctx context.Context, folder string, uids []uint32) error {
	s.deleted.Add(1)
	return s.MailSource.DeleteUIDs(ctx, folder, uids)
}

func (s *countedMailSource) MarkRead(ctx context.Context, folder string, uids []uint32, read bool) error {
	s.writes.Add(1)
	return s.MailSource.MarkRead(ctx, folder, uids, read)
}

func TestConcurrentSyncRejectsConsumedKeyBeforeIMAPSideEffect(t *testing.T) {
	f := newPGFixture(t)
	sess := Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	mail := &countedMailSource{MailSource: newFakeMail(11, msg(1, "one", false))}
	backend := &SyncBackend{Pool: f.platform}
	request := func(key string, change bool) *SyncRequest {
		col := Element(pageAirSync, tokCollection, Text(pageAirSync, tokSyncKey, key), Text(pageAirSync, tokCollectionID, "e:INBOX"))
		if change {
			col.Add(Element(pageAirSync, tokCommands, Element(pageAirSync, tokChange, Text(pageAirSync, tokServerID, "1"), Element(pageAirSync, tokApplicationData, Text(pageEmail, tokEmailRead, "1")))))
		}
		return &SyncRequest{Session: sess, DeviceID: "same-device", Mail: mail, Node: Element(pageAirSync, tokSync, Element(pageAirSync, tokCollections, col))}
	}
	full, err := backend.HandleSync(t.Context(), request("0", false))
	if err != nil {
		t.Fatal(err)
	}
	key := firstCollection(t, full).ChildText(tokSyncKey)
	start := make(chan struct{})
	results := make(chan string, 2)
	for range 2 {
		go func() {
			<-start
			response, err := backend.HandleSync(t.Context(), request(key, true))
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			results <- response.Child(tokCollections).Children[0].ChildText(tokStatus)
		}()
	}
	close(start)
	statuses := map[string]int{<-results: 1}
	statuses[<-results]++
	if statuses[syncStatusSuccess] != 1 || statuses[syncStatusInvalidKey] != 1 || mail.writes.Load() != 1 {
		t.Fatalf("statuses=%v IMAP writes=%d", statuses, mail.writes.Load())
	}
	again, err := backend.HandleSync(t.Context(), request("0", false))
	if err != nil {
		t.Fatal(err)
	}
	if next := firstCollection(t, again).ChildText(tokSyncKey); next == key {
		t.Fatal("explicit resync reused an old generation")
	}
}

// pm-eas-replay-fix-1: the same atomic-claim contract on the Delete path —
// one expunge, one owner, the loser answers InvalidKey with zero side
// effects.
func TestConcurrentSyncDeleteClaimedAtomically(t *testing.T) {
	f := newPGFixture(t)
	sess := Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	mail := &countedMailSource{MailSource: newFakeMail(11, msg(1, "one", false))}
	backend := &SyncBackend{Pool: f.platform}
	deleteChange := func(key string) *SyncRequest {
		col := Element(pageAirSync, tokCollection, Text(pageAirSync, tokSyncKey, key), Text(pageAirSync, tokCollectionID, "e:INBOX"))
		col.Add(Element(pageAirSync, tokCommands, Element(pageAirSync, tokDelete, Text(pageAirSync, tokServerID, "1"))))
		return &SyncRequest{Session: sess, DeviceID: "same-device", Mail: mail, Node: Element(pageAirSync, tokSync, Element(pageAirSync, tokCollections, col))}
	}
	full, err := backend.HandleSync(t.Context(), deleteChange("0"))
	if err != nil {
		t.Fatal(err)
	}
	key := firstCollection(t, full).ChildText(tokSyncKey)
	start := make(chan struct{})
	results := make(chan string, 2)
	for range 2 {
		go func() {
			<-start
			response, err := backend.HandleSync(t.Context(), deleteChange(key))
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			results <- response.Child(tokCollections).Children[0].ChildText(tokStatus)
		}()
	}
	close(start)
	statuses := map[string]int{<-results: 1}
	statuses[<-results]++
	if statuses[syncStatusSuccess] != 1 || statuses[syncStatusInvalidKey] != 1 || mail.deleted.Load() != 1 {
		t.Fatalf("statuses=%v deletes=%d", statuses, mail.deleted.Load())
	}
}

// failingReadSource fails exactly the first MarkRead, then succeeds: the
// claim must still be consumed (the replay answers InvalidKey) and the side
// effect must never be automatically re-applied.
type failingReadSource struct {
	MailSource
	failFirst atomic.Bool
	attempts  atomic.Int32
}

func (s *failingReadSource) MarkRead(ctx context.Context, folder string, uids []uint32, read bool) error {
	if s.failFirst.Load() && s.attempts.Add(1) == 1 {
		return errors.New("imap connection dropped")
	}
	return s.MailSource.MarkRead(ctx, folder, uids, read)
}

func TestSyncItemFailureConsumesClaimWithoutAutoRetry(t *testing.T) {
	f := newPGFixture(t)
	sess := Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	mail := &failingReadSource{MailSource: newFakeMail(11, msg(1, "one", false))}
	mail.failFirst.Store(true)
	backend := &SyncBackend{Pool: f.platform}
	request := func(key string, change bool) *SyncRequest {
		col := Element(pageAirSync, tokCollection, Text(pageAirSync, tokSyncKey, key), Text(pageAirSync, tokCollectionID, "e:INBOX"))
		if change {
			col.Add(Element(pageAirSync, tokCommands, Element(pageAirSync, tokChange, Text(pageAirSync, tokServerID, "1"), Element(pageAirSync, tokApplicationData, Text(pageEmail, tokEmailRead, "1")))))
		}
		return &SyncRequest{Session: sess, DeviceID: "same-device", Mail: mail, Node: Element(pageAirSync, tokSync, Element(pageAirSync, tokCollections, col))}
	}
	full, err := backend.HandleSync(t.Context(), request("0", false))
	if err != nil {
		t.Fatal(err)
	}
	key := firstCollection(t, full).ChildText(tokSyncKey)
	response, err := backend.HandleSync(t.Context(), request(key, true))
	if err != nil {
		t.Fatal(err)
	}
	// The per-item protocol error is surfaced; the claim is consumed.
	if firstCollection(t, response).ChildText(tokStatus) != syncStatusSuccess {
		t.Fatalf("collection status %s", firstCollection(t, response).ChildText(tokStatus))
	}
	// Same-key replay: InvalidKey — no automatic re-application of the
	// failed side effect; recovery is the explicit key-0 resync.
	replay, err := backend.HandleSync(t.Context(), request(key, true))
	if err != nil {
		t.Fatal(err)
	}
	if firstCollection(t, replay).ChildText(tokStatus) != syncStatusInvalidKey {
		t.Fatalf("replay status %s, want InvalidKey", firstCollection(t, replay).ChildText(tokStatus))
	}
	if mail.attempts.Load() != 1 {
		t.Fatalf("MarkRead attempts = %d, want 1 (no auto re-application)", mail.attempts.Load())
	}
}
