package mailboxsend

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type receiptMail struct {
	webmail.Client
	stored               bool
	onFind               func()
	appends              int
	deleted              bool
	appendErr, errorFind error
	raw                  []byte
}

func (f *receiptMail) FindSentCopy(_ context.Context, raw []byte) (webmail.DraftSaveResult, bool, error) {
	if f.onFind != nil {
		f.onFind()
	}
	return webmail.DraftSaveResult{}, f.stored && bytes.Equal(f.raw, raw), f.errorFind
}
func (f *receiptMail) SaveSentCopy(_ context.Context, raw []byte) (webmail.DraftSaveResult, error) {
	f.appends++
	f.stored = true
	f.raw = append([]byte{}, raw...)
	return webmail.DraftSaveResult{UID: 1, UIDValidity: 9}, f.appendErr
}
func (f *receiptMail) FolderUIDValidity(context.Context, string) (uint32, error) { return 9, nil }
func (f *receiptMail) DeleteMessages(context.Context, string, []uint32, bool) error {
	f.deleted = true
	return nil
}
func (f *receiptMail) Close() error { return nil }

type receiptDialer struct {
	client webmail.Client
	err    error
	calls  int
}

func (f *receiptDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	f.calls++
	return f.client, f.err
}

type receiptSubmitter struct {
	calls  atomic.Int32
	err    error
	before func(webmail.Outgoing)
}

func (f *receiptSubmitter) Send(_ context.Context, _, _ string, out webmail.Outgoing) (webmail.Submission, error) {
	f.calls.Add(1)
	if f.before != nil {
		f.before(out)
	}
	return webmail.Submission{Raw: out.Raw}, f.err
}

