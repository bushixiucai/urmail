// Package collab stores mailbox contacts and calendars. PostgreSQL is the only
// copy. Callers pass the mailbox taken from an authenticated session; every
// query also names that tenant and mailbox.
package collab

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// ErrConflict means the object version changed before this write.
var ErrConflict = collab.ErrConflict

// ErrNotFound means the object is missing or belongs to another mailbox.
var ErrNotFound = pgx.ErrNoRows

// Store is the subset of sqlc this service uses.
type Store interface {
	PlatformEnsureAddressbook(ctx context.Context, arg store.PlatformEnsureAddressbookParams) (store.MailboxAddressbooks, error)
	PlatformGetAddressbookByName(ctx context.Context, arg store.PlatformGetAddressbookByNameParams) (store.MailboxAddressbooks, error)
	PlatformInsertContact(ctx context.Context, arg store.PlatformInsertContactParams) (store.PlatformInsertContactRow, error)
	PlatformUpdateContact(ctx context.Context, arg store.PlatformUpdateContactParams) (store.PlatformUpdateContactRow, error)
	PlatformGetContact(ctx context.Context, arg store.PlatformGetContactParams) (store.MailboxContacts, error)
	PlatformListContacts(ctx context.Context, arg store.PlatformListContactsParams) ([]store.MailboxContacts, error)
	PlatformSearchContacts(ctx context.Context, arg store.PlatformSearchContactsParams) ([]store.MailboxContacts, error)
	PlatformDeleteContact(ctx context.Context, arg store.PlatformDeleteContactParams) (store.PlatformDeleteContactRow, error)
	PlatformEnsureCalendar(ctx context.Context, arg store.PlatformEnsureCalendarParams) (store.MailboxCalendars, error)
	PlatformGetCalendarByName(ctx context.Context, arg store.PlatformGetCalendarByNameParams) (store.MailboxCalendars, error)
	PlatformInsertEvent(ctx context.Context, arg store.PlatformInsertEventParams) (store.PlatformInsertEventRow, error)
	PlatformUpdateEvent(ctx context.Context, arg store.PlatformUpdateEventParams) (store.PlatformUpdateEventRow, error)
	PlatformGetEvent(ctx context.Context, arg store.PlatformGetEventParams) (store.MailboxEvents, error)
	PlatformListEvents(ctx context.Context, arg store.PlatformListEventsParams) ([]store.MailboxEvents, error)
	PlatformDeleteEvent(ctx context.Context, arg store.PlatformDeleteEventParams) (store.PlatformDeleteEventRow, error)
	PlatformListChanges(ctx context.Context, arg store.PlatformListChangesParams) ([]store.MailboxChanges, error)
}

// Service is safe for concurrent use.
type Service struct{ Q Store }

// Principal is the mailbox that owns the data.
type Principal struct {
	TenantID  int64
	MailboxID int64
}

// EnsureAddressbook returns the live book with this name, creating it once.
func (s Service) EnsureAddressbook(ctx context.Context, p Principal, name string) (store.MailboxAddressbooks, error) {
	name = strings.TrimSpace(name)
	if name == "" || p.TenantID <= 0 || p.MailboxID <= 0 {
		return store.MailboxAddressbooks{}, collab.ErrInvalidContact
	}
	row, err := s.Q.PlatformEnsureAddressbook(ctx, store.PlatformEnsureAddressbookParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Name: name})
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.MailboxAddressbooks{}, err
	}
	return s.Q.PlatformGetAddressbookByName(ctx, store.PlatformGetAddressbookByNameParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Name: name})
}

// CreateContact validates and inserts one contact.
func (s Service) CreateContact(ctx context.Context, p Principal, bookID int64, in collab.Contact) (store.PlatformInsertContactRow, error) {
	norm, err := collab.NormalizeContact(in)
	if err != nil {
		return store.PlatformInsertContactRow{}, err
	}
	emails, phones, err := encodeContact(norm)
	if err != nil {
		return store.PlatformInsertContactRow{}, err
	}
	row, err := s.Q.PlatformInsertContact(ctx, store.PlatformInsertContactParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID, AddressbookID: bookID,
		DisplayName: norm.DisplayName, GivenName: norm.GivenName, FamilyName: norm.FamilyName,
		Emails: emails, Phones: phones, Note: norm.Note,
	})
	return row, mapWrite(err)
}

