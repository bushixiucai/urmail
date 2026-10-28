package eas

// Calendar class sync (MS-ASCAL) over the collab change log. Recurring events
// whose RRULE fits the implemented subset (FREQ + INTERVAL/COUNT/UNTIL plus a
// single stored EXDATE) sync as one item with a Recurrence element. Anything
// else falls back to the documented expansion strategy: the series is synced
// as individual occurrences inside the fixed window (90 days back, 1 year
// forward), each occurrence its own ServerId "<id>#<unix>" with the series
// UID preserved.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// storedEvent is one mailbox_events row projected for sync.
type storedEvent struct {
	ID         int64
	CalendarID int64
	Version    int64
	UID        string
	Event      collab.Event
}

// loadEvent reads one live event inside the session scope.
func loadEvent(ctx context.Context, b *SyncBackend, req *SyncRequest, id int64) (storedEvent, error) {
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return storedEvent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := store.New(tx).PlatformGetEvent(ctx, store.PlatformGetEventParams{TenantID: req.Session.TenantID, MailboxID: req.Session.MailboxID, ID: id})
	if err != nil {
		return storedEvent{}, err
	}
	event, _, err := appcollab.EventFromRow(row)
	if err != nil {
		return storedEvent{}, err
	}
	return storedEvent{ID: row.ID, CalendarID: row.CalendarID, Version: row.Version, UID: row.Uid, Event: event}, nil
}

// listCalendarEvents reads live events of one calendar, bounded.
func listCalendarEvents(ctx context.Context, b *SyncBackend, req *SyncRequest, calendarID int64) ([]storedEvent, error) {
	tx, err := b.Pool.MailboxTx(ctx, req.Session.TenantID, req.Session.MailboxID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := store.New(tx).PlatformListCalendarSyncEvents(ctx, store.PlatformListCalendarSyncEventsParams{TenantID: req.Session.TenantID, MailboxID: req.Session.MailboxID, CalendarID: calendarID, PageLimit: MaxItemsPerRequest + 1})
	if err != nil {
		return nil, err
	}
	out := make([]storedEvent, 0, len(rows))
	for _, row := range rows {
		event, _, err := appcollab.EventFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, storedEvent{ID: row.ID, CalendarID: row.CalendarID, Version: row.Version, UID: row.Uid, Event: event})
	}
	return out, nil
}

