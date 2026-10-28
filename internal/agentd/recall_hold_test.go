package agentd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A held archive source for the same Message-ID blocks every local cleanup,
// even when the requested source itself is free. Clearing the hold never
// authorizes an old unreviewed intent; a fresh explicit review is required.
func TestRecallRefusedWhenSameMessageHeld(t *testing.T) {
	f := newRecallReviewFixture(t)
	var held, legacy int64
	if err := f.platform.QueryRow(t.Context(), "INSERT INTO archive_entries(tenant_id,mailbox_id,message_id,path,received_at,raw_sha256,legal_hold)VALUES($1,$2,$3,'related-held',now(),$4,true)RETURNING id", f.tenant, f.mailbox, f.messageID, strings.Repeat("b", 64)).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if err := f.platform.QueryRow(t.Context(), "INSERT INTO recall_requests(tenant_id,message_id,requested_by,archive_entry_id,mailbox_id)VALUES($1,$2,$3,$4,$5)RETURNING id", f.tenant, f.messageID, f.actor, f.entry, f.mailbox).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	originalOutput, originalRun := f.manager.output, f.manager.run
	f.manager.output = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("doveadm invoked despite a related legal hold")
		return nil, nil
	}
	f.manager.run = func(context.Context, string, ...string) error {
		t.Fatal("expunge invoked despite a related legal hold")
		return nil
	}
	raw, _ := json.Marshal(RecallParams{TenantID: f.tenant, RecallID: legacy})
	state, _, err := f.manager.ExpungeRecall(raw)
	var reason string
	if err != nil || state != "failed" {
		t.Fatalf("held result %s %v", state, err)
	}
	if err = f.platform.QueryRow(t.Context(), "SELECT reason FROM recall_requests WHERE tenant_id=$1 AND id=$2", f.tenant, legacy).Scan(&reason); err != nil || reason != "legal_hold_conflict" {
		t.Fatalf("hold conflict %q %v", reason, err)
	}
	if _, err = f.platform.Exec(t.Context(), "UPDATE archive_entries SET legal_hold=false WHERE tenant_id=$1 AND id=$2", f.tenant, held); err != nil {
		t.Fatal(err)
	}
	// Replaying the old durable refusal cannot query or delete mailbox contents.
	if state, _, err = f.manager.ExpungeRecall(raw); err != nil || state != "failed" {
		t.Fatal("hold release promoted an old intent", state, err)
	}
	f.manager.output, f.manager.run = originalOutput, originalRun
	reviewed := f.review(t, f.actor, []uint32{1, 2})
	current := f.execute(t, reviewed)
	if current.Status != "local_recalled" || len(f.mutations) != 2 {
		t.Fatalf("fresh reviewed release %s, mutations=%v", current.Status, f.mutations)
	}
}
