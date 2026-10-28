package collab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	domain "github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// ReminderWorker durably schedules bounded DISPLAY alarms. Delivery means an
// authenticated mailbox can read a persisted notification; it never submits
// email, opens a URL, plays audio or executes an external alarm action.
type ReminderWorker struct {
	Pool   *db.Pool
	Logger *zap.Logger
	Now    func() time.Time
}

// RunOnce scans fairly by a persisted timestamp, schedules at most 400 bounded
// instances per event, and publishes at most 100 due notifications atomically.
func (w ReminderWorker) RunOnce(ctx context.Context) error {
	if w.Pool == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	tx, err := w.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	cutoff, cutoffErr := q.PlatformCalendarRestoreCutoff(ctx)
	if cutoffErr != nil && !errors.Is(cutoffErr, pgx.ErrNoRows) {
		return cutoffErr
	}
	rows, err := q.PlatformTakeReminderScan(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		event, _, e := EventFromRow(row)
		if e != nil {
			w.warning(row.ID, e)
			continue
		}
		starts, e := domain.ExpandInstances(event, now.Add(-24*time.Hour), now.Add(29*24*time.Hour))
		if e != nil {
			w.warning(row.ID, e)
			continue
		}
		for _, start := range starts {
			instance := domain.InstanceAt(event, start)
			identity := instance.RecurrenceID
			if identity == "" {
				identity = start.UTC().Format(time.RFC3339Nano)
			}
			for _, minutes := range event.ReminderMinutes {
				fire := instance.StartsAt.Add(-time.Duration(minutes) * time.Minute)
				if fire.After(now.Add(time.Hour)) {
					continue
				}
				if cutoff.Valid && fire.Before(cutoff.Time) && row.UpdatedAt.Time.Before(cutoff.Time) {
					continue
				}
				if fire.Before(now.Add(-24 * time.Hour)) {
					continue
				}
				hash := sha256.Sum256(fmt.Appendf(nil, "%d|%s|%d", row.ID, identity, minutes))
				key := hex.EncodeToString(hash[:])
				payload, e := json.Marshal(map[string]any{"event_id": row.ID, "version": row.Version, "uid": row.Uid, "summary": instance.Summary, "starts_at": instance.StartsAt, "ends_at": instance.EndsAt, "timezone": instance.Timezone, "recurrence_id": identity, "minutes_before": minutes, "fire_at": fire, "acknowledged": false})
				if e != nil {
					return e
				}
				if e = q.PlatformScheduleCalendarReminder(ctx, store.PlatformScheduleCalendarReminderParams{TenantID: row.TenantID, MailboxID: row.MailboxID, IdempotencyKey: key, Payload: payload, LeaseUntil: pgtype.Timestamptz{Time: fire, Valid: true}}); e != nil {
					return e
				}
			}
		}
	}
	if err = q.PlatformDiscardStaleCalendarReminders(ctx); err != nil {
		return err
	}
	if _, err = q.PlatformPublishDueCalendarReminders(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (w ReminderWorker) warning(id int64, err error) {
	if w.Logger != nil {
		w.Logger.Warn("calendar reminder expansion refused", zap.Int64("event_id", id), zap.Error(err))
	}
}
