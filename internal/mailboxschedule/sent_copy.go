package mailboxschedule

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// SentCopyStore fences repair independently of SMTP submission.
type SentCopyStore interface {
	PlatformMarkScheduledSentCopyUnknown(context.Context, store.PlatformMarkScheduledSentCopyUnknownParams) (store.MailboxScheduledSends, error)
	PlatformCompleteScheduledSentCopy(context.Context, store.PlatformCompleteScheduledSentCopyParams) (store.MailboxScheduledSends, error)
	PlatformDeferScheduledSentCopy(context.Context, store.PlatformDeferScheduledSentCopyParams) error
}

func deferSentCopy(ctx context.Context, q SentCopyStore, row store.MailboxScheduledSends, cause error) error {
	final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := q.PlatformDeferScheduledSentCopy(final, store.PlatformDeferScheduledSentCopyParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: row.ClaimToken})
	return errors.Join(webmail.ErrSentCopyPending, cause, err)
}

// RepairSentCopy only reconciles/APPENDs Sent, and can never submit SMTP.
// Callers must own the bounded copy lease and close their IMAP connection.
func RepairSentCopy(ctx context.Context, q SentCopyStore, row store.MailboxScheduledSends, client webmail.Client) error {
	finder, ok := client.(webmail.SentCopyFinder)
	if !ok || len(row.SentRaw) == 0 {
		return deferSentCopy(ctx, q, row, errors.New("sent reconciliation unavailable"))
	}
	_, found, err := finder.FindSentCopy(ctx, row.SentRaw)
	if err != nil {
		return deferSentCopy(ctx, q, row, err)
	}
	if !found {
		if _, err = q.PlatformMarkScheduledSentCopyUnknown(ctx, store.PlatformMarkScheduledSentCopyUnknownParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: row.ClaimToken}); err != nil {
			return deferSentCopy(ctx, q, row, err)
		}
		if _, err = client.SaveSentCopy(ctx, row.SentRaw); err != nil {
			return deferSentCopy(ctx, q, row, err)
		}
	}
	final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err = q.PlatformCompleteScheduledSentCopy(final, store.PlatformCompleteScheduledSentCopyParams{TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID, ClaimToken: row.ClaimToken}); err != nil {
		return deferSentCopy(ctx, q, row, err)
	}
	// Original drafts survive pending/unknown copies. Cleanup is bound to the old
	// UIDVALIDITY; it cannot delete a different mailbox generation's message.
	if row.DraftUid > 0 && row.DraftUid <= math.MaxUint32 {
		validity, e := client.FolderUIDValidity(ctx, row.DraftFolder)
		if e == nil && validity > 0 && int64(validity) == row.DraftUidvalidity {
			_ = client.DeleteMessages(ctx, row.DraftFolder, []uint32{uint32(row.DraftUid)}, true)
		}
	}
	return nil
}
