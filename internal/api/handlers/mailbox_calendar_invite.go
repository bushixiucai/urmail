package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

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
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
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
func (h MailboxCalendarHandler) cancelWithNotice(c *gin.Context, session webmail.Session, p appcollab.Principal, id, version int64) {
	if !h.allowInvitation(c) {
		return
	}
	sender, ok := h.invitationSender(c)
	if !ok {
		authError(c, 503, "WEBMAIL_DISABLED")
		return
	}
	row, err := h.Service.Q.PlatformGetEvent(c, store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "EVENT_NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	if row.Organizer != "" && !strings.EqualFold(row.Organizer, session.Address) {
		authError(c, 403, "INVITATION_ORGANIZER_REQUIRED")
		return
	}
	if row.Version != version {
		authError(c, 409, "EVENT_CONFLICT")
		return
	}
	event, _, err := appcollab.EventFromRow(row)
	if err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	event.Status = "cancelled" //nolint:misspell // RFC 5545 value
	inv := collab.Invitation{UID: row.Uid, Sequence: int(row.Sequence + 1), Method: collab.MethodCancel, Organizer: session.Address, Event: event}
	if _, err = collab.InvitationICS(inv); err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	finish, err := h.claimCalendarInvitation(c, p, row, collab.MethodCancel, true)
	if !invitationClaimError(c, err) {
		return
	}
	if err = collab.SendInvitations(c, inv, true, sender); err != nil {
		authError(c, 409, "MAILBOX_SEND_UNCERTAIN")
		return
	}
	if err = finishCalendarInvitation(finish); err != nil {
		authError(c, 409, "MAILBOX_SEND_UNCERTAIN")
		return
	}
	c.JSON(200, gin.H{"deleted": true, "notification_status": "sent"})
}

// IngestReply refuses the retired raw-ICS endpoint. A claimed attendee field
// cannot establish who sent a response; use the original mailbox message.
func (h MailboxCalendarHandler) IngestReply(c *gin.Context) {
	if _, ok := h.principal(c); !ok {
		return
	}
	authError(c, 400, "INVITATION_SOURCE_REQUIRED")
}

func (h MailboxCalendarHandler) allowInvitation(c *gin.Context) bool {
	if h.MaySend == nil {
		return true
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return false
	}
	allowed, err := h.MaySend(c, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, 503, "MAIL_SERVICE_UNAVAILABLE")
		return false
	}
	if !allowed {
		authError(c, 403, "MAIL_SERVICE_RESTRICTED")
		return false
	}
	return true
}
