package mailboxschedule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

const canceledStatus = "cancelled" //nolint:misspell // Keep the persisted API status.

func scheduleDatabase(t *testing.T) (*db.Pool, *store.Queries, int64, int64) {
	t.Helper()
	p, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	var tenant, mailbox int64
	if err := p.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name) VALUES('schedule','Schedule') RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status) SELECT id,'Schedule','schedule','active' FROM b RETURNING id) SELECT id FROM t`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	var domain int64
	if err := p.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'schedule.example.test','active','fixture',now()) RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes) VALUES($1,$2,'alice','alice@schedule.example.test','fixture',1024) RETURNING id`, tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	return p, store.New(p.Pool), tenant, mailbox
}

func createDueSchedule(t *testing.T, q *store.Queries, tenant, mailbox, uid int64) store.MailboxScheduledSends {
	t.Helper()
	row, err := q.PlatformCreateMailboxScheduledSend(context.Background(), store.PlatformCreateMailboxScheduledSendParams{
		TenantID: tenant, MailboxID: mailbox, DraftFolder: "Drafts", DraftUid: uid, DraftUidvalidity: 9,
		SendAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}, SmtpSecret: "enc:fixture", IdempotencyKey: fmt.Sprintf("fixture-request-%d", uid), RequestHash: "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestScheduleDatabaseCancellationAndClaimFence(t *testing.T) {
	p, q, tenant, mailbox := scheduleDatabase(t)
	ctx := context.Background()
	row := createDueSchedule(t, q, tenant, mailbox, 1)
	claim, err := q.PlatformClaimMailboxScheduledSend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := q.PlatformCancelMailboxScheduledSend(ctx, store.PlatformCancelMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || canceled.Status != canceledStatus {
		t.Fatalf("canceled=%+v err=%v", canceled, err)
	}
	if _, err = q.PlatformBeginMailboxScheduledSendSubmission(ctx, store.PlatformBeginMailboxScheduledSendSubmissionParams{ID: row.ID, ClaimToken: claim.ClaimToken}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("canceled owner could submit", err)
	}
	row = createDueSchedule(t, q, tenant, mailbox, 2)
	claim, err = q.PlatformClaimMailboxScheduledSend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformBeginMailboxScheduledSendSubmission(ctx, store.PlatformBeginMailboxScheduledSendSubmissionParams{ID: row.ID, ClaimToken: claim.ClaimToken}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformCancelMailboxScheduledSend(ctx, store.PlatformCancelMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("submitting cancellation falsely succeeded", err)
	}
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET lease_until=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformRecoverStaleMailboxScheduledSends(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := q.PlatformGetMailboxScheduledSend(ctx, store.PlatformGetMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "uncertain" || recovered.SmtpSecret != "" {
		t.Fatalf("unsafe recovery %+v", recovered)
	}
	if _, err = q.PlatformFinishMailboxScheduledSend(ctx, store.PlatformFinishMailboxScheduledSendParams{ID: row.ID, ClaimToken: claim.ClaimToken, ExpectedStatus: "submitting", FinalStatus: "sent"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("expired owner overwrote recovered status", err)
	}
	if _, err = q.PlatformClaimMailboxScheduledSend(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("uncertain delivery became runnable", err)
	}
}

func TestScheduleDatabasePreparationRecoveryFencesOldWorker(t *testing.T) {
	p, q, tenant, mailbox := scheduleDatabase(t)
	ctx := context.Background()
	row := createDueSchedule(t, q, tenant, mailbox, 1)
	old, err := q.PlatformClaimMailboxScheduledSend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET lease_until=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformRecoverStaleMailboxScheduledSends(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET next_attempt_at=now() WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	newer, err := q.PlatformClaimMailboxScheduledSend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newer.ClaimToken <= old.ClaimToken {
		t.Fatal("claim not fenced")
	}
	if _, err = q.PlatformBeginMailboxScheduledSendSubmission(ctx, store.PlatformBeginMailboxScheduledSendSubmissionParams{ID: row.ID, ClaimToken: old.ClaimToken}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("old owner may submit", err)
	}
	if _, err = q.PlatformBeginMailboxScheduledSendSubmission(ctx, store.PlatformBeginMailboxScheduledSendSubmissionParams{ID: row.ID, ClaimToken: newer.ClaimToken}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleDatabaseConcurrentWorkersSubmitOnce(t *testing.T) {
	_, q, tenant, mailbox := scheduleDatabase(t)
	createDueSchedule(t, q, tenant, mailbox, 42)
	submit := &fakeSubmitter{}
	imap := &fakeIMAP{validity: 9}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := NewWorker(Options{Store: q, Secrets: fakeSecrets{plain: "password"}, Dialer: fakeDialer{imap}, Submitter: submit})
			_, err := w.Step(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if submit.calls != 1 {
		t.Fatalf("SMTP submissions=%d", submit.calls)
	}
}

func TestScheduleDatabaseActiveDraftUniquenessAndMailboxIsolation(t *testing.T) {
	_, q, tenant, mailbox := scheduleDatabase(t)
	row := createDueSchedule(t, q, tenant, mailbox, 1)
	ctx := context.Background()
	if _, err := q.PlatformCreateMailboxScheduledSend(ctx, store.PlatformCreateMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, DraftFolder: "Drafts", DraftUid: 1, DraftUidvalidity: 9, SendAt: row.SendAt, SmtpSecret: "x", IdempotencyKey: "another-request-key", RequestHash: "x"}); err == nil {
		t.Fatal("duplicate active draft accepted")
	}
	if _, err := q.PlatformGetMailboxScheduledSend(ctx, store.PlatformGetMailboxScheduledSendParams{TenantID: tenant + 1, MailboxID: mailbox, ID: row.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-tenant schedule visible", err)
	}
	if _, err := q.PlatformCancelMailboxScheduledSend(ctx, store.PlatformCancelMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox + 1, ID: row.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-mailbox cancellation accepted", err)
	}
	if _, err := q.PlatformCancelMailboxScheduledSend(ctx, store.PlatformCancelMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID}); err != nil {
		t.Fatal(err)
	}
	rows, err := q.PlatformListMailboxScheduledSends(ctx, store.PlatformListMailboxScheduledSendsParams{TenantID: tenant, MailboxID: mailbox, PageLimit: 50})
	if err != nil || len(rows) != 1 || rows[0].Status != canceledStatus {
		t.Fatalf("terminal list=%+v err=%v", rows, err)
	}
}

func TestScheduleDatabaseCancelAtWorkerBoundaries(t *testing.T) {
	_, q, tenant, mailbox := scheduleDatabase(t)
	ctx := context.Background()
	for _, phase := range []string{"fetching", "submitting"} {
		t.Run(phase, func(t *testing.T) {
			uid := int64(41)
			if phase == "submitting" {
				uid = 42
			}
			row := createDueSchedule(t, q, tenant, mailbox, uid)
			started := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			imap := &fakeIMAP{validity: 9}
			submit := &fakeSubmitter{}
			if phase == "fetching" {
				imap.beforeFetch = func(ctx context.Context) {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
			} else {
				submit.before = func() { close(started); <-release }
			}
			w := NewWorker(Options{Store: q, Secrets: fakeSecrets{plain: "password"}, Dialer: fakeDialer{imap}, Submitter: submit})
			go func() { _, err := w.Step(ctx); done <- err }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("worker did not reach barrier")
			}
			_, cancelErr := q.PlatformCancelMailboxScheduledSend(ctx, store.PlatformCancelMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not finish")
			}
			if phase == "fetching" {
				if cancelErr != nil || submit.calls != 0 {
					t.Fatalf("cancel during fetch: err=%v submissions=%d", cancelErr, submit.calls)
				}
			} else {
				if !errors.Is(cancelErr, pgx.ErrNoRows) || submit.calls != 1 {
					t.Fatalf("cancel during SMTP: err=%v submissions=%d", cancelErr, submit.calls)
				}
			}
		})
	}
}

func TestScheduleDatabaseHeldDraftCannotBeRescheduled(t *testing.T) {
	p, q, tenant, mailbox := scheduleDatabase(t)
	row := createDueSchedule(t, q, tenant, mailbox, 1)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET status='held',smtp_secret='' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	args := store.PlatformCreateMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, DraftFolder: row.DraftFolder, DraftUid: row.DraftUid, DraftUidvalidity: row.DraftUidvalidity, SendAt: row.SendAt, SmtpSecret: "enc:new", IdempotencyKey: "attempt-to-bypass-hold", RequestHash: "new"}
	if _, err := q.PlatformCreateMailboxScheduledSend(ctx, args); err == nil {
		t.Fatal("held draft was rescheduled with a new request key")
	}
	args.DraftUid = 2
	args.IdempotencyKey = "explicitly-new-draft"
	if _, err := q.PlatformCreateMailboxScheduledSend(ctx, args); err != nil {
		t.Fatalf("a distinct user-selected draft was blocked: %v", err)
	}
}

func TestScheduleAcceptedCopyDurabilityAndOwnerFence(t *testing.T) {
	p, q, tenant, mailbox := scheduleDatabase(t)
	ctx := t.Context()
	original := createDueSchedule(t, q, tenant, mailbox, 87)
	claim, err := q.PlatformClaimMailboxScheduledSend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformBeginMailboxScheduledSendSubmission(ctx, store.PlatformBeginMailboxScheduledSendSubmissionParams{ID: claim.ID, ClaimToken: claim.ClaimToken}); err != nil {
		t.Fatal(err)
	}
	raw := []byte("Message-ID: <durable-copy@fixture>\r\nFrom: team@example.test\r\n\r\naccepted payload\r\n")
	accepted, err := q.PlatformAcceptMailboxScheduledSend(ctx, store.PlatformAcceptMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, ID: original.ID, ClaimToken: claim.ClaimToken, SentRaw: raw})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != "sent" || accepted.SentCopyState != "pending" || !bytes.Equal(accepted.SentRaw, raw) || !accepted.SentAt.Valid || accepted.SmtpSecret == "" {
		t.Fatal("acceptance and repair payload not atomic")
	}
	if _, err = q.PlatformClaimOwnedScheduledSentCopy(ctx, store.PlatformClaimOwnedScheduledSentCopyParams{TenantID: tenant + 100, MailboxID: mailbox, ID: original.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-tenant repair claimed", err)
	}
	if _, err = p.Exec(ctx, `UPDATE mailbox_scheduled_sends SET lease_until=now()-interval '1 second' WHERE id=$1`, original.ID); err != nil {
		t.Fatal(err)
	}
	repair, err := q.PlatformClaimScheduledSentCopy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if repair.ClaimToken <= claim.ClaimToken {
		t.Fatal("repair failed to fence previous owner")
	}
	if _, err = q.PlatformCompleteScheduledSentCopy(ctx, store.PlatformCompleteScheduledSentCopyParams{TenantID: tenant, MailboxID: mailbox, ID: original.ID, ClaimToken: claim.ClaimToken}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("old owner completed", err)
	}
	imap := &fakeIMAP{validity: 9, copyStored: true}
	if err = RepairSentCopy(ctx, q, repair, imap); err != nil {
		t.Fatal(err)
	}
	done, err := q.PlatformGetMailboxScheduledSend(ctx, store.PlatformGetMailboxScheduledSendParams{TenantID: tenant, MailboxID: mailbox, ID: original.ID})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "sent" || done.SentCopyState != "complete" || len(done.SentRaw) != 0 || done.SmtpSecret != "" || done.SentAt != accepted.SentAt || imap.appendCalls != 0 {
		t.Fatal("repair lost acceptance time, duplicated APPEND, or retained sensitive payload")
	}
	var denied bool
	err = p.QueryRow(ctx, `SELECT NOT has_table_privilege('urmail_app','mailbox_scheduled_sends','SELECT')`).Scan(&denied)
	if err != nil || !denied {
		t.Fatal("app role can read temporary raw", err)
	}
}
