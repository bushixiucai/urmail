// Package mailboxschedule runs durable, fenced native-mailbox delivery tasks.
package mailboxschedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// SecretBox decrypts the credential sealed at schedule creation.
type SecretBox interface{ DecryptString(string) (string, error) }

// Store uses claim-token CAS for every transition after claiming.
type Store interface {
	PlatformRecoverStaleMailboxScheduledSends(context.Context) (int64, error)
	PlatformClaimMailboxScheduledSend(context.Context) (store.MailboxScheduledSends, error)
	PlatformGetMailboxScheduledSendAddress(context.Context, store.PlatformGetMailboxScheduledSendAddressParams) (string, error)
	PlatformBeginMailboxScheduledSendSubmission(context.Context, store.PlatformBeginMailboxScheduledSendSubmissionParams) (store.MailboxScheduledSends, error)
	PlatformFinishMailboxScheduledSend(context.Context, store.PlatformFinishMailboxScheduledSendParams) (store.MailboxScheduledSends, error)
	PlatformRequeueMailboxScheduledSend(context.Context, store.PlatformRequeueMailboxScheduledSendParams) (store.MailboxScheduledSends, error)
	CreateAuditLog(context.Context, store.CreateAuditLogParams) (store.AuditLogs, error)
}

// Options configures the process-local poller; durable state lives in PG.
type Options struct {
	Pool        *db.Pool
	Store       Store
	Secrets     SecretBox
	Vault       webmail.ScheduleSealer
	Dialer      webmail.Dialer
	Submitter   webmail.Submitter
	Interval    time.Duration
	TaskTimeout time.Duration
	Logger      *zap.Logger
	Now         func() time.Time
}

// Worker claims and submits due schedules without replaying ambiguous deliveries.
type Worker struct{ opts Options }

