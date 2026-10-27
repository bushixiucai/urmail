package eas

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestSyncGuardDoesNotExhaustSingleConnectionPool(t *testing.T) {
	f := newPGFixture(t)
	parsed, err := url.Parse(f.platform.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal("fixture connection URL invalid")
	}
	query := parsed.Query()
	query.Set("pool_max_conns", "1")
	query.Set("pool_min_conns", "0")
	parsed.RawQuery = query.Encode()
	pool, err := db.OpenPlatform(t.Context(), parsed.String())
	if err != nil {
		t.Fatal("open one-connection fixture pool failed")
	}
	defer pool.Close()
	if pool.Config().MaxConns != 1 {
		t.Fatal("fixture did not constrain pool")
	}
	sess := Session{TenantID: f.tenant, MailboxID: f.mailboxA}
	if err = UpsertDevice(t.Context(), pool, sess, Device{DeviceID: "single-connection", ProtocolVersion: "14.1"}); err != nil {
		t.Fatal(err)
	}
	backend := NewSyncBackend(pool)
	epoch, err := store.New(pool.Pool).PlatformRestoreSyncEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, status, err := backend.keyCheck(t.Context(), &SyncRequest{Session: sess, DeviceID: "single-connection"}, "e:INBOX", strconv.FormatInt(epoch, 10)+"-0")
	if err != nil || status != syncStatusInvalidKey {
		t.Fatal("unprimed collection accepted a guessed nonzero key")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := backend.HandleSync(ctx, &SyncRequest{Session: sess, DeviceID: "single-connection", Mail: newFakeMail(91, msg(1, "works with one connection", false)), Node: Element(0, tokSync, Element(0, tokCollections, Element(0, tokCollection, Text(0, tokSyncKey, "0"), Text(0, tokCollectionID, "e:INBOX"))))})
	if err != nil {
		t.Fatalf("Sync guard blocked its own collection transactions: %v", err)
	}
	col := firstCollection(t, result)
	if col.ChildText(tokStatus) != syncStatusSuccess || countTokens(commandChildren(t, col), tokAdd) != 1 {
		t.Fatal("single-connection Sync failed")
	}
}
