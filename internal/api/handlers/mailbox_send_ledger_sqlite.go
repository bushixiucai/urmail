package handlers

// SQLite send ledger + draft journal for the slice runtime
// (pm-webmail-writepath-1). This is the slice-engine mirror of the PG
// platform send-receipts engine (internal/mailboxsend + store platform
// queries): exactly-once submission per idempotency key, single live
// submission per source draft, a claim-lease submit protocol with LAZY
// stale recovery (a slice runtime has no background worker, so
// `submitting` rows older than their lease resolve to `uncertain` on the
// next read), sent-copy reconciliation state, and audit events into the
// platform audit_logs table.
//
// Tenant scope is enforced at the application layer: every statement
// binds tenant_id and mailbox_id to the values the handler sourced from
// the authenticated mailbox session (architecture ruling ①).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/timeutil"
)

// Send ledger states (mirrors the PG engine's status vocabulary).
const (
	ledgerCreated    = "created"
	ledgerSubmitting = "submitting"
	ledgerAccepted   = "accepted"
	ledgerUncertain  = "uncertain"
	ledgerRejected   = "rejected"

	ledgerLeaseMicros = int64(120 * 1_000_000) // 120s submit lease
)

// Slice send ledger errors (mapped by the handler to the SAME i18n codes
// the PG path uses, so the wire contract is identical).
var (
	// ErrLedgerConflict rejects reuse of a request key with different content.
	ErrLedgerConflict = errors.New("send request identity conflict")
	// ErrLedgerDraftLocked prevents another submission of an already submitted source.
	ErrLedgerDraftLocked = errors.New("draft already submitted")
)

// SQLiteSendLedger is the slice send ledger store. It carries the same
// row type as the PG engine (store.MailboxSendReceipts) so the receipt
// JSON contract is byte-identical between engines.
type SQLiteSendLedger struct {
	DB *sql.DB
}

// auditSendEvent appends a platform audit row for a terminal send state.
func (s *SQLiteSendLedger) auditSendEvent(ctx context.Context, tx *sql.Tx, row store.MailboxSendReceipts, action string) {
	detail, _ := json.Marshal(map[string]any{
		"actor_type": "mailbox", "mailbox_id": row.MailboxID,
		"request_key": row.RequestKey, "outcome": action,
	})
	_, _ = tx.ExecContext(ctx,
		`INSERT INTO audit_logs("tenant_id","action","target_type","target_id","detail") VALUES(?1,?2,?3,?4,?5)`,
		row.TenantID, action, "mailbox", fmt.Sprint(row.MailboxID), string(detail))
}

// recoverStale lazily resolves expired `submitting` claims to `uncertain`
// (the slice runtime has no background worker; reads drive the recovery).
func (s *SQLiteSendLedger) recoverStale(ctx context.Context, tx *sql.Tx, nowMicros int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET status='uncertain', last_error='MAILBOX_SEND_UNCERTAIN', updated_at=?1
		 WHERE status='submitting' AND lease_until > 0 AND lease_until < ?1`, nowMicros)
	return err
}

// PrepareSend registers a send intention exactly once per (tenant, mailbox,
// request key). Same key + same digest returns the prior row with created=false;
// same key + different digest is a conflict; a source draft already holding a
// live submission is locked. The raw message and SMTP credential arrive sealed.
func (s *SQLiteSendLedger) PrepareSend(ctx context.Context, arg store.PlatformCreateSendReceiptParams, digest string) (store.MailboxSendReceipts, bool, error) {
	now := timeutil.NowMicro()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return store.MailboxSendReceipts{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = s.recoverStale(ctx, tx, now); err != nil {
		return store.MailboxSendReceipts{}, false, err
	}
	row, err := s.getByKeyTx(ctx, tx, arg.TenantID, arg.MailboxID, arg.RequestKey)
	if err == nil {
		if row.RequestHash != digest {
			return row, false, ErrLedgerConflict
		}
		return row, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.MailboxSendReceipts{}, false, err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO mailbox_send_ledger
		   ("tenant_id","mailbox_id","request_key","request_hash","message_id",
		    "raw_secret","smtp_secret","status",
		    "draft_folder","draft_uid","draft_uidvalidity")
		 VALUES(?1,?2,?3,?4,?5,?6,?7,'created',?8,?9,?10)`,
		arg.TenantID, arg.MailboxID, arg.RequestKey, digest, arg.MessageID,
		arg.RawSecret, arg.SmtpSecret,
		arg.DraftFolder, arg.DraftUid, arg.DraftUidvalidity)
	if err != nil {
		// The partial unique index on the live draft identity rejects a
		// second live submission of the same source draft.
		if strings.Contains(err.Error(), "mailbox_send_ledger") && strings.Contains(err.Error(), "UNIQUE constraint failed") {
			// The owner-scoped request key and the live-draft identity are
			// the only uniqueness surfaces on this table; distinguish them.
			if strings.Contains(err.Error(), "draft_folder") {
				return store.MailboxSendReceipts{}, false, ErrLedgerDraftLocked
			}
			return store.MailboxSendReceipts{}, false, ErrLedgerConflict
		}
		return store.MailboxSendReceipts{}, false, err
	}
	row, err = s.getByKeyTx(ctx, tx, arg.TenantID, arg.MailboxID, arg.RequestKey)
	if err != nil {
		return store.MailboxSendReceipts{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return store.MailboxSendReceipts{}, false, err
	}
	return row, true, nil
}