// NewWorker builds a poller using the configured platform store.
func NewWorker(opts Options) *Worker {
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.TaskTimeout <= 0 {
		opts.TaskTimeout = 90 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Store == nil && opts.Pool != nil {
		opts.Store = store.New(opts.Pool.Pool)
	}
	return &Worker{opts: opts}
}

func (w *Worker) enabled() bool {
	return w != nil && w.opts.Store != nil && w.opts.Secrets != nil && w.opts.Dialer != nil && w.opts.Submitter != nil
}

// Run polls until the caller cancels its context.
func (w *Worker) Run(ctx context.Context) {
	if !w.enabled() {
		return
	}
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	for {
		for {
			worked, err := w.Step(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				w.opts.Logger.Warn("scheduled send worker step", zap.Error(err))
			}
			if !worked || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Step never retries a submission whose acceptance is unknown. Recovery of a
// crashed submitting worker produces uncertain, not pending.
func (w *Worker) Step(ctx context.Context) (bool, error) {
	if !w.enabled() {
		return false, nil
	}
	if _, err := w.opts.Store.PlatformRecoverStaleMailboxScheduledSends(ctx); err != nil {
		return false, err
	}
	row, err := w.opts.Store.PlatformClaimMailboxScheduledSend(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	taskCtx, cancel := context.WithTimeout(ctx, w.opts.TaskTimeout)
	defer cancel()
	return true, w.fire(taskCtx, row)
}

func (w *Worker) fire(ctx context.Context, row store.MailboxScheduledSends) error {
	if row.DraftUid < 1 || row.DraftUid > math.MaxUint32 {
		return w.finish(ctx, row, "preparing", "failed", "DRAFT_CHANGED")
	}
	uid := uint32(row.DraftUid)
	password, err := w.opts.Secrets.DecryptString(row.SmtpSecret)
	if err != nil || strings.TrimSpace(password) == "" {
		return w.finish(ctx, row, "preparing", "failed", "SCHEDULE_CREDENTIAL_UNAVAILABLE")
	}
	address, err := w.opts.Store.PlatformGetMailboxScheduledSendAddress(ctx, store.PlatformGetMailboxScheduledSendAddressParams{TenantID: row.TenantID, MailboxID: row.MailboxID})
	if err != nil {
		return w.retry(ctx, row, "preparing", "SCHEDULE_MAILBOX_UNAVAILABLE")
	}
	client, err := w.opts.Dialer.Dial(ctx, address, password)
	if err != nil {
		return w.retry(ctx, row, "preparing", "SCHEDULE_IMAP_UNAVAILABLE")
	}
	defer func() { _ = client.Close() }()
	validity, err := client.FolderUIDValidity(ctx, row.DraftFolder)
	if err != nil {
		return w.retry(ctx, row, "preparing", "SCHEDULE_IMAP_UNAVAILABLE")
	}
	if validity == 0 || int64(validity) != row.DraftUidvalidity {
		return w.finish(ctx, row, "preparing", "failed", "DRAFT_CHANGED")
	}
	outgoing, err := webmail.OutgoingFromDraft(ctx, client, row.DraftFolder, uid)
	if err != nil {
		return w.retry(ctx, row, "preparing", "SCHEDULE_DRAFT_UNAVAILABLE")
	}
	outgoing.From = address
	// A stable identifier supports reconciliation; SMTP recipients need not deduplicate it.
	outgoing.MessageID = fmt.Sprintf("<urmail-schedule-%d@localhost>", row.ID)
	// This durable CAS is the cancellation boundary. No SMTP call is allowed
	// unless this exact claim still owns a live preparing row.
	if _, err = w.opts.Store.PlatformBeginMailboxScheduledSendSubmission(ctx, store.PlatformBeginMailboxScheduledSendSubmissionParams{ID: row.ID, ClaimToken: row.ClaimToken}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	err = w.opts.Submitter.Send(ctx, address, password, outgoing)
	if err != nil {
		if errors.Is(err, webmail.ErrSubmitUncertain) || !errors.Is(err, webmail.ErrSubmitFailed) {
			return w.finish(ctx, row, "submitting", "uncertain", "SCHEDULE_DELIVERY_UNCERTAIN")
		}
		return w.retry(ctx, row, "submitting", "SCHEDULE_SMTP_REJECTED")
	}
	// Persist acceptance before deleting the source. A DB error here leaves
	// submitting for safe uncertain recovery and never reaches retry().
	if err = w.finish(ctx, row, "submitting", "sent", ""); err != nil {
		return err
	}
	validity, err = client.FolderUIDValidity(ctx, row.DraftFolder)
	if err != nil || int64(validity) != row.DraftUidvalidity {
		w.opts.Logger.Warn("scheduled send accepted; source mailbox identity changed", zap.Int64("id", row.ID))
		return nil
	}
	if err = client.DeleteMessages(ctx, row.DraftFolder, []uint32{uid}, true); err != nil {
		w.opts.Logger.Warn("scheduled send accepted; draft cleanup pending", zap.Int64("id", row.ID))
	}
	return nil
}

func (w *Worker) finish(ctx context.Context, row store.MailboxScheduledSends, expected, status, code string) error {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := w.opts.Store.PlatformFinishMailboxScheduledSend(finalCtx, store.PlatformFinishMailboxScheduledSendParams{
		ID: row.ID, ClaimToken: row.ClaimToken, ExpectedStatus: expected, FinalStatus: status, LastError: pgtype.Text{String: code, Valid: code != ""},
	})
	if err != nil {
		return err
	}
	if w.opts.Vault != nil {
		w.opts.Vault.DeleteSchedule(row.ID)
	}
	w.audit(finalCtx, row, "mailbox.scheduled_send."+status, code)
	return nil
}

func (w *Worker) retry(ctx context.Context, row store.MailboxScheduledSends, expected, code string) error {
	if ShouldGiveUp(int(row.Attempts)) {
		return w.finish(ctx, row, expected, "failed", code)
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := w.opts.Store.PlatformRequeueMailboxScheduledSend(finalCtx, store.PlatformRequeueMailboxScheduledSendParams{
		ID: row.ID, ClaimToken: row.ClaimToken, ExpectedStatus: expected,
		NextAttemptAt: pgtype.Timestamptz{Time: NextAttempt(int(row.Attempts), w.opts.Now()), Valid: true}, LastError: pgtype.Text{String: code, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil {
		w.audit(finalCtx, row, "mailbox.scheduled_send.retry", code)
	}
	return err
}

func (w *Worker) audit(ctx context.Context, row store.MailboxScheduledSends, action, code string) {
	raw, _ := json.Marshal(map[string]any{"actor_type": "system", "mailbox_id": row.MailboxID, "schedule_id": row.ID, "result_code": code})
	_, err := w.opts.Store.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: row.TenantID, Valid: true}, Action: action, TargetType: pgtype.Text{String: "mailbox_scheduled_send", Valid: true}, TargetID: pgtype.Text{String: fmt.Sprintf("%d", row.ID), Valid: true}, Detail: raw})
	if err != nil {
		w.opts.Logger.Warn("scheduled send audit", zap.String("action", action))
	}
}
