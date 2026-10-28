package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestArchiveActivityNotificationsAreAtomicScopedAndRevocablePG(t *testing.T) {
	f := newCaseFixture(t, 128)
	f.router.GET("/api/v1/notifications", (&NotificationHandler{Store: store.New(f.app.Pool)}).List)
	caseID, approval, _ := f.ready(t)
	count := func(actor int64) int {
		t.Helper()
		reply := caseResponse(t, f.request(t, actor, http.MethodGet, "/api/v1/notifications", nil), 200)
		var notices []map[string]json.RawMessage
		if err := json.Unmarshal(reply["notifications"], &notices); err != nil {
			t.Fatal(err)
		}
		for _, n := range notices {
			var params map[string]any
			if err := json.Unmarshal(n["params"], &params); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"subject", "reason", "source", "title", "url", "token"} {
				if _, ok := params[key]; ok {
					t.Fatal("notification disclosed sensitive source fields")
				}
			}
		}
		return len(notices)
	}
	if count(f.approver) != 1 || count(f.creator) != 1 || count(f.outsider) != 0 {
		t.Fatal("request/decision notifications did not bind eligible recipients")
	}
	var total int64
	if err := f.platform.QueryRow(t.Context(), "SELECT count(*) FROM notification_outbox WHERE tenant_id=$1", f.tenant).Scan(&total); err != nil || total != 2 {
		t.Fatalf("outbox count=%d err=%v", total, err)
	}
	// Replaying an unchanged state cannot emit a duplicate notification.
	_, currentDigest := f.detail(t, caseID)
	repeated := f.request(t, f.approver, http.MethodPost, fmt.Sprintf("/api/v1/archive/cases/%d/approvals/%d/decision", caseID, approval), map[string]any{"decision": "approved", "digest": currentDigest, "reason": "Repeated independent review"})
	if repeated.Code != http.StatusNotFound {
		t.Fatalf("closed approval replay status %d", repeated.Code)
	}
	if err := f.platform.QueryRow(t.Context(), "SELECT count(*) FROM notification_outbox WHERE tenant_id=$1", f.tenant).Scan(&total); err != nil || total != 2 {
		t.Fatal("same-state event duplicated")
	}
	row, digest := f.detail(t, caseID)
	tx, err := f.app.UserTx(t.Context(), f.tenant, f.creator)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.New(tx).ArchiveCasesRequestApproval(t.Context(), store.ArchiveCasesRequestApprovalParams{TenantID: f.tenant, ActorID: f.creator, Digest: digest, CaseID: caseID, Version: row.Version})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = f.platform.QueryRow(t.Context(), "SELECT count(*) FROM notification_outbox WHERE tenant_id=$1", f.tenant).Scan(&total); err != nil || total != 2 {
		t.Fatal("rolled-back approval emitted a notice")
	}
	// Loss of export grant immediately hides even an already delivered notice.
	var owner int64
	if err = f.platform.QueryRow(t.Context(), "SELECT granted_by FROM compliance_grants WHERE tenant_id=$1 AND user_id=$2 AND scope='export'", f.tenant, f.approver).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	tx, err = f.app.UserTx(t.Context(), f.tenant, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), "UPDATE compliance_grants SET revoked=true WHERE tenant_id=$1 AND user_id=$2 AND scope='export'", f.tenant, f.approver)
	if err == nil {
		err = tx.Commit(t.Context())
	}
	_ = tx.Rollback(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count(f.approver) != 0 || count(f.creator) != 1 {
		t.Fatal("revoked recipient could read historical approval notice")
	}
	// A session from another tenant cannot borrow the notification's identifiers.
	tx, err = f.app.UserTx(t.Context(), f.tenant+10000, f.creator)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := store.New(tx).ListTenantNotifications(t.Context(), store.ListTenantNotificationsParams{TenantID: pgtype.Int8{Int64: f.tenant, Valid: true}})
	_ = tx.Rollback(t.Context())
	if err != nil || len(notices) != 0 {
		t.Fatalf("cross tenant notices: %d %v", len(notices), err)
	}
	// Completion from a service transaction has no user context; the trigger must
	// restore that empty context and notify only the initiating administrator.
	var recallID int64
	if err = f.platform.QueryRow(t.Context(), "INSERT INTO recall_requests(tenant_id,archive_entry_id,mailbox_id,message_id,requested_by) VALUES($1,$2,$3,'<notice-only@example.test>',$4) RETURNING id", f.tenant, f.entry, f.mailbox, f.creator).Scan(&recallID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.platform.Exec(t.Context(), "UPDATE recall_requests SET status='failed',reason='remote_delivery_not_recallable',completed_at=now() WHERE tenant_id=$1 AND id=$2", f.tenant, recallID); err != nil {
		t.Fatal(err)
	}
	if count(f.creator) != 3 {
		t.Fatal("recall request/completion notice missing")
	}
	if _, err = f.platform.Exec(t.Context(), "DELETE FROM user_tenant_roles WHERE tenant_id=$1 AND user_id=$2", f.tenant, f.creator); err != nil {
		t.Fatal(err)
	}
	if w := f.request(t, f.creator, http.MethodGet, "/api/v1/notifications", nil); w.Code != http.StatusForbidden {
		t.Fatal("removed administrator retained archive activity visibility")
	}
	t.Logf("case %d approval %d recall %d: atomic notifications, rollback, cross-tenant and revocation passed", caseID, approval, recallID)
}

func TestArchiveApprovalDeadlineEmitsOneMinimalExpiryNoticePG(t *testing.T) {
	f := newCaseFixture(t, 64)
	f.router.GET("/api/v1/notifications", (&NotificationHandler{Store: store.New(f.app.Pool)}).List)
	expires := time.Now().Add(3 * time.Second).UTC()
	reply := caseResponse(t, f.request(t, f.creator, "POST", "/api/v1/archive/cases", map[string]any{"title": "Short deadline fixture", "purpose": "Verify actual expiry", "expires_at": expires, "request_key": uuid.NewString()}), 201)
	var id int64
	if err := json.Unmarshal(reply["id"], &id); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/v1/archive/cases/%d", id)
	row, _ := f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "POST", base+"/members", map[string]any{"user_id": f.approver, "version": row.Version}), 200)
	row, _ = f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "POST", base+"/sources", map[string]any{"entry_id": f.entry, "version": row.Version}), 200)
	row, digest := f.detail(t, id)
	caseResponse(t, f.request(t, f.creator, "POST", base+"/approvals", map[string]any{"version": row.Version, "digest": digest}), 201)
	deadline := time.Now().Add(5 * time.Second)
	for {
		changed, err := store.New(f.platform.Pool).PlatformExpireArchiveApprovals(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if changed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual deadline did not expire approval")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if changed, err := store.New(f.platform.Pool).PlatformExpireArchiveApprovals(t.Context()); err != nil || changed != 0 {
		t.Fatal("expired approval replayed")
	}
	notices := caseResponse(t, f.request(t, f.creator, http.MethodGet, "/api/v1/notifications", nil), 200)
	if !bytes.Contains(notices["notifications"], []byte("notification.archive_approval_expired")) {
		t.Fatal("requester could not read the minimal expiry fact")
	}
	var actor string
	if err := f.platform.QueryRow(t.Context(), "SELECT coalesce(current_setting('urmail.tenant_id',true),'')").Scan(&actor); err != nil || actor != "" {
		t.Fatal("service trigger leaked tenant context")
	}
}