// BeginSubmit claims the submit lease: created → submitting under a CAS so
// a concurrent replay cannot cross the SMTP boundary twice.
func (s *SQLiteSendLedger) BeginSubmit(ctx context.Context, tenant, mailbox, id int64) (store.MailboxSendReceipts, error) {
	tokenBytes := make([]byte, 8)
	if _, err := rand.Read(tokenBytes); err != nil {
		return store.MailboxSendReceipts{}, err
	}
	now := timeutil.NowMicro()
	token := time.Now().UnixNano() ^ int64(hex.EncodeToString(tokenBytes)[0])<<48
	res, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET status='submitting', claim_token=?1, lease_until=?2, updated_at=?1
		 WHERE id=?3 AND tenant_id=?4 AND mailbox_id=?5 AND status='created'`,
		token, now+ledgerLeaseMicros, id, tenant, mailbox)
	if err != nil {
		return store.MailboxSendReceipts{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return store.MailboxSendReceipts{}, fmt.Errorf("send claim lost")
	}
	return s.Get(ctx, tenant, mailbox, id)
}

// FinishSubmit closes the lease: submitting → final state, CAS on the claim
// token. A failed finish leaves the lease to expire; the next read resolves
// the row to `uncertain` (never an automatic retry).
func (s *SQLiteSendLedger) FinishSubmit(ctx context.Context, tenant, mailbox, id, claimToken int64, status, code string) (store.MailboxSendReceipts, error) {
	now := timeutil.NowMicro()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return store.MailboxSendReceipts{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET status=?2, last_error=?3, accepted_at=?4, updated_at=?1,
		 claim_token=0, lease_until=0
		 WHERE id=?5 AND tenant_id=?6 AND mailbox_id=?7 AND status='submitting' AND claim_token=?8`,
		now, status, code, boolMicros(status == ledgerAccepted, now), id, tenant, mailbox, claimToken)
	if err != nil {
		return store.MailboxSendReceipts{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return store.MailboxSendReceipts{}, fmt.Errorf("send finish claim mismatch")
	}
	row, err := s.getTx(ctx, tx, tenant, mailbox, id)
	if err != nil {
		return store.MailboxSendReceipts{}, err
	}
	if status == ledgerAccepted || status == ledgerUncertain || status == ledgerRejected {
		s.auditSendEvent(ctx, tx, row, "mailbox.send."+status)
	}
	if err = tx.Commit(); err != nil {
		return store.MailboxSendReceipts{}, err
	}
	return row, nil
}

func boolMicros(cond bool, micros int64) int64 {
	if cond {
		return micros
	}
	return 0
}

