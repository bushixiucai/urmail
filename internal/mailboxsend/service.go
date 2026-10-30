// Package mailboxsend records immediate SMTP intentions and repairs Sent copies.
package mailboxsend

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// ErrConflict rejects reuse of a request key with different content.
var ErrConflict = errors.New("send request identity conflict")

// ErrDraftLocked prevents another submission of an already submitted source.
var ErrDraftLocked = errors.New("draft already submitted")

// SecretBox seals temporary raw messages and credentials using the master key.
type SecretBox interface {
	EncryptString(string) (string, error)
	DecryptString(string) (string, error)
}

// DraftRef binds source cleanup to one authoritative mailbox generation.
type DraftRef struct {
	Folder        string
	UID, Validity uint32
}

// Service persists send intentions and performs IMAP-only copy recovery.
type Service struct {
	ValidateDraft func(context.Context, DraftRef) error
	Pool          *db.Pool
	Secrets       SecretBox
	Dialer        webmail.Dialer
	Submitter     webmail.Submitter
}

// Seal refuses nil or plaintext-fallback boxes. Recovery material must be encrypted.
func Seal(box interface{ EncryptString(string) (string, error) }, plain string) (string, error) {
	if box == nil {
		return "", errors.New("send recovery encryption unavailable")
	}
	sealed, err := box.EncryptString(plain)
	if err != nil || !strings.HasPrefix(sealed, "enc:v1:") {
		return "", errors.New("send recovery encryption unavailable")
	}
	return sealed, nil
}

// LockDraft serializes autosave, discard and submission for one source identity.
func LockDraft(ctx context.Context, q *store.Queries, tenant, mailbox int64, ref DraftRef) error {
	return q.PlatformLockSendDraft(ctx, store.PlatformLockSendDraftParams{TenantID: strconv.FormatInt(tenant, 10), MailboxID: strconv.FormatInt(mailbox, 10), Folder: ref.Folder, Uid: strconv.FormatUint(uint64(ref.UID), 10), Validity: strconv.FormatUint(uint64(ref.Validity), 10)})
}

// Prepare commits immutable bytes before SMTP. Only a newly created row may submit.
func (s Service) Prepare(ctx context.Context, tenant, mailbox int64, key, digest, password string, out webmail.Outgoing, ref DraftRef, ack int64) (store.MailboxSendReceipts, bool, error) {
	if s.Pool == nil || s.Secrets == nil {
		return store.MailboxSendReceipts{}, false, errors.New("send storage unavailable")
	}
	sealed, err := Seal(s.Secrets, string(out.Raw))
	if err != nil {
		return store.MailboxSendReceipts{}, false, err
	}
	credential, err := Seal(s.Secrets, password)
	if err != nil {
		return store.MailboxSendReceipts{}, false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return store.MailboxSendReceipts{}, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	// Every creation for this mailbox/key is serialized, including messages without drafts.
	if err = LockDraft(ctx, q, tenant, mailbox, DraftRef{Folder: "key:" + key}); err != nil {
		return store.MailboxSendReceipts{}, false, err
	}
	existing, err := q.PlatformGetSendReceiptByKey(ctx, store.PlatformGetSendReceiptByKeyParams{TenantID: tenant, MailboxID: mailbox, RequestKey: key})
	if err == nil {
		if existing.RequestHash != digest {
			return existing, false, ErrConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return existing, false, err
	}
	if ref.UID > 0 {
		if err = LockDraft(ctx, q, tenant, mailbox, ref); err != nil {
			return existing, false, err
		}
		if ack > 0 {
			n, e := q.PlatformAcknowledgeUncertainSend(ctx, store.PlatformAcknowledgeUncertainSendParams{TenantID: tenant, MailboxID: mailbox, ID: ack, DraftFolder: ref.Folder, DraftUid: int64(ref.UID), DraftUidvalidity: int64(ref.Validity)})
			if e != nil {
				return existing, false, e
			}
			if n != 1 {
				return existing, false, ErrDraftLocked
			}
		}
		locked, e := q.PlatformDraftSendLocked(ctx, store.PlatformDraftSendLockedParams{TenantID: tenant, MailboxID: mailbox, DraftFolder: ref.Folder, DraftUid: int64(ref.UID), DraftUidvalidity: int64(ref.Validity)})
		if e != nil {
			return existing, false, e
		}
		if locked.Bool {
			return existing, false, ErrDraftLocked
		}
		if s.ValidateDraft != nil {
			if e := s.ValidateDraft(ctx, ref); e != nil {
				return existing, false, e
			}
		}
	}
	row, err := q.PlatformCreateSendReceipt(ctx, store.PlatformCreateSendReceiptParams{TenantID: tenant, MailboxID: mailbox, RequestKey: key, RequestHash: digest, MessageID: out.MessageID, RawSecret: sealed, SmtpSecret: credential, DraftFolder: ref.Folder, DraftUid: int64(ref.UID), DraftUidvalidity: int64(ref.Validity)})
	if err != nil {
		return row, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return row, false, err
	}
	return row, true, nil
}

// Submit crosses the SMTP boundary exactly once; failed persistence is never a retry.
func (s Service) Submit(ctx context.Context, row store.MailboxSendReceipts, address, password string, out webmail.Outgoing) (store.MailboxSendReceipts, error) {
	q := store.New(s.Pool.Pool)
	claim, err := q.PlatformBeginSendReceipt(ctx, store.PlatformBeginSendReceiptParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID})
	if err != nil {
		return row, err
	}
	// An HTTP disconnect cannot interrupt the accepted-state write or create an automatic retry.
	attempt, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	_, sendErr := s.Submitter.Send(attempt, address, password, out)
	status, code := "accepted", ""
	if sendErr != nil {
		status = "uncertain"
		code = "MAILBOX_SEND_UNCERTAIN"
		if errors.Is(sendErr, webmail.ErrSubmitFailed) {
			status = "rejected"
			code = "SMTP_UNAVAILABLE"
		}
	}
	final, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	completed, err := q.PlatformFinishSendReceipt(final, store.PlatformFinishSendReceiptParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: claim.ClaimToken, FinalStatus: status, LastError: code})
	if err != nil {
		// The committed submitting row will be recovered to uncertain after its lease.
		claim.Status = "uncertain"
		claim.LastError = "MAILBOX_SEND_UNCERTAIN"
		return claim, nil
	}
	return completed, nil
}

