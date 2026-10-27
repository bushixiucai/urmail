package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/testdb"
)

func recallJSON(tenant, recall int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"tenant_id":%d,"recall_id":%d}`, tenant, recall))
}

// ARC-003: the agent itself must refuse a recall whose target copy was placed
// under legal hold after the request was created, and repeating the same
// recall must be a no-op: no second doveadm call, no second audit event, and
// the durable status never changes.
func TestRecallDirectHoldRefusedAndCompletionIdempotent(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	m := &ApplyManager{}
	m.pool = platform
	ctx := context.Background()

	var brand, tenant, user, domain, mailbox int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('idem','Idem') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Idem','idem','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('idem@example.test','fixture-only') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO domains(tenant_id,fqdn,status,provisioning_state,verification_token,verified_at) VALUES($1,'idem.test','active','ready',$2,now()) RETURNING id", tenant, strings.Repeat("d", 64)).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'one','one@idem.test','fixture-only','active') RETURNING id", tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	insertEntry := func(messageID string, hold bool) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, "INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at,raw_sha256,legal_hold) VALUES($1,$2,$3,'fixture',now(),$4,$5) RETURNING id",
			tenant, mailbox, messageID, strings.Repeat("e", 64), hold).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertRecall := func(entry int64, messageID string) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, "INSERT INTO recall_requests(tenant_id,message_id,requested_by,archive_entry_id,mailbox_id) VALUES($1,$2,$3,$4,$5) RETURNING id",
			tenant, messageID, user, entry, mailbox).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// A hold set after the request was created must refuse the destructive
	// step inside the privileged transaction, with no doveadm call.
	heldID := insertEntry("<held-first@example.test>", false)
	recallHeld := insertRecall(heldID, "<held-first@example.test>")
	if _, err := platform.Exec(ctx, "UPDATE archive_entries SET legal_hold=true WHERE id=$1", heldID); err != nil {
		t.Fatal(err)
	}
	m.output = func(_ context.Context, _ ...string) ([]byte, error) {
		t.Fatal("doveadm invoked while the target copy is held")
		return nil, nil
	}
	m.run = func(_ context.Context, _ string, _ ...string) error {
		t.Fatal("expunge executed while the target copy is held")
		return nil
	}
	status, _, err := m.ExpungeRecall(recallJSON(tenant, recallHeld))
	if err != nil || status != "failed" {
		t.Fatalf("held recall status=%q err=%v", status, err)
	}
	var reason string
	if err := platform.QueryRow(ctx, "SELECT reason FROM recall_requests WHERE id=$1", recallHeld).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "legal_hold" {
		t.Fatalf("reason=%q, want legal_hold", reason)
	}

	// Completing the same recall twice is one local destruction and one audit
	// event; the second invocation observes the completed status and stops.
	freeID := insertEntry("<idem@example.test>", false)
	recallID := insertRecall(freeID, "<idem@example.test>")
	m.output = func(_ context.Context, got ...string) ([]byte, error) {
		if len(got) > 2 && got[2] == "search" {
			return []byte("[{\"mailbox-guid\":\"0123456789abcdef0123456789abcdef\",\"uid\":7}]"), nil
		}
		return json.Marshal([]map[string]string{{"hdr.message-id": "<idem@example.test>"}})
	}
	m.run = func(_ context.Context, _ string, _ ...string) error { return nil }
	for i := 0; i < 2; i++ {
		status, _, err = m.ExpungeRecall(recallJSON(tenant, recallID))
		if err != nil || status != "local_recalled" {
			t.Fatalf("pass %d status=%q err=%v", i, status, err)
		}
	}
	var audits int
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='compliance.recall.complete' AND target_id=$2", tenant, fmt.Sprintf("%d", recallID)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audit events=%d, want exactly 1", audits)
	}
}
