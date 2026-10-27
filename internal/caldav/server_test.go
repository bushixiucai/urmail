package caldav

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

/* fakeStore keeps rows in memory and mirrors the change log shape. */
type fakeStore struct {
	byUID   map[string]store.MailboxEvents
	byID    map[int64]store.MailboxEvents
	changes []store.MailboxChanges
	nextID  int64
	nextLog int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{byUID: map[string]store.MailboxEvents{}, byID: map[int64]store.MailboxEvents{}}
}

func tz(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t.UTC(), Valid: true} }

func attendeeJSONFor(in []string) []byte {
	raw, err := json.Marshal(in)
	if err != nil || in == nil {
		return []byte("[]")
	}
	return raw
}

func (f *fakeStore) EnsureCalendar(_ context.Context, _ appcollab.Principal, _, _ string) (store.MailboxCalendars, error) {
	return store.MailboxCalendars{ID: 7}, nil
}

func (f *fakeStore) CreateEvent(_ context.Context, p appcollab.Principal, calendarID int64, uid string, in collab.Event) (store.PlatformInsertEventRow, error) {
	f.nextID++
	row := store.MailboxEvents{ID: f.nextID, TenantID: p.TenantID, MailboxID: p.MailboxID, CalendarID: calendarID, Uid: uid, Version: 1,
		Summary: in.Summary, Description: in.Description, Location: in.Location, StartsAt: tz(in.StartsAt), EndsAt: tz(in.EndsAt),
		AllDay: in.AllDay, Timezone: in.Timezone, Rrule: in.RRule, Status: in.Status, Attendees: attendeeJSONFor(in.Attendees)}
	f.byUID[uid] = row
	f.byID[row.ID] = row
	f.log(row.ID, row.Version, false)
	return store.PlatformInsertEventRow{ID: row.ID, Version: row.Version, Uid: uid}, nil
}

func (f *fakeStore) UpdateEvent(_ context.Context, _ appcollab.Principal, id, version int64, in collab.Event) (store.PlatformUpdateEventRow, error) {
	row, ok := f.byID[id]
	if !ok || row.DeletedAt.Valid || row.Version != version {
		return store.PlatformUpdateEventRow{}, appcollab.ErrConflict
	}
	row.Version++
	row.Summary, row.Description, row.Location = in.Summary, in.Description, in.Location
	row.StartsAt, row.EndsAt, row.AllDay, row.Timezone = tz(in.StartsAt), tz(in.EndsAt), in.AllDay, in.Timezone
	row.Rrule, row.Status = in.RRule, in.Status
	f.byID[id], f.byUID[row.Uid] = row, row
	f.log(row.ID, row.Version, false)
	return store.PlatformUpdateEventRow{ID: row.ID, Version: row.Version, Uid: row.Uid}, nil
}

func (f *fakeStore) GetEventByUID(_ context.Context, _ appcollab.Principal, uid string) (store.MailboxEvents, error) {
	row, ok := f.byUID[uid]
	if !ok || row.DeletedAt.Valid {
		return store.MailboxEvents{}, appcollab.ErrNotFound
	}
	return row, nil
}

func (f *fakeStore) GetEventAny(_ context.Context, _ appcollab.Principal, id int64) (store.MailboxEvents, error) {
	row, ok := f.byID[id]
	if !ok {
		return store.MailboxEvents{}, appcollab.ErrNotFound
	}
	return row, nil
}

func (f *fakeStore) DeleteEvent(_ context.Context, _ appcollab.Principal, id, version int64) (store.PlatformDeleteEventRow, error) {
	row, ok := f.byID[id]
	if !ok || row.DeletedAt.Valid || row.Version != version {
		return store.PlatformDeleteEventRow{}, appcollab.ErrConflict
	}
	row.DeletedAt, row.Status, row.Version = tz(time.Now()), `cancelled`, row.Version+1 //nolint:misspell // RFC 5545 STATUS:CANCELLED value
	f.byID[id], f.byUID[row.Uid] = row, row
	f.log(row.ID, row.Version, true)
	return store.PlatformDeleteEventRow{ID: row.ID, Version: row.Version}, nil
}

