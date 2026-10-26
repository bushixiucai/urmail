package agentd

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/testdb"
)

// ARC-01 regression: expunge matches on Message-ID, so a recall of one
// archived copy must be refused while another copy of the same Message-ID in
// the same mailbox is under legal hold -- otherwise the held copy's Maildir
// original is destroyed together with the recalled one.
func TestRecallRefusedWhenSameMessageHeld(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	m := &ApplyManager{}
	m.pool = platform
	ctx := context.Background()

	var brand, tenant, user, domain, mailbox int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('hold','Hold') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Hold','hold','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('hold@example.test','fixture-only') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,provisioning_state,verification_token,verified_at) VALUES($1,'hold.test','active','ready',$2,now()) RETURNING id", tenant, strings.Repeat("a", 64)).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'held','held@hold.test','fixture-only','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}

	const messageID = "<held-message@example.test>"
	insertEntry := func(rawSHA string, hold bool) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, "INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at,raw_sha256,legal_hold) VALUES($1,$2,$3,'fixture',now(),$4,$5) RETURNING id",
			tenant, mailbox, messageID, rawSHA, hold).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	heldID := insertEntry(strings.Repeat("a", 64), true)
	freeID := insertEntry(strings.Repeat("b", 64), false)

	insertRecall := func(entry int64) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, "INSERT INTO recall_requests(tenant_id,message_id,requested_by,archive_entry_id,mailbox_id) VALUES($1,$2,$3,$4,$5) RETURNING id",
			tenant, messageID, user, entry, mailbox).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	recallID := insertRecall(freeID)

	// No doveadm call may happen at all: the hold conflict must be detected
	// before any mailbox query.
	m.output = func(_ context.Context, _ ...string) ([]byte, error) {
		t.Fatal("doveadm invoked despite a legal-hold conflict")
		return nil, nil
	}
	m.run = func(_ context.Context, _ string, _ ...string) error {
		t.Fatal("expunge executed despite a legal-hold conflict")
		return nil
	}

	status, _, err := m.ExpungeRecall(json.RawMessage(`{"tenant_id":` + itoa64(tenant) + `,"recall_id":` + itoa64(recallID) + `}`))
	if err != nil || status != "failed" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	var reason string
	if err := platform.QueryRow(ctx, "SELECT reason FROM recall_requests WHERE id=$1", recallID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "legal_hold_conflict" {
		t.Fatalf("reason=%q, want legal_hold_conflict", reason)
	}

	// Clearing the hold unblocks the same path: this time the expunge runs
	// and the request completes as local_recalled.
	if _, err := platform.Exec(ctx, "UPDATE archive_entries SET legal_hold=false WHERE id=$1", heldID); err != nil {
		t.Fatal(err)
	}
	// UNIQUE(tenant_id,archive_entry_id): the follow-up recall targets the
	// formerly held entry itself, which is now identical in behavior.
	recallID2 := insertRecall(heldID)
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 2 && args[2] == "search" {
			return []byte("[{\"mailbox-guid\":\"0123456789abcdef0123456789abcdef\",\"uid\":10}]"), nil
		}
		return json.Marshal([]map[string]string{{"hdr.message-id": messageID}})
	}
	m.run = func(_ context.Context, _ string, _ ...string) error { return nil }
	status, _, err = m.ExpungeRecall(json.RawMessage(`{"tenant_id":` + itoa64(tenant) + `,"recall_id":` + itoa64(recallID2) + `}`))
	if err != nil || status != "local_recalled" {
		t.Fatalf("after hold release status=%q err=%v", status, err)
	}
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
