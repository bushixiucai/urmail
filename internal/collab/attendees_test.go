package collab

import (
	"context"
	"testing"
	"time"

	domain "github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

type eventStore struct {
	inserted store.PlatformInsertEventParams
	updated  store.PlatformUpdateEventParams
}

func (eventStore) PlatformEnsureAddressbook(context.Context, store.PlatformEnsureAddressbookParams) (store.MailboxAddressbooks, error) {
	return store.MailboxAddressbooks{}, nil
}
func (eventStore) PlatformGetAddressbookByName(context.Context, store.PlatformGetAddressbookByNameParams) (store.MailboxAddressbooks, error) {
	return store.MailboxAddressbooks{}, nil
}
func (eventStore) PlatformInsertContact(context.Context, store.PlatformInsertContactParams) (store.PlatformInsertContactRow, error) {
	return store.PlatformInsertContactRow{}, nil
}
func (eventStore) PlatformUpdateContact(context.Context, store.PlatformUpdateContactParams) (store.PlatformUpdateContactRow, error) {
	return store.PlatformUpdateContactRow{}, nil
}
func (eventStore) PlatformGetContact(context.Context, store.PlatformGetContactParams) (store.MailboxContacts, error) {
	return store.MailboxContacts{}, nil
}
func (eventStore) PlatformListContacts(context.Context, store.PlatformListContactsParams) ([]store.MailboxContacts, error) {
	return nil, nil
}
func (eventStore) PlatformSearchContacts(context.Context, store.PlatformSearchContactsParams) ([]store.MailboxContacts, error) {
	return nil, nil
}
func (eventStore) PlatformDeleteContact(context.Context, store.PlatformDeleteContactParams) (store.PlatformDeleteContactRow, error) {
	return store.PlatformDeleteContactRow{}, nil
}
func (eventStore) PlatformEnsureCalendar(context.Context, store.PlatformEnsureCalendarParams) (store.MailboxCalendars, error) {
	return store.MailboxCalendars{}, nil
}
func (eventStore) PlatformGetCalendarByName(context.Context, store.PlatformGetCalendarByNameParams) (store.MailboxCalendars, error) {
	return store.MailboxCalendars{}, nil
}
func (s *eventStore) PlatformInsertEvent(_ context.Context, arg store.PlatformInsertEventParams) (store.PlatformInsertEventRow, error) {
	s.inserted = arg
	return store.PlatformInsertEventRow{ID: 9}, nil
}
func (s *eventStore) PlatformUpdateEvent(_ context.Context, arg store.PlatformUpdateEventParams) (store.PlatformUpdateEventRow, error) {
	s.updated = arg
	return store.PlatformUpdateEventRow{ID: arg.ID}, nil
}
func (eventStore) PlatformGetEvent(context.Context, store.PlatformGetEventParams) (store.MailboxEvents, error) {
	return store.MailboxEvents{}, nil
}
func (eventStore) PlatformListEvents(context.Context, store.PlatformListEventsParams) ([]store.MailboxEvents, error) {
	return nil, nil
}
func (eventStore) PlatformDeleteEvent(context.Context, store.PlatformDeleteEventParams) (store.PlatformDeleteEventRow, error) {
	return store.PlatformDeleteEventRow{}, nil
}
func (eventStore) PlatformListChanges(context.Context, store.PlatformListChangesParams) ([]store.MailboxChanges, error) {
	return nil, nil
}

func TestServiceStoresAttendeesOnOriginalEvent(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	event := domain.Event{Summary: "A", StartsAt: start, EndsAt: start.Add(time.Hour), Attendees: []string{"Ada@Example.test"}}
	q := &eventStore{}
	row, err := (Service{Q: q}).CreateEvent(context.Background(), Principal{TenantID: 1, MailboxID: 2}, 3, "uid-1", event)
	if err != nil || row.ID != 9 || string(q.inserted.Attendees) != "[\"ada@example.test\"]" {
		t.Fatalf("%+v %s %v", row, q.inserted.Attendees, err)
	}
	updated, err := (Service{Q: q}).UpdateEvent(context.Background(), Principal{TenantID: 1, MailboxID: 2}, 9, 1, event)
	if err != nil || updated.ID != 9 || string(q.updated.Attendees) != "[\"ada@example.test\"]" {
		t.Fatalf("%+v %s %v", updated, q.updated.Attendees, err)
	}
}
