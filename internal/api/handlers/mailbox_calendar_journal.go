package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

var errInvitationAlreadySent = errors.New("invitation already completed")
var errInvitationUncertain = errors.New("invitation outcome uncertain")

func invitationClaimError(c *gin.Context, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, errInvitationAlreadySent):
		authError(c, 409, "INVITATION_ALREADY_SENT")
	case errors.Is(err, errInvitationUncertain):
		authError(c, 409, "MAILBOX_SEND_UNCERTAIN")
	case errors.Is(err, appcollab.ErrConflict), errors.Is(err, pgx.ErrNoRows):
		authError(c, 409, "EVENT_CONFLICT")
	default:
		authError(c, 503, "CALENDAR_UNAVAILABLE")
	}
	return false
}
func finishCalendarInvitation(finish func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return finish(ctx)
}

func (h MailboxCalendarHandler) claimCalendarInvitation(ctx context.Context, p appcollab.Principal, row store.MailboxEvents, method string, cancelEvent bool) (func(context.Context) error, error) {
	if h.invitationJournal != nil {
		return h.invitationJournal(ctx, p, row, method, cancelEvent)
	}
	if h.Pool == nil {
		return nil, errors.New("invitation persistence unavailable")
	}
	tx, err := h.Pool.MailboxTx(ctx, p.TenantID, p.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	current, err := q.PlatformLockCalendarInvitationEvent(ctx, store.PlatformLockCalendarInvitationEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: row.ID})
	if err != nil {
		return nil, err
	}
	if current.Version != row.Version {
		return nil, appcollab.ErrConflict
	}
	hash := sha256.Sum256(fmt.Appendf(nil, "%d|%s|%d|%s", row.ID, row.Uid, row.Version, method))
	key := hex.EncodeToString(hash[:])
	prior, err := q.PlatformGetDataImportReceipt(ctx, store.PlatformGetDataImportReceiptParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Kind: "calendar_invitation", IdempotencyKey: key})
	if err == nil {
		if prior.Status == "done" {
			return nil, errInvitationAlreadySent
		}
		return nil, errInvitationUncertain
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if cancelEvent {
		if _, err = q.PlatformDeleteEvent(ctx, store.PlatformDeleteEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: row.ID, Version: row.Version}); err != nil {
			return nil, err
		}
	}
	if n, e := q.PlatformBindOwnedCalendarOrganizer(ctx, store.PlatformBindOwnedCalendarOrganizerParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: row.ID}); e != nil {
		return nil, e
	} else if n != 1 {
		return nil, errors.New("calendar organizer differs from mailbox")
	}
	payload, err := json.Marshal(map[string]any{"event_id": row.ID, "uid": row.Uid, "version": row.Version, "sequence": row.Sequence, "method": method, "cancelled": cancelEvent, "attendees": json.RawMessage(row.Attendees)}) //nolint:misspell // RFC 5545 value
	if err != nil {
		return nil, err
	}
	if err = q.PlatformClaimCalendarInvitation(ctx, store.PlatformClaimCalendarInvitationParams{TenantID: p.TenantID, MailboxID: p.MailboxID, IdempotencyKey: key, Payload: payload}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		tx, err := h.Pool.MailboxTx(ctx, p.TenantID, p.MailboxID)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		n, err := store.New(tx).PlatformFinishCalendarInvitation(ctx, store.PlatformFinishCalendarInvitationParams{TenantID: p.TenantID, MailboxID: p.MailboxID, IdempotencyKey: key})
		if err != nil {
			return err
		}
		if n != 1 {
			return errInvitationUncertain
		}
		return tx.Commit(ctx)
	}, nil
}
