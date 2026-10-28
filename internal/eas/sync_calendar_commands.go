package eas

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

const syncStatusConflict = "7"

func (b *SyncBackend) applyCalendarCommand(ctx context.Context, req *SyncRequest, collection string, calendarID int64, cmd *Node, items map[string]itemRow) (string, string) {
	idText := cmd.ChildText(tokServerID)
	if cmd.Token != tokAdd && cmd.Token != tokChange && cmd.Token != tokDelete {
		return idText, syncStatusProtocolError
	}
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return idText, syncStatusServerError
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	svc := appcollab.Service{Q: q}
	principal := appcollab.Principal{TenantID: req.Session.TenantID, MailboxID: req.Session.MailboxID}
	if cmd.Token == tokAdd {
		data := cmd.Child(tokApplicationData)
		event, err := parseCalendarData(data, collab.Event{})
		if err != nil {
			return "", syncStatusProtocolError
		}
		canonical, _ := json.Marshal(event)
		result, err := collabAddReceipt(ctx, q, req, collection, "eas_calendar_add", cmd.ChildText(tokClientID), canonical, func() (collabAddResult, error) {
			row, err := svc.CreateEvent(ctx, principal, calendarID, event.UID, event)
			if err != nil {
				return collabAddResult{}, err
			}
			return collabAddResult{ServerID: baseEventID(row.ID), Version: row.Version}, nil
		})
		if errors.Is(err, errCollabClientIDConflict) {
			return "", syncStatusConflict
		}
		if err != nil {
			return "", syncStatusServerError
		}
		if tx.Commit(ctx) != nil {
			return "", syncStatusServerError
		}
		// Do not replace the existing snapshot here: the change log sends the
		// authoritative row on this or the next round, including a replayed Add.
		return result.ServerID, syncStatusSuccess
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 || strings.Contains(idText, "#") {
		return idText, syncStatusProtocolError
	}
	known, ok := items[idText]
	if !ok {
		return idText, syncStatusObjectDeleted
	}
	expected, err := strconv.ParseInt(strings.TrimPrefix(known.Fingerprint, "v"), 10, 64)
	if err != nil {
		return idText, syncStatusConflict
	}
	row, err := q.PlatformGetEvent(ctx, store.PlatformGetEventParams{TenantID: principal.TenantID, MailboxID: principal.MailboxID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return idText, syncStatusObjectDeleted
	}
	if err != nil {
		return idText, syncStatusServerError
	}
	if row.CalendarID != calendarID {
		return idText, syncStatusObjectDeleted
	}
	if row.Version != expected {
		return idText, syncStatusConflict
	}
	if cmd.Token == tokDelete {
		_, err = q.PlatformDeleteEvent(ctx, store.PlatformDeleteEventParams{TenantID: principal.TenantID, MailboxID: principal.MailboxID, ID: id, Version: expected})
	} else {
		event, _, decodeErr := appcollab.EventFromRow(row)
		if decodeErr != nil {
			return idText, syncStatusServerError
		}
		next, parseErr := parseCalendarData(cmd.Child(tokApplicationData), event)
		if parseErr != nil {
			return idText, syncStatusProtocolError
		}
		// UID identifies the series and cannot be replaced by a partial Change.
		if next.UID != event.UID {
			return idText, syncStatusProtocolError
		}
		_, err = svc.UpdateEvent(ctx, principal, id, expected, next)
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, appcollab.ErrConflict) {
		return idText, syncStatusConflict
	}
	if err != nil {
		return idText, syncStatusServerError
	}
	if tx.Commit(ctx) != nil {
		return idText, syncStatusServerError
	}
	return idText, syncStatusSuccess
}
