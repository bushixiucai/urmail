package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func (h MailboxCalendarHandler) applyMailInvitation(c *gin.Context) {
	var body calendarMailAction
	if !bindMailboxMessage(c, &body) {
		return
	}
	if !body.Confirmed {
		authError(c, 400, "INVITATION_CONFIRMATION_REQUIRED")
		return
	}
	message, session, recipient, ok := h.readSchedulingMail(c, body.Source)
	if !ok {
		return
	}
	if message.Digest != body.SourceDigest {
		authError(c, 409, "CALENDAR_SOURCE_CHANGED")
		return
	}
	sendReply := body.Action == "accept" || body.Action == "decline" || body.Action == "tentative"
	if sendReply && !h.allowInvitation(c) {
		return
	}
	if sendReply && (h.Submitter == nil || h.Vault == nil) {
		authError(c, 503, "WEBMAIL_DISABLED")
		return
	}
	password, sendFrom := "", ""
	if sendReply {
		var exists bool
		password, exists = h.Vault.Get(session.TokenHash)
		if !exists {
			authError(c, 401, "MAILBOX_REAUTH_REQUIRED")
			return
		}
		_, sendFrom, ok = h.Native.authorizedIdentity(c, session, recipient)
		if !ok {
			return
		}
	}
	p := appcollab.Principal{TenantID: session.TenantID, MailboxID: session.MailboxID}
	tx, err := h.Pool.MailboxTx(c.Request.Context(), p.TenantID, p.MailboxID)
	if err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	row, err := q.PlatformFindSchedulingEvent(c.Request.Context(), store.PlatformFindSchedulingEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Uid: message.Message.UID})
	var linked *store.MailboxEvents
	if err == nil {
		linked = &row
	} else if !errors.Is(err, pgx.ErrNoRows) {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	message, err = alignSchedulingDate(message, linked)
	if err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	key := calendarActionKey(message, body.Action, recipient)
	kind := "calendar_mail_action"
	if sendReply {
		kind = "calendar_response"
	}
	prior, err := q.PlatformGetDataImportReceipt(c.Request.Context(), store.PlatformGetDataImportReceiptParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Kind: kind, IdempotencyKey: key})
	if err == nil {
		if prior.Status == "done" {
			c.JSON(200, gin.H{"applied": true, "already_applied": true, "notification_status": "sent"})
			return
		}
		authError(c, 409, "MAILBOX_SEND_UNCERTAIN")
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	if !calendarActionAllowed(schedulingActions(message, linked, recipient), body.Action) {
		authError(c, 403, "INVITATION_ACTION_REFUSED")
		return
	}
	if linked != nil && body.ExpectedVersion != linked.Version {
		authError(c, 409, "EVENT_CONFLICT")
		return
	}
	svc := appcollab.Service{Q: q}
	var event collab.Event
	eventID := int64(0)
	response := ""
	if linked != nil {
		event, _, err = appcollab.EventFromRow(*linked)
		if err != nil {
			authError(c, 503, "CALENDAR_UNAVAILABLE")
			return
		}
		eventID = linked.ID
		response = linked.InvitationResponse
	}
	switch body.Action {
	case "copy_personal":
		event = *message.Message.Event
		event.Organizer = ""
		event.Sequence = 0
		event.UID = ""
		event.Attendees = nil
		event.AttendeeStatus = nil
		event.ReminderMinutes = nil
		event.RecurrenceID = ""
		calendar, e := svc.EnsureCalendar(c.Request.Context(), p, defaultCalendarName, event.Timezone)
		if e != nil {
			authError(c, 503, "CALENDAR_UNAVAILABLE")
			return
		}
		saved, e := svc.CreateEvent(c.Request.Context(), p, calendar.ID, "personal-"+key+"@urmail", event)
		if e != nil {
			authError(c, 409, "EVENT_CONFLICT")
			return
		}
		eventID = saved.ID
	case "apply_reply":
		if linked == nil || !strings.EqualFold(message.Message.Organizer, recipient) || message.Message.Sequence != int(linked.InvitationSequence) {
			authError(c, 409, "EVENT_CONFLICT")
			return
		}
		next, e := collab.ApplyReply(event, *message.Message.Reply)
		if e != nil {
			authError(c, 403, "INVITATION_REPLY_REFUSED")
			return
		}
		attendees, e := json.Marshal(collab.AttendeeStates(next))
		if e != nil {
			authError(c, 503, "CALENDAR_UNAVAILABLE")
			return
		}
		if _, e = q.PlatformRecordSchedulingReply(c.Request.Context(), store.PlatformRecordSchedulingReplyParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: linked.ID, Version: linked.Version, Attendees: attendees}); e != nil {
			authError(c, 409, "EVENT_CONFLICT")
			return
		}
	case "apply_cancel":
		if linked == nil {
			authError(c, 409, "EVENT_CONFLICT")
			return
		}
		if message.Message.RecurrenceID == "" {
			event.Status = "cancelled" //nolint:misspell // RFC 5545 value.
		} else {
			event, err = schedulingOccurrence(event, nil, message.Message.RecurrenceID, true)
			if err != nil {
				authError(c, 409, "EVENT_CONFLICT")
				return
			}
		} //nolint:misspell // RFC 5545 spelling.
		if _, err = svc.UpdateEvent(c.Request.Context(), p, linked.ID, linked.Version, event); err != nil {
			authError(c, 409, "EVENT_CONFLICT")
			return
		}
	default:
		if message.Message.Event == nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		incoming := *message.Message.Event
		incoming.ReminderMinutes = nil
		switch body.Action {
		case "accept":
			response = collab.PartStatAccepted
		case "decline":
			response = collab.PartStatDeclined
		case "tentative":
			response = collab.PartStatTentative
		case "apply_update":
		default:
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if linked == nil {
			if incoming.RecurrenceID != "" {
				authError(c, 409, "INVITATION_SERIES_REQUIRED")
				return
			}
			event = incoming
			event.Organizer = message.Message.Organizer
			event.Sequence = message.Message.Sequence
			if response != "" {
				if event.AttendeeStatus == nil {
					event.AttendeeStatus = map[string]string{}
				}
				event.AttendeeStatus[recipient] = response
			}
			calendar, e := svc.EnsureCalendar(c.Request.Context(), p, defaultCalendarName, event.Timezone)
			if e != nil {
				authError(c, 503, "CALENDAR_UNAVAILABLE")
				return
			}
			saved, e := svc.CreateEvent(c.Request.Context(), p, calendar.ID, message.Message.UID, event)
			if e != nil {
				authError(c, 409, "EVENT_CONFLICT")
				return
			}
			eventID = saved.ID
		} else {
			if incoming.RecurrenceID != "" {
				event, err = schedulingOccurrence(event, &incoming, incoming.RecurrenceID, false)
				if err != nil {
					authError(c, 409, "EVENT_CONFLICT")
					return
				}
			} else {
				incoming.ReminderMinutes = event.ReminderMinutes
				event = incoming
			}
			if response != "" {
				if event.AttendeeStatus == nil {
					event.AttendeeStatus = map[string]string{}
				}
				event.AttendeeStatus[recipient] = response
			}
			if _, err = svc.UpdateEvent(c.Request.Context(), p, linked.ID, linked.Version, event); err != nil {
				authError(c, 409, "EVENT_CONFLICT")
				return
			}
		}
	}
	if body.Action != "copy_personal" && body.Action != "apply_reply" {
		if _, err = q.PlatformBindSchedulingEvent(c.Request.Context(), store.PlatformBindSchedulingEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: eventID, Organizer: message.Message.Organizer, InvitationSequence: int32(min(max(message.Message.Sequence, 0), 2147483647)), InvitationStamp: pgtype.Timestamptz{Time: message.Message.Stamp, Valid: true}, InvitationDigest: message.Digest, InvitationResponse: response}); err != nil {
			authError(c, 409, "EVENT_CONFLICT")
			return
		}
	}
	payload, err := json.Marshal(map[string]any{"event_id": eventID, "uid": message.Message.UID, "sequence": message.Message.Sequence, "action": body.Action, "source": body.Source, "source_digest": message.Digest, "recipient": recipient})
	if err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	if sendReply {
		err = q.PlatformClaimCalendarResponse(c.Request.Context(), store.PlatformClaimCalendarResponseParams{TenantID: p.TenantID, MailboxID: p.MailboxID, IdempotencyKey: key, Payload: payload})
	} else {
		err = q.PlatformInsertDataImportReceipt(c.Request.Context(), store.PlatformInsertDataImportReceiptParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Kind: kind, IdempotencyKey: key, Payload: payload})
	}
	if err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	if err = tx.Commit(c.Request.Context()); err != nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return
	}
	if !sendReply {
		c.JSON(200, gin.H{"applied": true, "event_id": eventID, "notification_status": "not_requested"})
		return
	}
	event.UID = message.Message.UID
	event.RecurrenceID = message.Message.RecurrenceID
	ics, err := collab.ResponseICS(event, message.Message.Organizer, recipient, response, message.Message.Sequence, time.Now().UTC())
	if err != nil {
		authError(c, 409, "MAILBOX_SEND_UNCERTAIN")
		return
	}
	sendCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	submission, err := h.Submitter.Send(sendCtx, session.Address, password, webmail.Outgoing{From: sendFrom, To: []string{message.Message.Organizer}, Subject: "Re: " + event.Summary, Text: fmt.Sprintf("%s: %s", response, event.Summary), InReplyTo: message.MessageID, References: message.MessageID, Attachments: []webmail.OutgoingAttachment{{Filename: "response.ics", ContentType: "text/calendar; charset=utf-8; method=REPLY", Content: []byte(ics)}}})
	if err != nil {
		authError(c, 409, "MAILBOX_SEND_UNCERTAIN")
		return
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	n, err := store.New(h.Pool).PlatformFinishCalendarResponse(finishCtx, store.PlatformFinishCalendarResponseParams{TenantID: p.TenantID, MailboxID: p.MailboxID, IdempotencyKey: key})
	if err != nil || n != 1 {
		authError(c, 409, "MAILBOX_SEND_UNCERTAIN")
		return
	}
	sentCopy := "pending"
	if len(submission.Raw) > 0 && h.Native.Dialer != nil {
		if client, e := h.Native.Dialer.Dial(finishCtx, session.Address, password); e == nil {
			if _, e = client.SaveSentCopy(finishCtx, submission.Raw); e == nil {
				sentCopy = "saved"
			}
			_ = client.Close()
		}
	}
	c.JSON(200, gin.H{"applied": true, "event_id": eventID, "notification_status": "sent", "sent_copy": sentCopy})
}