// UpdateContact refuses a stale version instead of overwriting.
func (s Service) UpdateContact(ctx context.Context, p Principal, id, version int64, in collab.Contact) (store.PlatformUpdateContactRow, error) {
	norm, err := collab.NormalizeContact(in)
	if err != nil {
		return store.PlatformUpdateContactRow{}, err
	}
	emails, phones, err := encodeContact(norm)
	if err != nil {
		return store.PlatformUpdateContactRow{}, err
	}
	row, err := s.Q.PlatformUpdateContact(ctx, store.PlatformUpdateContactParams{
		DisplayName: norm.DisplayName, GivenName: norm.GivenName, FamilyName: norm.FamilyName,
		Emails: emails, Phones: phones, Note: norm.Note,
		TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id, ExpectedVersion: version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.PlatformUpdateContactRow{}, ErrConflict
	}
	return row, mapWrite(err)
}

// EnsureCalendar returns the live calendar with this name, creating it once.
// timezone is stored on create; an empty value is UTC. Recurrence exceptions
// are not stored: domain RecurrenceID is ignored at persistence.
func (s Service) EnsureCalendar(ctx context.Context, p Principal, name, timezone string) (store.MailboxCalendars, error) {
	name = strings.TrimSpace(name)
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	if name == "" || p.TenantID <= 0 || p.MailboxID <= 0 {
		return store.MailboxCalendars{}, collab.ErrInvalidEvent
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return store.MailboxCalendars{}, collab.ErrInvalidEvent
	}
	row, err := s.Q.PlatformEnsureCalendar(ctx, store.PlatformEnsureCalendarParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Name: name, Timezone: timezone})
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.MailboxCalendars{}, err
	}
	return s.Q.PlatformGetCalendarByName(ctx, store.PlatformGetCalendarByNameParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Name: name})
}

// CreateEvent validates and inserts one event. A blank UID is generated.
// RecurrenceID is not a column and is not persisted.
func (s Service) CreateEvent(ctx context.Context, p Principal, calendarID int64, uid string, in collab.Event) (store.PlatformInsertEventRow, error) {
	norm, err := collab.NormalizeEvent(in)
	if err != nil {
		return store.PlatformInsertEventRow{}, err
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		uid, err = newEventUID()
		if err != nil {
			return store.PlatformInsertEventRow{}, err
		}
	}
	row, err := s.Q.PlatformInsertEvent(ctx, store.PlatformInsertEventParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID, CalendarID: calendarID, Uid: uid,
		Summary: norm.Summary, Description: norm.Description, Location: norm.Location,
		StartsAt: timestamptz(norm.StartsAt), EndsAt: timestamptz(norm.EndsAt),
		AllDay: norm.AllDay, Timezone: norm.Timezone, Rrule: norm.RRule, Status: norm.Status,
		Attendees: attendeeJSON(norm.Attendees, norm.AttendeeStatus),
	})
	return row, mapWrite(err)
}

// UpdateEvent refuses a stale version instead of overwriting.
// RecurrenceID is not a column and is not persisted.
func (s Service) UpdateEvent(ctx context.Context, p Principal, id, version int64, in collab.Event) (store.PlatformUpdateEventRow, error) {
	norm, err := collab.NormalizeEvent(in)
	if err != nil {
		return store.PlatformUpdateEventRow{}, err
	}
	row, err := s.Q.PlatformUpdateEvent(ctx, store.PlatformUpdateEventParams{
		Summary: norm.Summary, Description: norm.Description, Location: norm.Location,
		StartsAt: timestamptz(norm.StartsAt), EndsAt: timestamptz(norm.EndsAt),
		AllDay: norm.AllDay, Timezone: norm.Timezone, Rrule: norm.RRule, Status: norm.Status,
		Attendees: attendeeJSON(norm.Attendees, norm.AttendeeStatus),
		TenantID:  p.TenantID, MailboxID: p.MailboxID, ID: id, ExpectedVersion: version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.PlatformUpdateEventRow{}, ErrConflict
	}
	return row, mapWrite(err)
}

// storedAttendee is the per-attendee JSON shape used once any attendee has
// answered. Rows without answers keep the legacy plain-string shape.
type storedAttendee struct {
	Email    string `json:"email"`
	PartStat string `json:"partstat"`
}

func attendeeJSON(attendees []string, status map[string]string) []byte {
	answered := false
	for _, state := range status {
		if collab.ValidPartStat(state) && state != collab.PartStatNeedsAction {
			answered = true
			break
		}
	}
	if !answered {
		raw, err := json.Marshal(attendees)
		if err != nil || attendees == nil {
			return []byte("[]")
		}
		return raw
	}
	out := make([]storedAttendee, 0, len(attendees))
	for _, email := range attendees {
		state := collab.PartStatNeedsAction
		if stored, ok := status[email]; ok && collab.ValidPartStat(stored) {
			state = stored
		}
		out = append(out, storedAttendee{Email: email, PartStat: state})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return []byte("[]")
	}
	return raw
}

// decodeAttendees accepts both stored shapes: legacy ["a@x"] strings and
// answered [{"email":"a@x","partstat":"accepted"}] objects.
func decodeAttendees(raw []byte) ([]string, map[string]string) {
	var plain []string
	if len(raw) == 0 || json.Unmarshal(raw, &plain) == nil && plain != nil {
		return plain, nil
	}
	var objects []storedAttendee
	if json.Unmarshal(raw, &objects) != nil || objects == nil {
		return nil, nil
	}
	emails := make([]string, 0, len(objects))
	status := make(map[string]string, len(objects))
	for _, one := range objects {
		emails = append(emails, one.Email)
		if collab.ValidPartStat(one.PartStat) {
			status[one.Email] = one.PartStat
		}
	}
	return emails, status
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func newEventUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]) + "@urmail", nil
}

