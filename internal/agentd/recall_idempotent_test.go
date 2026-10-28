package agentd

import (
	"context"
	"fmt"
	"testing"
)

// A legal hold added after confirmation blocks the privileged step. After a
// fresh review, completed replay performs neither another expunge nor audit.
func TestRecallDirectHoldRefusedAndCompletionIdempotent(t *testing.T) {
	f := newRecallReviewFixture(t)
	reviewed := f.review(t, f.actor, []uint32{1, 2})
	if _, err := f.platform.Exec(t.Context(), "UPDATE archive_entries SET legal_hold=true WHERE tenant_id=$1 AND id=$2", f.tenant, f.entry); err != nil {
		t.Fatal(err)
	}
	originalOutput, originalRun := f.manager.output, f.manager.run
	f.manager.output = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("doveadm invoked while source is held")
		return nil, nil
	}
	f.manager.run = func(context.Context, string, ...string) error {
		t.Fatal("expunge invoked while source is held")
		return nil
	}
	held := f.execute(t, reviewed)
	if held.Status != "failed" || held.Reason.String != "legal_hold" {
		t.Fatalf("direct hold %s %s", held.Status, held.Reason.String)
	}
	if _, err := f.platform.Exec(t.Context(), "UPDATE archive_entries SET legal_hold=false WHERE tenant_id=$1 AND id=$2", f.tenant, f.entry); err != nil {
		t.Fatal(err)
	}
	f.manager.output, f.manager.run = originalOutput, originalRun
	confirmed := f.review(t, f.actor, []uint32{1, 2})
	for i := 0; i < 2; i++ {
		if current := f.execute(t, confirmed); current.Status != "local_recalled" {
			t.Fatalf("replay %d: %s", i, current.Status)
		}
	}
	if len(f.mutations) != 2 {
		t.Fatalf("completed intent repeated destruction: %v", f.mutations)
	}
	var audits int
	if err := f.platform.QueryRow(t.Context(), "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='compliance.recall.review_complete' AND target_id=$2", f.tenant, fmt.Sprint(confirmed.ID)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("review completion facts=%d, want held refusal plus one successful reviewed completion", audits)
	}
}