func schedulingOccurrence(master collab.Event, child *collab.Event, identity string, cancel bool) (collab.Event, error) {
	stamp, err := time.Parse(time.RFC3339, identity)
	if err != nil || master.RRule == "" {
		return master, collab.ErrInvalidEvent
	}
	valid := false
	for _, override := range master.Overrides {
		valid = valid || override.RecurrenceID.Equal(stamp)
	}
	if !valid {
		instances, e := collab.ExpandInstances(master, stamp.Add(-time.Second), stamp.Add(time.Second))
		if e != nil || len(instances) == 0 {
			return master, collab.ErrInvalidEvent
		}
	}
	instance := collab.InstanceAt(master, stamp)
	if child != nil {
		instance = *child
	}
	if cancel {
		instance.Status = "cancelled" //nolint:misspell // RFC 5545 value.
	} //nolint:misspell // RFC 5545 spelling.
	override := collab.OccurrenceOverride{RecurrenceID: stamp, Summary: instance.Summary, Description: instance.Description, Location: instance.Location, StartsAt: instance.StartsAt, EndsAt: instance.EndsAt, AllDay: instance.AllDay, Status: instance.Status}
	next := make([]collab.OccurrenceOverride, 0, len(master.Overrides)+1)
	for _, existing := range master.Overrides {
		if !existing.RecurrenceID.Equal(stamp) {
			next = append(next, existing)
		}
	}
	next = append(next, override)
	master.Overrides = next
	return collab.NormalizeEvent(master)
}