// Repair is strictly IMAP-only, always reconciling exact bytes before APPEND.
func (s Service) Repair(ctx context.Context, row store.MailboxSendReceipts, client webmail.Client, guards ...func(context.Context) error) error {
	q := store.New(s.Pool.Pool)
	deferCopy := func(cause error) error {
		final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, e := q.PlatformDeferSendCopy(final, store.PlatformDeferSendCopyParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: row.ClaimToken})
		return errors.Join(webmail.ErrSentCopyPending, cause, e)
	}
	if s.Secrets == nil || !strings.HasPrefix(row.RawSecret, "enc:v1:") {
		return deferCopy(errors.New("copy recovery unavailable"))
	}
	raw, err := s.Secrets.DecryptString(row.RawSecret)
	if err != nil {
		return deferCopy(err)
	}
	finder, ok := client.(webmail.SentCopyFinder)
	if !ok {
		return deferCopy(errors.New("copy reconciliation unavailable"))
	}
	authorize := func() error {
		for _, guard := range guards {
			if guard != nil {
				if err := guard(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err = authorize(); err != nil {
		return deferCopy(err)
	}
	_, found, err := finder.FindSentCopy(ctx, []byte(raw))
	if err != nil {
		return deferCopy(err)
	}
	if err = authorize(); err != nil {
		return deferCopy(err)
	}
	if !found {
		if _, err = q.PlatformMarkSendCopyUnknown(ctx, store.PlatformMarkSendCopyUnknownParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: row.ClaimToken}); err != nil {
			return deferCopy(err)
		}
		if _, err = client.SaveSentCopy(ctx, []byte(raw)); err != nil {
			return deferCopy(err)
		}
	}
	if err = authorize(); err != nil {
		return deferCopy(err)
	}
	final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err = q.PlatformCompleteSendCopy(final, store.PlatformCompleteSendCopyParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: row.ClaimToken}); err != nil {
		return deferCopy(err)
	}
	// Cleanup can only address the exact source generation, after a confirmed copy.
	if row.DraftUid > 0 && row.DraftUid <= math.MaxUint32 {
		validity, e := client.FolderUIDValidity(final, row.DraftFolder)
		if e == nil && int64(validity) == row.DraftUidvalidity {
			_ = client.DeleteMessages(final, row.DraftFolder, []uint32{uint32(row.DraftUid)}, true)
		}
	}
	return nil
}

// StepCopy never submits SMTP. Stale submitting intentions become uncertain.
func (s Service) StepCopy(ctx context.Context) (bool, error) {
	if s.Pool == nil || s.Secrets == nil || s.Dialer == nil {
		return false, nil
	}
	q := store.New(s.Pool.Pool)
	if _, err := q.PlatformRecoverSendReceipts(ctx); err != nil {
		return false, err
	}
	if _, err := q.PlatformHoldUnauthorizedSendCopies(ctx); err != nil {
		return false, err
	}
	if _, err := q.PlatformHoldExpiredSendCopies(ctx); err != nil {
		return false, err
	}
	row, err := q.PlatformClaimSendCopy(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	deferCopy := func(cause error) (bool, error) {
		_, e := q.PlatformDeferSendCopy(ctx, store.PlatformDeferSendCopyParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: row.ClaimToken})
		return true, errors.Join(cause, e)
	}
	password, err := s.Secrets.DecryptString(row.SmtpSecret)
	if err != nil || password == "" {
		return deferCopy(errors.New("copy credential unavailable"))
	}
	address, err := q.PlatformGetMailboxScheduledSendAddress(ctx, store.PlatformGetMailboxScheduledSendAddressParams{TenantID: row.TenantID, MailboxID: row.MailboxID})
	if err != nil {
		return deferCopy(err)
	}
	copyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client, err := s.Dialer.Dial(copyCtx, address, password)
	if err != nil {
		return deferCopy(err)
	}
	defer func() { _ = client.Close() }()
	guard := func(ctx context.Context) error {
		_, e := q.PlatformAuthorizeSendCopy(ctx, store.PlatformAuthorizeSendCopyParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: row.ClaimToken})
		if e != nil {
			_, _ = q.PlatformHoldUnauthorizedSendCopies(ctx)
		}
		return e
	}
	return true, s.Repair(copyCtx, row, client, guard)
}

// ClaimOwner acquires a bounded recovery lease for an already authenticated owner.
func (s Service) ClaimOwner(ctx context.Context, tenant, mailbox, id int64) (store.MailboxSendReceipts, error) {
	if s.Pool == nil {
		return store.MailboxSendReceipts{}, fmt.Errorf("send store unavailable")
	}
	return store.New(s.Pool.Pool).PlatformClaimOwnedSendCopy(ctx, store.PlatformClaimOwnedSendCopyParams{TenantID: tenant, MailboxID: mailbox, ID: id})
}