// Get returns one receipt, recovering a stale lease first.
func (s *SQLiteSendLedger) Get(ctx context.Context, tenant, mailbox, id int64) (store.MailboxSendReceipts, error) {
	now := timeutil.NowMicro()
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET status='uncertain', last_error='MAILBOX_SEND_UNCERTAIN', updated_at=?1
		 WHERE tenant_id=?2 AND mailbox_id=?3 AND id=?4 AND status='submitting' AND lease_until > 0 AND lease_until < ?1`,
		now, tenant, mailbox, id); err != nil {
		return store.MailboxSendReceipts{}, err
	}
	return scanLedger(s.DB.QueryRowContext(ctx,
		`SELECT `+sendLedgerColumns+` FROM mailbox_send_ledger WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3`,
		tenant, mailbox, id))
}

// GetByKey returns the receipt for an idempotency key (sql.ErrNoRows when absent).
func (s *SQLiteSendLedger) GetByKey(ctx context.Context, tenant, mailbox int64, key string) (store.MailboxSendReceipts, error) {
	now := timeutil.NowMicro()
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET status='uncertain', last_error='MAILBOX_SEND_UNCERTAIN', updated_at=?1
		 WHERE tenant_id=?2 AND mailbox_id=?3 AND request_key=?4 AND status='submitting' AND lease_until > 0 AND lease_until < ?1`,
		now, tenant, mailbox, key); err != nil {
		return store.MailboxSendReceipts{}, err
	}
	return scanLedger(s.DB.QueryRowContext(ctx,
		`SELECT `+sendLedgerColumns+` FROM mailbox_send_ledger WHERE tenant_id=?1 AND mailbox_id=?2 AND request_key=?3`,
		tenant, mailbox, key))
}

// List returns the owner-scoped receipts, newest first.
func (s *SQLiteSendLedger) List(ctx context.Context, tenant, mailbox, limit, offset int64) ([]store.MailboxSendReceipts, error) {
	now := timeutil.NowMicro()
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET status='uncertain', last_error='MAILBOX_SEND_UNCERTAIN', updated_at=?1
		 WHERE tenant_id=?2 AND mailbox_id=?3 AND status='submitting' AND lease_until > 0 AND lease_until < ?1`,
		now, tenant, mailbox); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+sendLedgerColumns+` FROM mailbox_send_ledger WHERE tenant_id=?1 AND mailbox_id=?2
		 ORDER BY id DESC LIMIT ?3 OFFSET ?4`, tenant, mailbox, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanLedgerRows(rows)
}

const sendLedgerColumns = `"id","tenant_id","mailbox_id","request_key","request_hash","message_id","status",` +
	`"sent_copy_state","raw_secret","smtp_secret","smtp_password_version","draft_folder","draft_uid",` +
	`"draft_uidvalidity","draft_released","claim_token","lease_until","next_attempt_at","copy_attempts",` +
	`"copy_pending_since","accepted_at","last_error","created_at","updated_at"`

func (s *SQLiteSendLedger) getByKeyTx(ctx context.Context, tx *sql.Tx, tenant, mailbox int64, key string) (store.MailboxSendReceipts, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+sendLedgerColumns+` FROM mailbox_send_ledger WHERE tenant_id=?1 AND mailbox_id=?2 AND request_key=?3`,
		tenant, mailbox, key)
	return scanLedger(row)
}

func (s *SQLiteSendLedger) getTx(ctx context.Context, tx *sql.Tx, tenant, mailbox, id int64) (store.MailboxSendReceipts, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+sendLedgerColumns+` FROM mailbox_send_ledger WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3`,
		tenant, mailbox, id)
	return scanLedger(row)
}

// Sent-copy reconciliation operations (owner + endpoint driven on the slice
// runtime; the PG worker loop is not part of a pool-less process).

// DeferCopy marks the sent copy pending after any reconciliation failure.
func (s *SQLiteSendLedger) DeferCopy(ctx context.Context, tenant, mailbox, id, claimToken int64) error {
	now := timeutil.NowMicro()
	_, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET sent_copy_state='pending', copy_pending_since=?1, copy_attempts=copy_attempts+1, updated_at=?1
		 WHERE id=?2 AND tenant_id=?3 AND mailbox_id=?4 AND claim_token=?5`,
		now, id, tenant, mailbox, claimToken)
	return err
}

// CompleteCopy closes a reconciled sent copy under the claim token.
func (s *SQLiteSendLedger) CompleteCopy(ctx context.Context, tenant, mailbox, id, claimToken int64) error {
	now := timeutil.NowMicro()
	_, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET sent_copy_state='complete', copy_pending_since=0, draft_released=1, updated_at=?1
		 WHERE id=?2 AND tenant_id=?3 AND mailbox_id=?4 AND claim_token=?5`,
		now, id, tenant, mailbox, claimToken)
	return err
}

// MarkCopyUnknown records that the Sent folder no longer provably holds the
// exact bytes, before a fresh APPEND.
func (s *SQLiteSendLedger) MarkCopyUnknown(ctx context.Context, tenant, mailbox, id, claimToken int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET sent_copy_state='held', updated_at=?1
		 WHERE id=?2 AND tenant_id=?3 AND mailbox_id=?4 AND claim_token=?5`,
		timeutil.NowMicro(), id, tenant, mailbox, claimToken)
	return err
}