// syncCalendarCollection runs one Sync round for a "cal:<calendarID>" collection.
func (b *SyncBackend) syncCalendarCollection(ctx context.Context, req *SyncRequest, p parsedCollection, budget *syncBudget) collectionResult {
	globalBudget := budget
	window := b.MaxItems
	if window <= 0 {
		window = DefaultMaxItems
	}
	if p.windowSize > 0 && p.windowSize < window {
		window = p.windowSize
	}
	if window > budget.itemsLeft {
		window = budget.itemsLeft
	}
	localBudget := syncBudget{itemsLeft: window, bytesLeft: budget.bytesLeft}
	budget = &localBudget
	defer func() {
		globalBudget.itemsLeft -= window - localBudget.itemsLeft
		globalBudget.bytesLeft = localBudget.bytesLeft
	}()
	calendarID, err := strconv.ParseInt(strings.TrimPrefix(p.collectionID, "cal:"), 10, 64)
	if err != nil || calendarID <= 0 {
		return collectionResult{status: syncStatusObjectDeleted}
	}
	state, items, fullSync, status, err := b.keyCheck(ctx, req, p.collectionID, p.syncKey)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	if status != syncStatusSuccess {
		return collectionResult{status: status}
	}

	ok, err := collabCollectionExists(ctx, b, req, "mailbox_calendars", calendarID)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	if !ok {
		return collectionResult{status: syncStatusObjectDeleted}
	}

	result := collectionResult{status: syncStatusSuccess}
	for _, cmd := range p.commands {
		serverID, status := b.applyCalendarCommand(ctx, req, p.collectionID, calendarID, cmd, items)
		result.responses = append(result.responses, responseEntry(cmd.Token, serverID, cmd.ChildText(tokClientID), status))
	}

	if fullSync {
		full := b.fullSyncCalendar(ctx, req, p, state, calendarID, budget)
		full.responses = result.responses
		return full
	}

	changes, err := listChangesFor(ctx, b.Pool, req.Session, state.ChangeSeq, "event", changeLogBatch)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	collapsed := collapseChanges(changes)
	cursor := state.ChangeSeq
	for _, ch := range collapsed {
		if !budget.itemsLeftOk() {
			result.moreAvailable = true
			break
		}
		if ch.Deleted {
			// Remove the series item and any expanded occurrence items.
			prefix := eventServerID(ch.ResourceID, 0)
			for id := range items {
				if id != baseEventID(ch.ResourceID) && !strings.HasPrefix(id, prefix) {
					continue
				}
				if !strings.HasPrefix(id, prefix) && id != baseEventID(ch.ResourceID) {
					continue
				}
				cmd := deleteCommand(id)
				if !budget.chargeCommand(cmd) {
					result.moreAvailable = true
					break
				}
				result.commands = append(result.commands, cmd)
				delete(items, id)
			}
			if result.moreAvailable {
				break
			}
			cursor = ch.ID
			continue
		}
		row, err := loadEvent(ctx, b, req, ch.ResourceID)
		if errorsIsNoRows(err) {
			cursor = ch.ID
			continue
		}
		if err != nil {
			return collectionResult{status: syncStatusServerError}
		}
		if row.CalendarID != calendarID {
			cursor = ch.ID
			continue
		}
		fp := "v" + strconv.FormatInt(row.Version, 10)
		baseID := baseEventID(row.ID)
		prev, known := items[baseID]
		if known && prev.Fingerprint == fp {
			cursor = ch.ID
			continue
		}
		// If the series was previously expanded, retire stale occurrences.
		retired := retireExpanded(row.ID, fp, items, &result, budget)
		if retired {
			result.moreAvailable = true
			break
		}
		rec, representable := parseRecurrence(row.Event.RRule)
		switch {
		case row.Event.RRule != "" && representable:
			app := calendarAppData(row.Event, time.Time{}, &rec, row.UID)
			cmd := addCommand(changeToken(items, baseID), baseID, app)
			if !budget.chargeCommand(cmd) {
				result.moreAvailable = true
				break
			}
			result.commands = append(result.commands, cmd)
			items[baseID] = itemRow{ItemID: baseID, Fingerprint: fp}
		case row.Event.RRule != "":
			// Unrepresentable recurrence: expand occurrences in the window.
			start, end := syncWindow(time.Now())
			occurrences, err := collab.ExpandInstances(row.Event, start, end)
			if err != nil {
				// Expansion refused the rule: fall back to a single item
				// carrying the first instance, still never a pseudo-success.
				occurrences = []time.Time{row.Event.StartsAt}
			}
			emitted := true
			for _, occ := range occurrences {
				id := eventOccurrenceID(row.ID, occ)
				app := calendarAppData(row.Event, occ, nil, row.UID)
				cmd := addCommand(changeToken(items, id), id, app)
				if !budget.chargeCommand(cmd) {
					result.moreAvailable = true
					emitted = false
					break
				}
				result.commands = append(result.commands, cmd)
				items[id] = itemRow{ItemID: id, Fingerprint: fp + "@" + occurrenceStamp(occ)}
			}
			if !emitted {
				break
			}
			delete(items, baseID)
		default:
			app := calendarAppData(row.Event, time.Time{}, nil, row.UID)
			cmd := addCommand(changeToken(items, baseID), baseID, app)
			if !budget.chargeCommand(cmd) {
				result.moreAvailable = true
				break
			}
			result.commands = append(result.commands, cmd)
			items[baseID] = itemRow{ItemID: baseID, Fingerprint: fp}
		}
		cursor = ch.ID
	}
	state.ChangeSeq = cursor
	if err := persistCollabState(ctx, b, req, p.collectionID, state, items); err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	result.syncKey = formatSyncKey(state.Epoch, state.ChangeSeq)
	return result
}

