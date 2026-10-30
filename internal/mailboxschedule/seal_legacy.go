package mailboxschedule

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxsend"
	"github.com/bushixiucai/urmail/internal/store"
)

// SealLegacyCopies encrypts upgrade-era payloads without IMAP or SMTP effects.
// It is safe during quarantine and preserves held state and the original deadline.
func SealLegacyCopies(ctx context.Context, pool *db.Pool, box mailboxsend.SecretBox) error {
	if pool == nil || box == nil {
		return errors.New("copy recovery encryption unavailable")
	}
	for {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		q := store.New(tx)
		row, err := q.PlatformGetLegacyScheduledCopy(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return nil
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		secret, err := mailboxsend.Seal(box, string(row.SentRaw))
		if err == nil {
			err = q.PlatformSealLegacyScheduledCopy(ctx, store.PlatformSealLegacyScheduledCopyParams{SentRawSecret: secret, TenantID: row.TenantID, MailboxID: row.MailboxID, ID: row.ID})
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
}
