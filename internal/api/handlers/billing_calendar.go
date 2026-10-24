package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

var (
	errBillingCalendarReview  = errors.New("billing calendar requires audited historical review")
	errBillingCalendarChanged = errors.New("billing calendar changed since quote")
)

func calendarSpec(row store.BillingCalendars) *billing.CalendarSpec {
	var wall billing.CalendarAnchor
	if json.Unmarshal(row.AnchorWall, &wall) != nil {
		return &billing.CalendarSpec{Timezone: row.Timezone, Version: row.Version}
	}
	return &billing.CalendarSpec{Timezone: row.Timezone, AnchorAt: row.AnchorAt.Time.UTC(), Version: row.Version, Wall: &wall}
}

// quoteBillingCalendar never infers an unknown historical timezone from today's setting.
func quoteBillingCalendar(ctx context.Context, q *store.Queries, tenantID int64, now time.Time, paidCurrent bool) (*billing.CalendarSpec, error) {
	row, err := q.GetBillingCalendar(ctx, tenantID)
	if err == nil {
		spec := calendarSpec(row)
		if !paidCurrent {
			spec.AnchorAt = now.UTC().Truncate(time.Microsecond)
			spec.Reanchor = true
			spec.Provisional = true
			spec.Wall = nil
		}
		if _, err = spec.Anchor(); err != nil {
			return nil, err
		}
		return spec, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	review, err := q.BillingCalendarReviewRequired(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if review {
		return nil, errBillingCalendarReview
	}
	zone, err := q.GetBillingBusinessTimezone(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	spec := &billing.CalendarSpec{Timezone: zone, AnchorAt: now.UTC().Truncate(time.Microsecond), Provisional: true}
	if _, err = spec.Anchor(); err != nil {
		return nil, err
	}
	return spec, nil
}

func verifyQuoteBillingCalendar(ctx context.Context, q *store.Queries, tenantID int64, spec *billing.CalendarSpec) error {
	if spec == nil {
		review, err := q.BillingCalendarReviewRequired(ctx, tenantID)
		if err != nil {
			return err
		}
		if review {
			return errBillingCalendarReview
		}
		return errBillingCalendarChanged
	}
	if _, err := spec.Anchor(); err != nil {
		return err
	}
	row, err := q.GetBillingCalendar(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		if spec.Version != 0 || !spec.Provisional || spec.Reanchor {
			return errBillingCalendarChanged
		}
		review, e := q.BillingCalendarReviewRequired(ctx, tenantID)
		if e != nil {
			return e
		}
		if review {
			return errBillingCalendarReview
		}
		zone, e := q.GetBillingBusinessTimezone(ctx, tenantID)
		if e != nil {
			return e
		}
		if zone != spec.Timezone {
			return errBillingCalendarChanged
		}
		return nil
	}
	if err != nil {
		return err
	}
	if spec.Version != row.Version || spec.Timezone != row.Timezone || (!spec.Reanchor && !spec.AnchorAt.Equal(row.AnchorAt.Time)) {
		return errBillingCalendarChanged
	}
	return nil
}

// settlementBillingCalendar runs under the existing tenant advisory lock. The
// first paid service captures its actual start; expired service gets a full new
// cycle, while the first frozen timezone and all historical terms stay intact.
func settlementBillingCalendar(ctx context.Context, q *store.Queries, tenantID, orderID int64, now time.Time, continuous, manualRecovery bool, agreed *billing.Quote) (*billing.CalendarSpec, error) {
	row, err := q.GetBillingCalendar(ctx, tenantID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	missing := errors.Is(err, pgx.ErrNoRows)
	var spec *billing.CalendarSpec
	if agreed != nil {
		spec = agreed.Calendar
	}
	if !manualRecovery && agreed != nil && spec == nil {
		return nil, errBillingCalendarReview
	}
	if missing {
		review, e := q.PlatformBillingCalendarReviewExcludingOrder(ctx, store.PlatformBillingCalendarReviewExcludingOrderParams{TenantID: tenantID, OrderID: orderID})
		if e != nil {
			return nil, e
		}
		if review {
			return nil, errBillingCalendarReview
		}
		zone, e := q.GetBillingBusinessTimezone(ctx, tenantID)
		if e != nil {
			return nil, e
		}
		if !manualRecovery && spec != nil && (spec.Version != 0 || spec.Timezone != zone) {
			return nil, errBillingCalendarChanged
		}
		anchorAt := now.UTC().Truncate(time.Microsecond)
		if _, e = billing.NewCalendarAnchor(anchorAt, zone); e != nil {
			return nil, e
		}
		row, e = q.PlatformInitializeBillingCalendar(ctx, store.PlatformInitializeBillingCalendarParams{TenantID: tenantID, Timezone: zone, FirstPaidAt: pgtype.Timestamptz{Time: anchorAt, Valid: true}, AnchorAt: pgtype.Timestamptz{Time: anchorAt, Valid: true}, Source: "first_paid", ReviewReason: "First fulfilled paid service", CreatedBy: pgtype.Int8{}})
		if e != nil {
			return nil, e
		}
		if e = auditSettlementCalendar(ctx, q, row, orderID, "establish"); e != nil {
			return nil, e
		}
	} else {
		if !manualRecovery && spec != nil && (spec.Version != row.Version || spec.Timezone != row.Timezone || (!spec.Reanchor && !spec.AnchorAt.Equal(row.AnchorAt.Time))) {
			return nil, errBillingCalendarChanged
		}
		if !continuous {
			row, err = q.PlatformReanchorBillingCalendar(ctx, store.PlatformReanchorBillingCalendarParams{TenantID: tenantID, Version: row.Version, AnchorAt: pgtype.Timestamptz{Time: now.UTC().Truncate(time.Microsecond), Valid: true}})
			if err != nil {
				return nil, err
			}
			if err = auditSettlementCalendar(ctx, q, row, orderID, "reactivate"); err != nil {
				return nil, err
			}
		}
	}
	return calendarSpec(row), nil
}

func auditSettlementCalendar(ctx context.Context, q *store.Queries, row store.BillingCalendars, orderID int64, action string) error {
	detail, _ := json.Marshal(map[string]any{"actor_type": "payment", "outcome": "succeeded", "order_id": orderID, "timezone": row.Timezone, "anchor_at": row.AnchorAt.Time, "version": row.Version})
	_, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: row.TenantID, Valid: true}, Action: "BILLING_CALENDAR " + action, TargetType: pgtype.Text{String: "billing_calendars", Valid: true}, TargetID: pgtype.Text{String: fmt.Sprint(row.TenantID), Valid: true}, Detail: detail})
	return err
}