func receiptFixture(t *testing.T) (Service, *store.Queries, *db.Pool, *db.Pool, int64, int64, *receiptMail, *receiptSubmitter) {
	t.Helper()
	platform, app := testdb.OpenMigrated(t)
	ctx := t.Context()
	var tenant, mailbox int64
	if err := platform.QueryRow(ctx, `WITH b AS(INSERT INTO brands(slug,name)VALUES('receipt-test','Receipt')RETURNING id),t AS(INSERT INTO tenants(brand_id,name,slug,status)SELECT id,'Receipt','receipt-test','active' FROM b RETURNING id)SELECT id FROM t`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	var domain int64
	if err := platform.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at)VALUES($1,'receipt.example.test','active','fixture',now())RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes)VALUES($1,$2,'alice','alice@receipt.example.test','fixture-hash',1024)RETURNING id`, tenant, domain).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	client := &receiptMail{}
	submit := &receiptSubmitter{}
	return Service{Pool: platform, Secrets: box, Dialer: &receiptDialer{client: client}, Submitter: submit}, store.New(platform.Pool), platform, app, tenant, mailbox, client, submit
}
func frozenMessage() webmail.Outgoing {
	out := webmail.FreezeOutgoing(webmail.Outgoing{From: "alice@receipt.example.test", To: []string{"recipient@example.test"}, Bcc: []string{"hidden@example.test"}, Subject: "Receipt fixture", Text: "confidential fixture body"})
	out.Raw = []byte(webmail.BuildMIME(out.From, out))
	return out
}

func TestImmediateReceiptConcurrentReplayAndFrozenSubmission(t *testing.T) {
	s, q, p, app, tenant, mailbox, _, submit := receiptFixture(t)
	ctx := t.Context()
	out := frozenMessage()
	ref := DraftRef{Folder: "Drafts", UID: 42, Validity: 9}
	var created atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	submit.before = func(sent webmail.Outgoing) {
		row, e := q.PlatformGetSendReceiptByKey(ctx, store.PlatformGetSendReceiptByKeyParams{TenantID: tenant, MailboxID: mailbox, RequestKey: "concurrent-send-0001"})
		if e != nil || row.Status != "submitting" {
			t.Error("SMTP entered without durable submitting")
		}
		raw, e := s.Secrets.DecryptString(row.RawSecret)
		if e != nil || raw != string(sent.Raw) {
			t.Error("SMTP bytes differ from frozen encrypted intention")
		}
	}
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row, newRow, e := s.Prepare(ctx, tenant, mailbox, "concurrent-send-0001", strings.Repeat("a", 64), "secret-password", out, ref, 0)
			if e != nil {
				errs <- e
				return
			}
			if newRow {
				created.Add(1)
				_, e = s.Submit(ctx, row, "alice@receipt.example.test", "secret-password", out)
				if e != nil {
					errs <- e
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if created.Load() != 1 || submit.calls.Load() != 1 {
		t.Fatalf("created=%d SMTP=%d", created.Load(), submit.calls.Load())
	}
	row, err := q.PlatformGetSendReceiptByKey(ctx, store.PlatformGetSendReceiptByKeyParams{TenantID: tenant, MailboxID: mailbox, RequestKey: "concurrent-send-0001"})
	if err != nil || row.Status != "accepted" {
		t.Fatalf("receipt %s %v", row.Status, err)
	}
	if strings.Contains(row.RawSecret, "confidential") || strings.Contains(row.SmtpSecret, "secret-password") || !strings.HasPrefix(row.RawSecret, "enc:v1:") {
		t.Fatal("recovery material stored in plaintext")
	}
	if _, _, err = s.Prepare(ctx, tenant, mailbox, "concurrent-send-0001", strings.Repeat("b", 64), "secret-password", out, ref, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("different body accepted: %v", err)
	}
	if _, _, err = s.Prepare(ctx, tenant, mailbox, "different-send-0001", strings.Repeat("c", 64), "secret-password", out, ref, 0); !errors.Is(err, ErrDraftLocked) {
		t.Fatalf("accepted draft resubmitted: %v", err)
	}
	if _, err = q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant + 1, MailboxID: mailbox, ID: row.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-tenant receipt exposed")
	}
	tx, err := app.MailboxTx(ctx, tenant, mailbox+1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM mailbox_send_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("RLS leaked count=%d err=%v", count, err)
	}
	var readOnly bool
	if err = p.QueryRow(ctx, `SELECT has_table_privilege('urmail_app','mailbox_send_receipts','SELECT') AND NOT has_table_privilege('urmail_app','mailbox_send_receipts','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') AND NOT has_sequence_privilege('urmail_app','mailbox_send_receipts_id_seq','USAGE,SELECT,UPDATE')`).Scan(&readOnly); err != nil || !readOnly {
		t.Fatal("receipt app privileges are not metadata-read-only", err)
	}
	for _, mutation := range []string{`UPDATE mailbox_send_receipts SET status='rejected' WHERE id=$1`, `DELETE FROM mailbox_send_receipts WHERE id=$1`, `INSERT INTO mailbox_send_receipts(tenant_id,mailbox_id,request_key,request_hash,message_id,raw_secret) SELECT tenant_id,mailbox_id,'forged-receipt-0001',request_hash,message_id,raw_secret FROM mailbox_send_receipts WHERE id=$1`} {
		ownerTx, e := app.MailboxTx(ctx, tenant, mailbox)
		if e != nil {
			t.Fatal(e)
		}
		var visible int
		if e = ownerTx.QueryRow(ctx, `SELECT count(*) FROM mailbox_send_receipts WHERE id=$1`, row.ID).Scan(&visible); e != nil || visible != 1 {
			_ = ownerTx.Rollback(ctx)
			t.Fatal("owner cannot read their receipt", e)
		}
		_, e = ownerTx.Exec(ctx, mutation, row.ID)
		_ = ownerTx.Rollback(ctx)
		var pgerr *pgconn.PgError
		if !errors.As(e, &pgerr) || pgerr.Code != "42501" {
			t.Fatalf("app role could mutate durable receipt: %v", e)
		}
	}
}

func TestImmediateCopyLostAppendAckSevenDaysAndManualRepair(t *testing.T) {
	s, q, p, _, tenant, mailbox, client, submit := receiptFixture(t)
	ctx := t.Context()
	out := frozenMessage()
	row, _, err := s.Prepare(ctx, tenant, mailbox, "copy-repair-send-0001", strings.Repeat("a", 64), "secret-password", out, DraftRef{Folder: "Drafts", UID: 42, Validity: 9}, 0)
	if err != nil {
		t.Fatal(err)
	}
	row, err = s.Submit(ctx, row, "alice@receipt.example.test", "secret-password", out)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimOwner(ctx, tenant, mailbox, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	client.appendErr = webmail.ErrSentCopyPending
	if err = s.Repair(ctx, claim, client); !errors.Is(err, webmail.ErrSentCopyPending) {
		t.Fatalf("lost append ack=%v", err)
	}
	if client.deleted {
		t.Fatal("pending copy deleted source draft")
	}
	_, err = p.Exec(ctx, `UPDATE mailbox_send_receipts SET copy_pending_since=now()-interval '7 days',next_attempt_at=now()-interval '1 second' WHERE id=$1`, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StepCopy(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || row.SentCopyState != "held" || row.RawSecret == "" || row.SmtpSecret != "" {
		t.Fatalf("expiry lost payload/credential: %+v %v", row, err)
	}
	originalSince := row.CopyPendingSince
	client.appendErr = nil
	claim, err = s.ClaimOwner(ctx, tenant, mailbox, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Repair(ctx, claim, client); err != nil {
		t.Fatal(err)
	}
	row, err = q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || row.SentCopyState != "complete" || row.RawSecret != "" || row.SmtpSecret != "" || row.CopyPendingSince != originalSince {
		t.Fatal("manual copy repair did not preserve deadline/clean secrets")
	}
	if submit.calls.Load() != 1 || client.appends != 1 || !client.deleted {
		t.Fatalf("repair resent=%d appends=%d deleted=%v", submit.calls.Load(), client.appends, client.deleted)
	}
}

func TestImmediateCrashRecoveryRestoreAndCredentialRevocation(t *testing.T) {
	s, q, p, _, tenant, mailbox, client, submit := receiptFixture(t)
	ctx := t.Context()
	out := frozenMessage()
	row, _, err := s.Prepare(ctx, tenant, mailbox, "crashed-send-0001", strings.Repeat("a", 64), "secret-password", out, DraftRef{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformBeginSendReceipt(ctx, store.PlatformBeginSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `UPDATE mailbox_send_receipts SET lease_until=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StepCopy(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || row.Status != "uncertain" || row.SmtpSecret != "" || submit.calls.Load() != 0 {
		t.Fatal("crashed submission was replayed or credentials survived")
	}
	accepted, _, err := s.Prepare(ctx, tenant, mailbox, "accepted-send-0001", strings.Repeat("b", 64), "secret-password", out, DraftRef{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err = s.Submit(ctx, accepted, "alice@receipt.example.test", "secret-password", out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `UPDATE mailboxes SET password_hash='changed' WHERE tenant_id=$1 AND id=$2`, tenant, mailbox); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StepCopy(ctx); err != nil {
		t.Fatal(err)
	}
	accepted, err = q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: accepted.ID})
	if err != nil || accepted.SentCopyState != "held" || accepted.SmtpSecret != "" || client.appends != 0 {
		t.Fatal("revoked credentials reached IMAP")
	}
	if err = db.HoldRestoredSideEffects(ctx, p.Config().ConnConfig.ConnString(), "send-receipt-restore"); err != nil {
		t.Fatal(err)
	}
	row, err = q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || row.Status != "uncertain" || row.LastError != "RESTORE_RECONCILIATION_REQUIRED" || row.RawSecret == "" {
		t.Fatal("restore did not preserve/hold uncertain material")
	}
	if _, err = p.Exec(ctx, `SELECT urmail_begin_restore_quarantine()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Prepare(ctx, tenant, mailbox, "quarantine-send-0001", strings.Repeat("c", 64), "secret-password", out, DraftRef{}, 0); err == nil {
		t.Fatal("restore quarantine accepted a new intention")
	}
}

func TestSevenDayBoundaryDoesNotResetOnDefer(t *testing.T) {
	s, q, p, _, tenant, mailbox, _, _ := receiptFixture(t)
	ctx := t.Context()
	out := frozenMessage()
	row, _, err := s.Prepare(ctx, tenant, mailbox, "boundary-send-0001", strings.Repeat("a", 64), "secret-password", out, DraftRef{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	row, err = s.Submit(ctx, row, "alice@receipt.example.test", "secret-password", out)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Exec(ctx, `UPDATE mailbox_send_receipts SET copy_pending_since=now()-interval '7 days'+interval '1 minute' WHERE id=$1`, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := q.PlatformClaimSendCopy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	since := claim.CopyPendingSince
	if _, err = q.PlatformDeferSendCopy(ctx, store.PlatformDeferSendCopyParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID, ClaimToken: claim.ClaimToken}); err != nil {
		t.Fatal(err)
	}
	current, err := q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || current.CopyPendingSince != since || current.SentCopyState == "held" {
		t.Fatal("defer reset deadline or held too early")
	}
	if !current.NextAttemptAt.Time.After(time.Now()) {
		t.Fatal("copy did not back off")
	}
}

func TestExplicitNewAttemptCanReleaseOnlyMatchingUncertainDraft(t *testing.T) {
	s, q, _, _, tenant, mailbox, _, submit := receiptFixture(t)
	ctx := t.Context()
	out := frozenMessage()
	ref := DraftRef{Folder: "Drafts", UID: 42, Validity: 9}
	original, _, err := s.Prepare(ctx, tenant, mailbox, "uncertain-original-0001", strings.Repeat("a", 64), "secret-password", out, ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	submit.err = webmail.ErrSubmitUncertain
	original, err = s.Submit(ctx, original, "alice@receipt.example.test", "secret-password", out)
	if err != nil || original.Status != "uncertain" {
		t.Fatal("fixture not uncertain")
	}
	if _, _, err = s.Prepare(ctx, tenant, mailbox, "explicit-attempt-0001", strings.Repeat("b", 64), "secret-password", out, ref, 0); !errors.Is(err, ErrDraftLocked) {
		t.Fatal("uncertain source unlocked without acknowledgement")
	}
	next, created, err := s.Prepare(ctx, tenant, mailbox, "explicit-attempt-0001", strings.Repeat("b", 64), "secret-password", out, ref, original.ID)
	if err != nil || !created {
		t.Fatal("explicit matching acknowledgement refused", err)
	}
	submit.err = nil
	next, err = s.Submit(ctx, next, "alice@receipt.example.test", "secret-password", out)
	if err != nil || next.Status != "accepted" || submit.calls.Load() != 2 {
		t.Fatal("explicit independent attempt not submitted once")
	}
	original, err = q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: original.ID})
	if err != nil || !original.DraftReleased || original.RawSecret != "" || original.SmtpSecret != "" {
		t.Fatal("acknowledged attempt retained source lock or unnecessary recovery secrets")
	}
	if _, _, err = s.Prepare(ctx, tenant, mailbox, "third-attempt-0001", strings.Repeat("c", 64), "secret-password", out, ref, next.ID); !errors.Is(err, ErrDraftLocked) {
		t.Fatal("accepted source released by uncertain acknowledgement")
	}
}

func TestImmediateAcceptedStateWriteOutlivesClientCancellation(t *testing.T) {
	s, q, _, _, tenant, mailbox, _, submit := receiptFixture(t)
	out := frozenMessage()
	row, _, err := s.Prepare(t.Context(), tenant, mailbox, "client-disconnect-0001", strings.Repeat("a", 64), "secret-password", out, DraftRef{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, disconnect := context.WithCancel(t.Context())
	submit.before = func(webmail.Outgoing) { disconnect() }
	row, err = s.Submit(clientCtx, row, "alice@receipt.example.test", "secret-password", out)
	if err != nil || row.Status != "accepted" {
		t.Fatalf("client disconnect lost accepted state %s %v", row.Status, err)
	}
	stored, err := q.PlatformGetSendReceipt(t.Context(), store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || stored.Status != "accepted" {
		t.Fatal("accepted state not durable after disconnect")
	}
}

func TestImmediateSMTPAcceptanceBeforeDatabaseLossRecoversUncertainWithoutRetry(t *testing.T) {
	s, _, p, _, tenant, mailbox, _, submit := receiptFixture(t)
	ctx := t.Context()
	out := frozenMessage()
	dsn := p.Config().ConnConfig.ConnString()
	row, _, err := s.Prepare(ctx, tenant, mailbox, "acceptance-db-loss-0001", strings.Repeat("a", 64), "secret-password", out, DraftRef{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	submit.before = func(webmail.Outgoing) { p.Close() }
	row, err = s.Submit(ctx, row, "alice@receipt.example.test", "secret-password", out)
	if err != nil || row.Status != "uncertain" || submit.calls.Load() != 1 {
		t.Fatal("database loss after SMTP did not remain uncertain")
	}
	fresh, err := db.OpenPlatform(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.Close)
	s.Pool = fresh
	q := store.New(fresh.Pool)
	stored, err := q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || stored.Status != "submitting" || stored.RawSecret == "" {
		t.Fatal("pre-SMTP recovery intention lost")
	}
	if _, err = fresh.Exec(ctx, `UPDATE mailbox_send_receipts SET lease_until=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StepCopy(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || stored.Status != "uncertain" || submit.calls.Load() != 1 {
		t.Fatal("recovery blindly repeated accepted SMTP submission")
	}
}

func TestAutomaticCopyRevocationDuringFindCannotAppend(t *testing.T) {
	s, q, p, _, tenant, mailbox, client, _ := receiptFixture(t)
	ctx := t.Context()
	out := frozenMessage()
	row, _, err := s.Prepare(ctx, tenant, mailbox, "copy-revocation-0001", strings.Repeat("a", 64), "secret-password", out, DraftRef{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	row, err = s.Submit(ctx, row, "alice@receipt.example.test", "secret-password", out)
	if err != nil {
		t.Fatal(err)
	}
	client.onFind = func() {
		if _, e := p.Exec(ctx, `UPDATE mailboxes SET password_hash='changed-during-copy-find' WHERE tenant_id=$1 AND id=$2`, tenant, mailbox); e != nil {
			t.Fatal(e)
		}
	}
	if _, err = s.StepCopy(ctx); !errors.Is(err, webmail.ErrSentCopyPending) {
		t.Fatalf("revoked copy repair error=%v", err)
	}
	current, err := q.PlatformGetSendReceipt(ctx, store.PlatformGetSendReceiptParams{TenantID: tenant, MailboxID: mailbox, ID: row.ID})
	if err != nil || current.SentCopyState != "held" || current.SmtpSecret != "" || current.RawSecret == "" || client.appends != 0 {
		t.Fatal("revoked automatic copy reached APPEND or lost recovery material")
	}
}
