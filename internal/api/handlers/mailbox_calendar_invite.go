package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// defaultInvitationGate rate limits invitation bursts for every calendar
// handler in this process: one burst per event UID, SEQUENCE and method, so
// re-POSTing the same event version cannot storm the mail system. Sending
// again becomes possible after the next SEQUENCE bump (an event update).
var defaultInvitationGate = newInvitationGate()

type invitationGate struct {
	mu   sync.Mutex
	sent map[string]bool
}

func newInvitationGate() *invitationGate {
	return &invitationGate{sent: map[string]bool{}}
}

func (g *invitationGate) key(uid string, sequence int64, method string) string {
	return fmt.Sprintf("%s|%d|%s", uid, sequence, method)
}

// allow claims the one-shot slot; it returns false when this exact
// invitation already went out.
func (g *invitationGate) allow(uid string, sequence int64, method string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := g.key(uid, sequence, method)
	if g.sent[key] {
		return false
	}
	g.sent[key] = true
	return true
}

// release returns the slot after a failed send so a retry is possible.
func (g *invitationGate) release(uid string, sequence int64, method string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.sent, g.key(uid, sequence, method))
}

// invitationMessage builds the RFC 5546-ish invitation mail: a text body for
// clients without calendar support plus a text/calendar attachment carrying
// METHOD, UID, SEQUENCE, ORGANIZER and ATTENDEE lines. One message per
// attendee keeps each recipient's To line single-valued.
func invitationMessage(inv collab.Invitation, attendee string) (webmail.Outgoing, error) {
	body, err := collab.InvitationICS(inv)
	if err != nil {
		return webmail.Outgoing{}, err
	}
	subject := "Invitation: " + inv.Event.Summary
	if inv.Method == collab.MethodCancel {
		// The double-L spelling is deliberate, conventional iCalendar
		// wording kept for user-visible subject lines.
		subject = "Cancelled: " + inv.Event.Summary //nolint:misspell // RFC 5545-style wording kept in user-visible subject
	}
	text := fmt.Sprintf(
		"%s\n\nWhat: %s\nWhen: %s - %s UTC\nWhere: %s\nOrganizer: %s\n\nThe full update is attached as invite.ics.\n",
		textLead(inv.Method), inv.Event.Summary,
		inv.Event.StartsAt.UTC().Format(time.RFC3339), inv.Event.EndsAt.UTC().Format(time.RFC3339),
		inv.Event.Location, inv.Organizer,
	)
	contentType := "text/calendar; charset=utf-8; method=" + inv.Method
	return webmail.Outgoing{
		From:    inv.Organizer,
		To:      []string{attendee},
		Subject: subject,
		Text:    text,
		Attachments: []webmail.OutgoingAttachment{{
			Filename:    "invite.ics",
			ContentType: contentType,
			Content:     []byte(body),
		}},
	}, nil
}

func textLead(method string) string {
	if method == collab.MethodCancel {
		return "This event has been cancelled by the organizer." //nolint:misspell // user-visible wording kept
	}
	return "You are invited to this event. Please accept or decline from your calendar."
}

// SendInvitationsAs builds an InviteSender from the mailbox's own identity:
// each invitation is submitted through the existing Submitter with the
// organizer address as both AUTH identity and From header. Invitations are
// regular sent mail; the Sent copy follows the same pending semantics as
// normal sends (the Submitter's raw DATA is available for a later Sent
// APPEND, which the invitation path does not perform inline).
// SendInvitationsAs uses an empty submission password (test/fake submitters).
func SendInvitationsAs(submitter webmail.Submitter) collab.InviteSender {
	return SendInvitationsAsWithPassword(submitter, func(context.Context, string) (string, error) { return "", nil })
}

// SendInvitationsAsWithPassword resolves the organizer's vaulted submission
// password per invitation burst; production wiring must inject the credential
// vault lookup so real SMTP AUTH succeeds.
func SendInvitationsAsWithPassword(submitter webmail.Submitter, password func(ctx context.Context, address string) (string, error)) collab.InviteSender {
	if submitter == nil {
		return nil
	}
	if password == nil {
		password = func(context.Context, string) (string, error) { return "", nil }
	}
	return func(ctx context.Context, inv collab.Invitation) error {
		recipients := inv.Event.Attendees
		sent := 0
		for _, attendee := range recipients {
			if attendee == inv.Organizer {
				continue
			}
			msg, err := invitationMessage(inv, attendee)
			if err != nil {
				return err
			}
			password, passErr := password(ctx, inv.Organizer)
			if passErr != nil {
				return fmt.Errorf("resolve invitation credential: %w", passErr)
			}
			if _, err := submitter.Send(ctx, inv.Organizer, password, msg); err != nil {
				if sent > 0 {
					return fmt.Errorf("invitation sent to %d of %d attendees before failure: %w", sent, len(recipients), err)
				}
				return err
			}
			sent++
		}
		return nil
	}
}

// cancelWithNotice marks a delete-with-notify: before the tombstone write it
// delivers a METHOD:CANCEL invitation for the stored event. Refusing when
// the notice cannot be delivered keeps storage and attendee calendars honest.
func (h MailboxCalendarHandler) cancelWithNotice(c *gin.Context, session webmail.Session, p appcollab.Principal, id, _ int64) bool {
	row, err := h.Service.Q.PlatformGetEvent(c.Request.Context(), store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "EVENT_NOT_FOUND")
		return false
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return false
	}
	emails, status := appcollab.DecodeAttendees(row.Attendees)
	event, normErr := collab.NormalizeEvent(collab.Event{
		Summary: row.Summary, Description: row.Description, Location: row.Location,
		StartsAt: row.StartsAt.Time, EndsAt: row.EndsAt.Time, AllDay: row.AllDay,
		// The double-L spelling is the RFC 5545 STATUS wire token.
		Timezone: row.Timezone, RRule: row.Rrule, Status: "cancelled", //nolint:misspell // RFC 5545 STATUS value
		Attendees: emails, AttendeeStatus: status,
	})
	if normErr != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return false
	}
	sequence := int(row.Sequence)
	if sequence <= 0 {
		sequence = int(row.Version)
	}
	invitation := collab.Invitation{UID: row.Uid, Sequence: sequence, Method: collab.MethodCancel, Organizer: session.Address, Event: event}
	if !defaultInvitationGate.allow(row.Uid, int64(sequence), collab.MethodCancel) {
		// A cancel notice already went out for this version; delete proceeds.
		return true
	}
	if err := collab.SendInvitations(c.Request.Context(), invitation, true, h.InviteSender); err != nil {
		defaultInvitationGate.release(row.Uid, int64(sequence), collab.MethodCancel)
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return false
	}
	return true
}

// IngestReply applies one attendee's METHOD:REPLY answer to the stored event
// so the organizer's attendee list shows the response. The reply UID must
// name the event, the replier must be a stored attendee, and only that
// attendee's status changes.
func (h MailboxCalendarHandler) IngestReply(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	raw, err := c.GetRawData()
	if err != nil || len(raw) == 0 || len(raw) > 200000 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	reply, parseErr := collab.ParseReply(string(raw))
	if parseErr != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := h.Service.ApplyReply(c.Request.Context(), p, id, reply)
	if errors.Is(err, appcollab.ErrConflict) || errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusConflict, "EVENT_CONFLICT")
		return
	}
	if errors.Is(err, collab.ErrInvalidEvent) {
		authError(c, http.StatusForbidden, "INVITATION_REPLY_REFUSED")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CALENDAR_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, row)
}
