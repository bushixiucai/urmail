package loadbench

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	domaincollab "github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// Scenario builds the per-iteration operation for a named scenario.
// Every operation exercises the production store queries with explicit
// tenant/mailbox predicates, exactly as the HTTP handlers do.
func (h *Harness) Scenario(_ context.Context, name string, f *Fixture) (Op, error) {
	q := queries(h.Pool)
	switch name {
	case ScenarioMailboxListRead:
		return func(ctx context.Context) error {
			mboxes, err := q.ListMailboxesByTenant(ctx, f.TenantID)
			if err != nil {
				return err
			}
			if len(mboxes) == 0 {
				return errors.New("no mailboxes seeded")
			}
			_, err = q.GetMailboxByID(ctx, store.GetMailboxByIDParams{TenantID: f.TenantID, ID: mboxes[0].ID})
			return err
		}, nil
	case ScenarioContactCRUD:
		var seq atomic.Int64
		return func(ctx context.Context) error {
			n := seq.Add(1)
			emails := []byte(`[{"value":"crud@loadbench.test","type":"HOME"}]`)
			c, err := q.PlatformInsertContact(ctx, store.PlatformInsertContactParams{
				TenantID: f.TenantID, MailboxID: f.MailboxID, AddressbookID: f.Addressbook,
				DisplayName: fmt.Sprintf("CRUD %d", n), Emails: emails, Phones: []byte("[]"),
			})
			if err != nil {
				return err
			}
			if _, err = q.PlatformSearchContacts(ctx, store.PlatformSearchContactsParams{
				TenantID: f.TenantID, MailboxID: f.MailboxID, Query: fmt.Sprintf("CRUD %d", n),
			}); err != nil {
				return err
			}
			if _, err = q.PlatformUpdateContact(ctx, store.PlatformUpdateContactParams{
				DisplayName: fmt.Sprintf("CRUD %d updated", n), Emails: emails, Phones: []byte("[]"),
				TenantID: f.TenantID, MailboxID: f.MailboxID, ID: c.ID, ExpectedVersion: c.Version,
			}); err != nil {
				return err
			}
			_, err = q.PlatformDeleteContact(ctx, store.PlatformDeleteContactParams{
				TenantID: f.TenantID, MailboxID: f.MailboxID, ID: c.ID, Version: c.Version + 1,
			})
			return err
		}, nil
	case ScenarioCalendarExpand:
		now := time.Now()
		params := store.PlatformListEventsParams{
			TenantID: f.TenantID, MailboxID: f.MailboxID,
			RangeStart: pgtypeT(now), RangeEnd: pgtypeT(now.Add(7 * 24 * time.Hour)), PageLimit: 500,
		}
		return func(ctx context.Context) error {
			events, err := q.PlatformListEvents(ctx, params)
			if err != nil {
				return err
			}
			for _, ev := range events {
				if _, err = domaincollab.ExpandInstances(domaincollab.Event{
					Summary: ev.Summary, StartsAt: ev.StartsAt.Time, EndsAt: ev.EndsAt.Time,
					Timezone: ev.Timezone, RRule: ev.Rrule, Status: ev.Status,
				}, now, now.Add(7*24*time.Hour)); err != nil {
					return err
				}
			}
			return nil
		}, nil
	case ScenarioMarketingStep:
		return h.marketingStep(q, f), nil
	case ScenarioNotifyDrain:
		return h.notifyDrain(q, f), nil
	default:
		return nil, fmt.Errorf("unknown scenario %q", name)
	}
}

// marketingStep replicates one marketing worker iteration using the durable
// claim/authorize/finish path with SKIP LOCKED leases and quota windows.
// No SMTP sender is attached: nothing leaves the machine.
func (h *Harness) marketingStep(q *store.Queries, f *Fixture) Op {
	owner := "loadbench-" + uuid.NewString()
	return func(ctx context.Context) error {
		r, err := q.PlatformClaimMarketingRecipient(ctx, owner)
		if errors.Is(err, pgx.ErrNoRows) {
			// Recipients exhausted for this run window: recycle sent rows so
			// the claim path keeps exercising real work at a steady state.
			_, err = h.Pool.Exec(ctx, `UPDATE marketing_recipients SET status='queued',completed_at=NULL
				WHERE tenant_id=$1 AND campaign_id=$2 AND status='sent'`, f.TenantID, f.CampaignID)
			return err
		}
		if err != nil {
			return err
		}
		_, err = q.PlatformGetMarketingDelivery(ctx, store.PlatformGetMarketingDeliveryParams{TenantID: f.TenantID, ID: r.ID})
		if err != nil {
			return err
		}
		outcome, err := q.PlatformAuthorizeMarketingSend(ctx, store.PlatformAuthorizeMarketingSendParams{
			TenantID: f.TenantID, RecipientID: r.ID, LeaseOwner: owner, Fence: r.Fence,
		})
		if err != nil {
			return err
		}
		if outcome != "authorized" {
			// Mirror the production worker: rate_limited and other soft
			// outcomes release the durable claim and retry later; they are
			// not step errors.
			if _, relErr := q.PlatformReleaseMarketingClaim(ctx, store.PlatformReleaseMarketingClaimParams{
				TenantID: f.TenantID, ID: r.ID, LeaseOwner: pgtype.Text{String: owner, Valid: true}, Fence: r.Fence,
			}); relErr != nil {
				return relErr
			}
			return nil
		}
		_, err = q.PlatformFinishMarketingSend(ctx, store.PlatformFinishMarketingSendParams{
			TenantID: f.TenantID, RecipientID: r.ID, LeaseOwner: owner, Fence: r.Fence, Result: "sent",
		})
		return err
	}
}

// notifyDrain claims and finishes one email notification per iteration with
// the production SKIP LOCKED lease path. SMTP submission is stubbed out.
func (h *Harness) notifyDrain(q *store.Queries, f *Fixture) Op {
	return func(ctx context.Context) error {
		n, err := q.PlatformClaimEmailNotification(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = h.Pool.Exec(ctx, `UPDATE notification_outbox SET state='pending',attempts=0,delivered_at=NULL,
				lease_until=NULL,next_attempt_at=now() WHERE tenant_id=$1 AND channel='email' AND state='delivered'`, f.TenantID)
			return err
		}
		if err != nil {
			return err
		}
		rows, err := q.PlatformFinishEmailNotification(ctx, store.PlatformFinishEmailNotificationParams{
			ID: n.ID, Fence: n.Fence, Column3: "delivered", Column4: "",
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errors.New("notification finish lost its lease")
		}
		return nil
	}
}

// AllScenarios lists the budgeted scenarios in reporting order.
func AllScenarios() []string {
	return []string{ScenarioMailboxListRead, ScenarioContactCRUD, ScenarioCalendarExpand, ScenarioMarketingStep, ScenarioNotifyDrain}
}