func encodeContact(in collab.Contact) ([]byte, []byte, error) {
	emails, err := json.Marshal(in.Emails)
	if err != nil {
		return nil, nil, err
	}
	phones, err := json.Marshal(in.Phones)
	if err != nil {
		return nil, nil, err
	}
	return emails, phones, nil
}

func mapWrite(err error) error {
	if IsConflict(err) {
		return ErrConflict
	}
	return err
}

// IsConflict reports whether the condition holds.
func IsConflict(err error) bool {
	var pg *pgconn.PgError
	return errors.Is(err, ErrConflict) || (errors.As(err, &pg) && pg.Code == "23505")
}

// Timestamp keeps event bounds explicit at the API edge.
func Timestamp(t pgtype.Timestamptz) pgtype.Timestamptz { return t }

// GroupMemberStore implements the corresponding domain rule.
type GroupMemberStore interface {
	PlatformInsertContactGroupMember(context.Context, store.PlatformInsertContactGroupMemberParams) (int64, error)
}

// AddGroupMember implements the corresponding domain rule.
func AddGroupMember(ctx context.Context, q GroupMemberStore, p Principal, groupID, contactID int64) error {
	if q == nil || p.TenantID <= 0 || p.MailboxID <= 0 || groupID <= 0 || contactID <= 0 {
		return collab.ErrGroupScope
	}
	count, err := q.PlatformInsertContactGroupMember(ctx, store.PlatformInsertContactGroupMemberParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: groupID, ID_2: contactID})
	if err != nil {
		return err
	}
	if count != 1 {
		return collab.ErrGroupScope
	}
	return nil
}

// eventFromRow rebuilds the domain event from the stored row.
func eventFromRow(row store.MailboxEvents) (collab.Event, []string, error) {
	emails, status := decodeAttendees(row.Attendees)
	event, err := collab.NormalizeEvent(collab.Event{
		Summary: row.Summary, Description: row.Description, Location: row.Location,
		StartsAt: row.StartsAt.Time, EndsAt: row.EndsAt.Time, AllDay: row.AllDay,
		Timezone: row.Timezone, RRule: row.Rrule, Status: row.Status,
		Attendees: emails, AttendeeStatus: status,
	})
	return event, emails, err
}

// ApplyReply records one attendee's own PARTSTAT answer on a stored event.
// The reply UID must name this event and the replier must be one of its
// stored attendees; anything else is refused without a write.
func (s Service) ApplyReply(ctx context.Context, p Principal, id int64, reply collab.Reply) (store.PlatformUpdateEventRow, error) {
	if p.TenantID <= 0 || p.MailboxID <= 0 || id <= 0 {
		return store.PlatformUpdateEventRow{}, collab.ErrInvalidEvent
	}
	row, err := s.Q.PlatformGetEvent(ctx, store.PlatformGetEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if err != nil {
		return store.PlatformUpdateEventRow{}, err
	}
	if reply.UID != row.Uid {
		return store.PlatformUpdateEventRow{}, collab.ErrInvalidEvent
	}
	event, emails, err := eventFromRow(row)
	if err != nil {
		return store.PlatformUpdateEventRow{}, err
	}
	next, err := collab.ApplyReply(event, reply)
	if err != nil {
		return store.PlatformUpdateEventRow{}, err
	}
	updated, err := s.Q.PlatformUpdateEvent(ctx, store.PlatformUpdateEventParams{
		Summary: next.Summary, Description: next.Description, Location: next.Location,
		StartsAt: timestamptz(next.StartsAt), EndsAt: timestamptz(next.EndsAt),
		AllDay: next.AllDay, Timezone: next.Timezone, Rrule: next.RRule, Status: next.Status,
		Attendees: attendeeJSON(emails, next.AttendeeStatus),
		TenantID:  p.TenantID, MailboxID: p.MailboxID, ID: id, ExpectedVersion: row.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.PlatformUpdateEventRow{}, ErrConflict
	}
	return updated, mapWrite(err)
}

// DecodeAttendees exposes the stored attendee decode for API views. It
// accepts both stored shapes (legacy strings and answered objects).
func DecodeAttendees(raw []byte) ([]string, map[string]string) {
	return decodeAttendees(raw)
}