func (f *fakeStore) ListEvents(_ context.Context, _ appcollab.Principal, _, _ time.Time, _ int32) ([]store.MailboxEvents, error) {
	var rows []store.MailboxEvents
	for _, row := range f.byID {
		if !row.DeletedAt.Valid {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func (f *fakeStore) ListEventChanges(_ context.Context, _ appcollab.Principal, after int64, _ int32) ([]store.MailboxChanges, error) {
	var out []store.MailboxChanges
	for _, row := range f.changes {
		if row.ID > after {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) log(id, version int64, deleted bool) {
	f.nextLog++
	f.changes = append(f.changes, store.MailboxChanges{ID: f.nextLog, ResourceType: `event`, ResourceID: id, Version: version, Deleted: deleted})
}

/* do sends one request to the server with an optional session. */
func do(t *testing.T, server *Server, method, target, body string, session *Session, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if session != nil {
		req = req.WithContext(WithSession(req.Context(), *session))
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	return w
}

var testSession = &Session{TenantID: 1, MailboxID: 2}

const sampleICS = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
DTSTART:20260301T090000Z
DTEND:20260301T100000Z
SUMMARY:Standup
RRULE:FREQ=DAILY;COUNT=5
EXDATE:20260303T090000Z
ATTENDEE:mailto:ada@example.test
END:VEVENT
END:VCALENDAR`

func TestAnonymousIsUnauthorized(t *testing.T) {
	w := do(t, &Server{Store: newFakeStore()}, http.MethodGet, "/caldav/calendar/", "", nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
}

func TestWellKnownRedirects(t *testing.T) {
	w := do(t, &Server{Store: newFakeStore()}, http.MethodGet, "/.well-known/caldav", "", testSession, nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/caldav/" {
		t.Fatal(w.Code, w.Header().Get(`Location`))
	}
}

func TestOptionsAdvertisesCalendarAccess(t *testing.T) {
	w := do(t, &Server{Store: newFakeStore()}, http.MethodOptions, "/caldav/calendar/", "", testSession, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("DAV"), `calendar-access`) {
		t.Fatal(w.Code, w.Header().Get(`DAV`))
	}
}

func TestPropfindPrincipalAndCalendar(t *testing.T) {
	server := &Server{Store: newFakeStore()}
	w := do(t, server, `PROPFIND`, "/caldav/", "", testSession, nil)
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), `calendar-home-set`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = do(t, server, `PROPFIND`, "/caldav/calendar/", "", testSession, map[string]string{"Depth": "1"})
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), `supported-calendar-component-set`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPutGetRoundTripKeepsRecurrence(t *testing.T) {
	server := &Server{Store: newFakeStore()}
	w := do(t, server, http.MethodPut, "/caldav/calendar/rec-1@urmail.ics", sampleICS, testSession, nil)
	if w.Code != http.StatusCreated || w.Header().Get("ETag") != quote+"1"+quote {
		t.Fatal(w.Code, w.Header().Get(`ETag`))
	}
	w = do(t, server, http.MethodGet, "/caldav/calendar/rec-1@urmail.ics", "", testSession, nil)
	body := w.Body.String()
	for _, want := range []string{`RRULE:FREQ=DAILY;COUNT=5`, `EXDATE:20260303T090000Z`, `UID:rec-1@urmail`, `ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:ada@example.test`} {
		if !strings.Contains(body, want) {
			t.Fatal(`missing`, want, body)
		}
	}
}

func TestConditionalPutAndDelete(t *testing.T) {
	server := &Server{Store: newFakeStore()}
	do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, nil)
	if w := do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, map[string]string{"If-None-Match": "*"}); w.Code != http.StatusPreconditionFailed {
		t.Fatal(`if-none-match`, w.Code)
	}
	if w := do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, map[string]string{"If-Match": quote + "9" + quote}); w.Code != http.StatusPreconditionFailed {
		t.Fatal(`stale if-match`, w.Code)
	}
	if w := do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, map[string]string{"If-Match": quote + "1" + quote}); w.Code != http.StatusNoContent || w.Header().Get("ETag") != quote+"2"+quote {
		t.Fatal(`update`, w.Code, w.Header().Get(`ETag`))
	}
	if w := do(t, server, http.MethodDelete, "/caldav/calendar/e1.ics", "", testSession, map[string]string{"If-Match": quote + "1" + quote}); w.Code != http.StatusPreconditionFailed {
		t.Fatal(`stale delete`, w.Code)
	}
	if w := do(t, server, http.MethodDelete, "/caldav/calendar/e1.ics", "", testSession, nil); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
}
func syncBody(token string) string {
	return `<C:sync-collection xmlns:C=` + quote + `urn:ietf:params:xml:ns:caldav` + quote + ` xmlns:D=` + quote + `DAV:` + quote + `><D:sync-token>` + token + `</D:sync-token><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></C:sync-collection>`
}

func TestSyncCollectionTombstonesWithoutResurrection(t *testing.T) {
	store0 := newFakeStore()
	server := &Server{Store: store0}
	do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, nil)
	do(t, server, http.MethodDelete, "/caldav/calendar/e1.ics", "", testSession, nil)
	w := do(t, server, `REPORT`, "/caldav/calendar/", syncBody(""), testSession, nil)
	if w.Code != http.StatusMultiStatus {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `404 Not Found`) {
		t.Fatal(`tombstone missing`, w.Body.String())
	}
	do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, nil)
	w = do(t, server, `REPORT`, "/caldav/calendar/", syncBody(""), testSession, nil)
	body := w.Body.String()
	if strings.Count(body, `e1.ics`) != 1 || !strings.Contains(body, `200 OK`) {
		t.Fatal(`resurrected or duplicated`, body)
	}
}

func TestSyncCollectionTokenAdvances(t *testing.T) {
	server := &Server{Store: newFakeStore()}
	do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, nil)
	w := do(t, server, `REPORT`, "/caldav/calendar/", syncBody(""), testSession, nil)
	body := w.Body.String()
	marker := `<D:sync-token>`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal(body)
	}
	tail := body[i+len(marker):]
	j := strings.Index(tail, `<`)
	if j < 0 {
		t.Fatal(body)
	}
	token := tail[:j]
	if token == "0" {
		t.Fatal(`token did not advance`)
	}
	w = do(t, server, `REPORT`, "/caldav/calendar/", syncBody(token), testSession, nil)
	if strings.Contains(w.Body.String(), `<D:response>`) {
		t.Fatal(`stale token returned entries`, w.Body.String())
	}
}

func TestCalendarQueryTimeRangeFilters(t *testing.T) {
	server := &Server{Store: newFakeStore()}
	do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, nil)
	body := `<C:calendar-query xmlns:C=` + quote + `urn:ietf:params:xml:ns:caldav` + quote + ` xmlns:D=` + quote + `DAV:` + quote + `><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name=` + quote + `VCALENDAR` + quote + `><C:comp-filter name=` + quote + `VEVENT` + quote + `><C:time-range start=` + quote + `20260301T000000Z` + quote + ` end=` + quote + `20260302T000000Z` + quote + `/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`
	w := do(t, server, `REPORT`, "/caldav/calendar/", body, testSession, nil)
	if !strings.Contains(w.Body.String(), `200 OK`) {
		t.Fatal(w.Body.String())
	}
	body = strings.Replace(body, `20260302T000000Z`, `20260102T000000Z`, 1)
	w = do(t, server, `REPORT`, "/caldav/calendar/", body, testSession, nil)
	if strings.Contains(w.Body.String(), `200 OK`) {
		t.Fatal(`window before event should not match`, w.Body.String())
	}
}

func TestMultigetReportsMissingAs404(t *testing.T) {
	server := &Server{Store: newFakeStore()}
	do(t, server, http.MethodPut, "/caldav/calendar/e1.ics", sampleICS, testSession, nil)
	body := `<C:calendar-multiget xmlns:C=` + quote + `urn:ietf:params:xml:ns:caldav` + quote + ` xmlns:D=` + quote + `DAV:` + quote + `><D:href>/caldav/calendar/e1.ics</D:href><D:href>/caldav/calendar/nope.ics</D:href></C:calendar-multiget>`
	w := do(t, server, `REPORT`, "/caldav/calendar/", body, testSession, nil)
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), `200 OK`) || !strings.Contains(w.Body.String(), `404 Not Found`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestUnsupportedReportIsRejected(t *testing.T) {
	server := &Server{Store: newFakeStore()}
	body := `<D:expand-property xmlns:D=` + quote + `DAV:` + quote + `/>`
	w := do(t, server, `REPORT`, "/caldav/calendar/", body, testSession, nil)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestUnknownPathIs404(t *testing.T) {
	w := do(t, &Server{Store: newFakeStore()}, http.MethodGet, "/caldav/other/", "", testSession, nil)
	if w.Code != http.StatusNotFound {
		t.Fatal(w.Code)
	}
}
