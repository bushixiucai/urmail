package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type calendarMailSource struct {
	FolderEnc   string `json:"folder_enc"`
	UID         uint32 `json:"uid"`
	UIDValidity uint32 `json:"uid_validity"`
}
type calendarMailAction struct {
	Source          calendarMailSource `json:"source"`
	SourceDigest    string             `json:"source_digest"`
	Action          string             `json:"action"`
	ExpectedVersion int64              `json:"expected_version"`
	Confirmed       bool               `json:"confirmed"`
}

// RegisterCalendarIncomingRoutes binds invitation actions to an actual message
// in the authenticated mailbox, never a client-uploaded organizer payload.
func RegisterCalendarIncomingRoutes(group *gin.RouterGroup, h MailboxCalendarHandler) {
	group.GET("/calendars/invitations", h.previewMailInvitation)
	group.POST("/calendars/invitations", h.applyMailInvitation)
}
func (h MailboxCalendarHandler) readSchedulingMail(c *gin.Context, source calendarMailSource) (appcollab.SchedulingMail, webmail.Session, string, bool) {
	var empty appcollab.SchedulingMail
	if h.Native == nil || h.Pool == nil {
		authError(c, 503, "CALENDAR_UNAVAILABLE")
		return empty, webmail.Session{}, "", false
	}
	folder, err := webmail.DecodeFolderName(source.FolderEnc)
	if err != nil || source.UID == 0 || source.UIDValidity == 0 {
		authError(c, 400, "INVALID_PARAMS")
		return empty, webmail.Session{}, "", false
	}
	client, session, ok := h.Native.dialIMAP(c)
	if !ok {
		return empty, session, "", false
	}
	defer func() { _ = client.Close() }()
	if !mailboxExpectedValidity(c, client, folder, source.UIDValidity, true) {
		return empty, session, "", false
	}
	rawClient, ok := client.(webmail.RawMessageAccessor)
	if !ok {
		authError(c, 503, "CALENDAR_SOURCE_UNAVAILABLE")
		return empty, session, "", false
	}
	raw, validity, err := rawClient.GetRawMessage(c.Request.Context(), folder, source.UID)
	if errors.Is(err, webmail.ErrMessageNotFound) {
		authError(c, 404, "MESSAGE_NOT_FOUND")
		return empty, session, "", false
	}
	if err != nil {
		authError(c, 503, "IMAP_UNAVAILABLE")
		return empty, session, "", false
	}
	if validity != source.UIDValidity {
		authError(c, 409, "MAILBOX_UIDVALIDITY_CHANGED")
		return empty, session, "", false
	}
	message, err := appcollab.ParseSchedulingMail(c.Request.Context(), raw, h.SchedulingTXT)
	if err != nil {
		authError(c, 404, "CALENDAR_INVITATION_NOT_FOUND")
		return empty, session, "", false
	}
	recipient := ""
	for _, candidate := range message.Recipients {
		owned := strings.EqualFold(candidate, session.Address)
		if !owned && h.Native.SelfSettings != nil {
			owned, err = h.Native.SelfSettings.SenderIdentityAllowed(c.Request.Context(), session.TenantID, session.Address, candidate)
			if err != nil {
				authError(c, 503, "MAIL_POLICY_UNAVAILABLE")
				return empty, session, "", false
			}
		}
		if !owned {
			continue
		}
		if message.Message.Method == collab.MethodRequest {
			attendee := false
			for _, address := range message.Message.Event.Attendees {
				attendee = attendee || strings.EqualFold(address, candidate)
			}
			if !attendee {
				continue
			}
		}
		recipient = candidate
		break
	}
	if recipient == "" {
		authError(c, 403, "INVITATION_RECIPIENT_REFUSED")
		return empty, session, "", false
	}
	if message.Message.Method == collab.MethodReply {
		owned := strings.EqualFold(message.Message.Organizer, session.Address)
		if !owned && h.Native.SelfSettings != nil {
			owned, err = h.Native.SelfSettings.SenderIdentityAllowed(c.Request.Context(), session.TenantID, session.Address, message.Message.Organizer)
			if err != nil {
				authError(c, 503, "MAIL_POLICY_UNAVAILABLE")
				return empty, session, "", false
			}
		}
		if !owned {
			authError(c, 403, "INVITATION_RECIPIENT_REFUSED")
			return empty, session, "", false
		}
		recipient = message.Message.Organizer
	}
	return message, session, recipient, true
}
func schedulingActions(message appcollab.SchedulingMail, linked *store.MailboxEvents, recipient string) []string {
	actions := []string{}
	if !message.Verified {
		if message.Message.Method == collab.MethodRequest {
			return []string{"copy_personal"}
		}
		return actions
	}
	if linked != nil {
		if message.Message.Method != collab.MethodReply && linked.Organizer != message.Message.Organizer {
			return actions
		}
		if message.Message.Method != collab.MethodReply && (message.Message.Sequence < int(linked.InvitationSequence) || (message.Message.Sequence == int(linked.InvitationSequence) && linked.InvitationStamp.Valid && message.Message.Stamp.Before(linked.InvitationStamp.Time))) {
			return actions
		}
	}
	if linked == nil && message.Message.RecurrenceID != "" {
		if message.Message.Event != nil {
			return []string{"copy_personal"}
		}
		return actions
	}
	switch message.Message.Method {
	case collab.MethodRequest:
		actions = []string{"accept", "decline", "tentative"}
		if linked != nil {
			actions = append(actions, "apply_update")
		}
	case collab.MethodCancel:
		if linked != nil {
			actions = []string{"apply_cancel"}
		}
	case collab.MethodReply:
		if linked != nil && strings.EqualFold(message.Message.Organizer, recipient) && (linked.Organizer == "" || strings.EqualFold(linked.Organizer, message.Message.Organizer)) {
			actions = []string{"apply_reply"}
		}
	}
	return actions
}
func (h MailboxCalendarHandler) previewMailInvitation(c *gin.Context) {
	uid, err := strconv.ParseUint(c.Query("uid"), 10, 32)
	validity, vErr := strconv.ParseUint(c.Query("uid_validity"), 10, 32)
	if err != nil || vErr != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	message, session, recipient, ok := h.readSchedulingMail(c, calendarMailSource{FolderEnc: c.Query("folder_enc"), UID: uint32(uid), UIDValidity: uint32(validity)})
	if !ok {
		return
	}
	row, err := store.New(h.Pool).PlatformFindSchedulingEvent(c.Request.Context(), store.PlatformFindSchedulingEventParams{TenantID: session.TenantID, MailboxID: session.MailboxID, Uid: message.Message.UID})
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
	verification := "unverified"
	if message.Verified {
		verification = "verified"
	}
	var event any
	if message.Message.Event != nil {
		e := message.Message.Event
		event = gin.H{"summary": e.Summary, "description": e.Description, "location": e.Location, "starts_at": e.StartsAt, "ends_at": e.EndsAt, "timezone": e.Timezone, "all_day": e.AllDay, "rrule": e.RRule}
	}
	var linkedView any
	if linked != nil {
		linkedView = gin.H{"id": linked.ID, "version": linked.Version, "response": linked.InvitationResponse, "sequence": linked.InvitationSequence}
	}
	actions := schedulingActions(message, linked, recipient)
	notificationStatus := "none"
	for _, action := range []string{"accept", "decline", "tentative"} {
		prior, e := store.New(h.Pool).PlatformGetDataImportReceipt(c.Request.Context(), store.PlatformGetDataImportReceiptParams{TenantID: session.TenantID, MailboxID: session.MailboxID, Kind: "calendar_response", IdempotencyKey: calendarActionKey(message, action, recipient)})
		if e == nil {
			if prior.Status != "done" {
				notificationStatus = "uncertain"
				actions = []string{}
				break
			}
			notificationStatus = "sent"
		} else if !errors.Is(e, pgx.ErrNoRows) {
			authError(c, 503, "CALENDAR_UNAVAILABLE")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"source_digest": message.Digest, "notification_status": notificationStatus, "verification": verification, "verification_reason": message.VerificationReason, "method": message.Message.Method, "uid": message.Message.UID, "sequence": message.Message.Sequence, "organizer": message.Message.Organizer, "from": message.From, "recipient": recipient, "recurrence_id": message.Message.RecurrenceID, "event": event, "linked_event": linkedView, "actions": actions})
}
func calendarActionKey(message appcollab.SchedulingMail, action, recipient string) string {
	identity := message.Message.RecurrenceID
	if message.Message.RecurrenceDate != "" {
		identity = "date:" + message.Message.RecurrenceDate
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%d|%s|%s|%s|%s", message.Message.Organizer, message.Message.UID, message.Message.Sequence, message.Message.Stamp.UTC(), identity, action, recipient))
	return hex.EncodeToString(sum[:])
}
func calendarActionAllowed(actions []string, action string) bool {
	for _, allowed := range actions {
		if allowed == action {
			return true
		}
	}
	return false
}

func alignSchedulingDate(message appcollab.SchedulingMail, linked *store.MailboxEvents) (appcollab.SchedulingMail, error) {
	if linked == nil || !message.Message.RecurrenceIsDate {
		return message, nil
	}
	date, err := time.Parse("20060102", message.Message.RecurrenceDate)
	if err != nil {
		return message, err
	}
	loc, err := time.LoadLocation(linked.Timezone)
	if err != nil {
		return message, err
	}
	identity := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	message.Message.RecurrenceID = identity.UTC().Format(time.RFC3339)
	if message.Message.Event != nil {
		event := *message.Message.Event
		from, err := time.LoadLocation(event.Timezone)
		if err != nil {
			return message, err
		}
		for _, value := range []*time.Time{&event.StartsAt, &event.EndsAt} {
			local := value.In(from)
			*value = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
		}
		event.Timezone = linked.Timezone
		event.RecurrenceID = message.Message.RecurrenceID
		message.Message.Event = &event
	}
	return message, nil
}
