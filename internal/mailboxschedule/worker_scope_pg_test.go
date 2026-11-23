package mailboxschedule

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/store"
)

// TestWorkerStepOwnsPlatformScope proves the real background worker executes
// its platform statements when the caller supplies no scope at all: the
// runtime failure "PlatformGetLegacyScheduledCopy requires platform scope"
// came from the product worker path, not from a fixture. It seeds a legacy
// plaintext scheduled copy and requires Step to seal it and otherwise stay
// idle, for both an unbound and a tenant-scoped caller context.
func TestWorkerStepOwnsPlatformScope(t *testing.T) {
	p, _, tenant, mailbox := scheduleDatabase(t)
	ctx := context.Background()
	legacy, err := p.Exec(ctx, `INSERT INTO mailbox_scheduled_sends(
		tenant_id,mailbox_id,draft_folder,draft_uid,send_at,status,smtp_secret,sent_raw,sent_copy_state)
		VALUES($1,$2,'Drafts',1,now()-interval '1 hour','sent','enc:fixture','legacy raw bytes','none')`, tenant, mailbox)
	if err != nil {
		t.Fatalf("seed legacy copy: %v", err)
	}
	if legacy.RowsAffected() != 1 {
		t.Fatalf("legacy seed rows=%d", legacy.RowsAffected())
	}
	worker := NewWorker(Options{Pool: p, Secrets: fakeSecrets{}, Dialer: fakeDialer{}, Submitter: &fakeSubmitter{}})
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"unbound caller", context.Background()},
		{"tenant-scoped caller", isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: tenant})},
	} {
		worked, err := worker.Step(tc.ctx)
		if err != nil {
			t.Fatalf("%s: worker step: %v", tc.name, err)
		}
		if worked {
			t.Fatalf("%s: worker claimed work with an empty queue", tc.name)
		}
	}
	var raw []byte
	var secret string
	if err := p.QueryRow(ctx, `SELECT sent_raw, sent_raw_secret FROM mailbox_scheduled_sends WHERE tenant_id=$1 AND mailbox_id=$2`, tenant, mailbox).Scan(&raw, &secret); err != nil {
		t.Fatalf("read sealed copy: %v", err)
	}
	if raw != nil {
		t.Fatalf("legacy plaintext copy still present: %q", raw)
	}
	if !strings.HasPrefix(secret, "enc:v1:") || !strings.HasSuffix(secret, "legacy raw bytes") {
		t.Fatalf("legacy copy not sealed with the worker's box: %q", secret)
	}
}

// TestPlatformScopeNotWidenedByWorkerBinding proves the worker's own binding
// is local: entry-point contexts still cannot run platform statements, and
// tenant parameters are still checked against the authenticated tenant.
func TestPlatformScopeNotWidenedByWorkerBinding(t *testing.T) {
	p, _, tenant, mailbox := scheduleDatabase(t)
	q := db.Querier(p.Pool)
	tenantCtx := isolation.WithScope(context.Background(), isolation.ScopeContext{Scope: isolation.ScopeTenant, TenantID: tenant})

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"unbound", context.Background()},
		{"tenant", tenantCtx},
	} {
		if _, err := q.PlatformGetLegacyScheduledCopy(tc.ctx); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
			t.Fatalf("%s context ran a platform recovery query: %v", tc.name, err)
		}
		if _, err := q.PlatformHoldUnauthorizedScheduledCopies(tc.ctx); !errors.Is(err, isolation.ErrPlatformScopeRequired) {
			t.Fatalf("%s context ran a platform hold: %v", tc.name, err)
		}
	}
	if _, err := q.GetMailboxByID(tenantCtx, store.GetMailboxByIDParams{TenantID: tenant + 1, ID: mailbox}); !errors.Is(err, isolation.ErrCrossTenantAccess) {
		t.Fatalf("cross-tenant mailbox read not refused: %v", err)
	}
	if _, err := q.GetMailboxByID(tenantCtx, store.GetMailboxByIDParams{TenantID: tenant, ID: mailbox}); err != nil {
		t.Fatalf("own-tenant mailbox read failed: %v", err)
	}
}
