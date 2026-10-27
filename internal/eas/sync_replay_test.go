package eas

import (
	"context"
	"sync/atomic"
	"testing"
)

type countedMailSource struct {
	MailSource
	writes atomic.Int32
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
