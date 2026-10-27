package airetrieval

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func newRetrievalFixture(t *testing.T) (PGChunkStore, Principal, Principal) {
	t.Helper()
	pool, _ := testdb.OpenMigrated(t)
	ctx := t.Context()
	tx, err := pool.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var brand, domain, tenant, mailboxA, mailboxB int64
	if err = tx.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('airetrieval','AIRetrieval') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'AIRetrieval','airetrieval','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'airetrieval.test','active','tok',now()) RETURNING id", tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@airetrieval.test','x','active') RETURNING id", tenant, domain).Scan(&mailboxA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'b','b@airetrieval.test','x','active') RETURNING id", tenant, domain).Scan(&mailboxB); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return PGChunkStore{Pool: pool}, Principal{TenantID: tenant, MailboxID: mailboxA}, Principal{TenantID: tenant, MailboxID: mailboxB}
}

func TestPGChunkStoreCrossMailboxIsolation(t *testing.T) {
	store, a, b := newRetrievalFixture(t)
	ctx := t.Context()
	digest := ContentDigest("invoice total 42")
	if err := store.ReplaceMessage(ctx, a, "INBOX", 3, 7, 1, digest, []string{"the invoice total is 42 euros"}); err != nil {
		t.Fatal(err)
	}
	// Principal B never sees mailbox A's chunk.
	hits, err := store.SearchChunks(ctx, b, "invoice", 20)
	if err != nil || len(hits) != 0 {
		t.Fatalf("cross-mailbox leak: %+v %v", hits, err)
	}
	if _, found, err := store.GetChunk(ctx, b, 1); err != nil || found {
		t.Fatalf("foreign chunk readable: found=%v err=%v", found, err)
	}
	// And the owner still sees it with a matching digest.
	ch, found, err := store.GetChunk(ctx, a, 1)
	if err != nil || !found || ch.Digest != digest || ch.CitationID() != "1" {
		t.Fatalf("%+v found=%v err=%v", ch, found, err)
	}
}

func TestPGReplaceMessageIncrementsVersion(t *testing.T) {
	store, a, _ := newRetrievalFixture(t)
	ctx := t.Context()
	if err := store.ReplaceMessage(ctx, a, "INBOX", 3, 7, 1, ContentDigest("v1"), []string{"v1 text"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceMessage(ctx, a, "INBOX", 3, 7, 2, ContentDigest("v2"), []string{"v2 text"}); err != nil {
		t.Fatal(err)
	}
	states, err := store.MessageStates(ctx, a)
	if err != nil || len(states) != 1 {
		t.Fatalf("%+v %v", states, err)
	}
	for _, state := range states {
		if state.Version != 2 || state.Digest != ContentDigest("v2") {
			t.Fatalf("stale state survived: %+v", state)
		}
	}
}

func TestPGDeleteMessagesDropsChunks(t *testing.T) {
	store, a, _ := newRetrievalFixture(t)
	ctx := t.Context()
	if err := store.ReplaceMessage(ctx, a, "INBOX", 3, 7, 1, ContentDigest("x"), []string{"secret body"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMessages(ctx, a, "INBOX", 3, []uint32{7}); err != nil {
		t.Fatal(err)
	}
	hits, err := store.SearchChunks(ctx, a, "secret", 20)
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted chunks survived: %+v %v", hits, err)
	}
}