// ClaimOwnedCopy hands the owner the row for an authorized repair pass.
func (s *SQLiteSendLedger) ClaimOwnedCopy(ctx context.Context, tenant, mailbox, id int64) (store.MailboxSendReceipts, error) {
	now := timeutil.NowMicro()
	token := now ^ id<<8
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_send_ledger SET claim_token=?1, lease_until=?2, updated_at=?3
		 WHERE id=?4 AND tenant_id=?5 AND mailbox_id=?6`,
		token, now+ledgerLeaseMicros, now, id, tenant, mailbox); err != nil {
		return store.MailboxSendReceipts{}, err
	}
	return s.Get(ctx, tenant, mailbox, id)
}

// Draft journal: idempotent server-side draft saves.

// ClaimDraftSave inserts the save intent (unique per idempotency key); an
// existing done row with the same digest returns its stored result.
func (s *SQLiteSendLedger) ClaimDraftSave(ctx context.Context, tenant, mailbox int64, key, digest, messageID string) (status string, resultJSON string, err error) {
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO mailbox_draft_journal("tenant_id","mailbox_id","request_key","digest","message_id","status")
		 VALUES(?1,?2,?3,?4,?5,'claimed')`,
		tenant, mailbox, key, digest, messageID)
	if err != nil {
		if strings.Contains(err.Error(), "mailbox_draft_journal") && strings.Contains(err.Error(), "UNIQUE constraint failed") {
			var status, resultJSON, priorDigest string
			if err = s.DB.QueryRowContext(ctx,
				`SELECT "status","result_json","digest" FROM mailbox_draft_journal WHERE tenant_id=?1 AND mailbox_id=?2 AND request_key=?3`,
				tenant, mailbox, key).Scan(&status, &resultJSON, &priorDigest); err != nil {
				return "", "", err
			}
			if priorDigest != digest {
				return "", "", ErrLedgerConflict
			}
			return status, resultJSON, nil
		}
		return "", "", err
	}
	return "claimed", "", nil
}

// CompleteDraftSave closes a claimed save with its append result.
func (s *SQLiteSendLedger) CompleteDraftSave(ctx context.Context, tenant, mailbox int64, key, resultJSON string, folder string, uid, uidvalidity int64) (int64, error) {
	res, err := s.DB.ExecContext(ctx,
		`UPDATE mailbox_draft_journal SET status='done', result_json=?1, folder=?2, uid=?3, uidvalidity=?4
		 WHERE tenant_id=?5 AND mailbox_id=?6 AND request_key=?7 AND status='claimed'`,
		resultJSON, folder, uid, uidvalidity, tenant, mailbox, key)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// scanLedger keeps the ledger row Scan single-sourced across every read.
func scanLedger(row *sql.Row) (store.MailboxSendReceipts, error) {
	var i store.MailboxSendReceipts
	err := row.Scan(
		&i.ID, &i.TenantID, &i.MailboxID, &i.RequestKey, &i.RequestHash, &i.MessageID, &i.Status,
		&i.SentCopyState, &i.RawSecret, &i.SmtpSecret, &i.SmtpPasswordVersion, &i.DraftFolder, &i.DraftUid,
		&i.DraftUidvalidity, &i.DraftReleased, &i.ClaimToken, &i.LeaseUntil, &i.NextAttemptAt, &i.CopyAttempts,
		&i.CopyPendingSince, &i.AcceptedAt, &i.LastError, &i.CreatedAt, &i.UpdatedAt,
	)
	if err != nil {
		return store.MailboxSendReceipts{}, err
	}
	return i, nil
}

func scanLedgerRows(rows *sql.Rows) ([]store.MailboxSendReceipts, error) {
	items := []store.MailboxSendReceipts{}
	for rows.Next() {
		var i store.MailboxSendReceipts
		if err := rows.Scan(
			&i.ID, &i.TenantID, &i.MailboxID, &i.RequestKey, &i.RequestHash, &i.MessageID, &i.Status,
			&i.SentCopyState, &i.RawSecret, &i.SmtpSecret, &i.SmtpPasswordVersion, &i.DraftFolder, &i.DraftUid,
			&i.DraftUidvalidity, &i.DraftReleased, &i.ClaimToken, &i.LeaseUntil, &i.NextAttemptAt, &i.CopyAttempts,
			&i.CopyPendingSince, &i.AcceptedAt, &i.LastError, &i.CreatedAt, &i.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