// fullSyncCalendar emits every live event of the calendar up to the ceilings.
func (b *SyncBackend) fullSyncCalendar(ctx context.Context, req *SyncRequest, p parsedCollection, state collectionState, calendarID int64, budget *syncBudget) collectionResult {
	rows, err := listCalendarEvents(ctx, b, req, calendarID)
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	maxChange, err := maxChangeIDFor(ctx, b, req, "event")
	if err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	result := collectionResult{status: syncStatusSuccess}
	if len(rows) > MaxItemsPerRequest {
		rows = rows[:MaxItemsPerRequest]
		result.moreAvailable = true
	}
	items := map[string]itemRow{}
	for _, row := range rows {
		fp := "v" + strconv.FormatInt(row.Version, 10)
		rec, representable := parseRecurrence(row.Event.RRule)
		if row.Event.RRule == "" || representable {
			var recPtr *recurrenceData
			if row.Event.RRule != "" {
				recPtr = &rec
			}
			baseID := baseEventID(row.ID)
			app := calendarAppData(row.Event, time.Time{}, recPtr, row.UID)
			cmd := addCommand(tokAdd, baseID, app)
			if !budget.chargeCommand(cmd) {
				result.moreAvailable = true
				break
			}
			result.commands = append(result.commands, cmd)
			items[baseID] = itemRow{ItemID: baseID, Fingerprint: fp}
			continue
		}
		// Expansion fallback within the full-sync window.
		start, end := syncWindow(time.Now())
		occurrences, err := collab.ExpandInstances(row.Event, start, end)
		if err != nil {
			occurrences = []time.Time{row.Event.StartsAt}
		}
		stopped := false
		for _, occ := range occurrences {
			id := eventOccurrenceID(row.ID, occ)
			app := calendarAppData(row.Event, occ, nil, row.UID)
			cmd := addCommand(tokAdd, id, app)
			if !budget.chargeCommand(cmd) {
				result.moreAvailable = true
				stopped = true
				break
			}
			result.commands = append(result.commands, cmd)
			items[id] = itemRow{ItemID: id, Fingerprint: fp + "@" + occurrenceStamp(occ)}
		}
		if stopped {
			break
		}
	}
	if result.moreAvailable {
		state.ChangeSeq = 0
	} else {
		state.ChangeSeq = maxChange
	}
	if err := persistCollabState(ctx, b, req, p.collectionID, state, items); err != nil {
		return collectionResult{status: syncStatusServerError}
	}
	result.syncKey = formatSyncKey(state.Epoch, state.ChangeSeq)
	return result
}

// baseEventID is the series ServerId for a non-expanded event.
func baseEventID(id int64) string { return strconv.FormatInt(id, 10) }

// eventServerID prefixes occurrence ids for prefix matching.
func eventServerID(id int64, _ int64) string { return baseEventID(id) + "#" }

// eventOccurrenceID builds the expanded-occurrence ServerId.
func eventOccurrenceID(id int64, occ time.Time) string {
	return baseEventID(id) + "#" + strconv.FormatInt(occ.Unix(), 10)
}

func occurrenceStamp(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func changeToken(items map[string]itemRow, id string) byte {
	if _, known := items[id]; known {
		return tokChange
	}
	return tokAdd
}

// retireExpanded deletes stored occurrence rows of a series whose version
// changed. Returns true when the budget stopped the retirement early.
func retireExpanded(eventID int64, newFP string, items map[string]itemRow, result *collectionResult, budget *syncBudget) bool {
	prefix := eventServerID(eventID, 0)
	for id, row := range items {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		version, occ, found := strings.Cut(row.Fingerprint, "@")
		if !found || version == newFP {
			continue // already at the new series version
		}
		cmd := deleteCommand(id)
		if !budget.chargeCommand(cmd) {
			return true
		}
		result.commands = append(result.commands, cmd)
		delete(items, id)
		_ = occ
	}
	return false
}

// errorsIsNoRows matches pgx.ErrNoRows without importing pgx twice here.
// syncWindow bounds recurrence expansion fallbacks.
func syncWindow(now time.Time) (time.Time, time.Time) {
	return now.AddDate(0, 0, -90), now.AddDate(1, 0, 0)
}

func errorsIsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
