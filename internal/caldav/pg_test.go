package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/handlers"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

/* pgFixture owns one migrated database, two mailboxes and a server. */
type pgFixture struct {
	tx       pgx.Tx
	tenant   int64
	mailboxA int64
	mailboxB int64
	server   *Server
	sessionA *Session
	sessionB *Session
	web      *gin.Engine
}

const recICS = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
DTSTART:20260301T090000Z
DTEND:20260301T100000Z
SUMMARY:Standup
RRULE:FREQ=DAILY;COUNT=3
EXDATE:20260302T090000Z
ATTENDEE:MAILTO:ada@example.test
END:VEVENT
END:VCALENDAR`

func newPGFixture(t *testing.T) *pgFixture {
	t.Helper()
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	tx, err := platform.PlatformTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	var brand, tenant, domain, mailboxA, mailboxB int64
	if err = tx.QueryRow(ctx, `INSERT INTO brands(slug,name) VALUES('caldav','Cal') RETURNING id`).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'C','caldav-c','active') RETURNING id`, brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES($1,'cal.test','active','tok',now()) RETURNING id`, tenant).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'a','a@cal.test','x','active') RETURNING id`, tenant, domain).Scan(&mailboxA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES($1,$2,'b','b@cal.test','x','active') RETURNING id`, tenant, domain).Scan(&mailboxB); err != nil {
		t.Fatal(err)
	}
	queries := store.New(tx)
	fixture := &pgFixture{
		tx:       tx,
		tenant:   tenant,
		mailboxA: mailboxA,
		mailboxB: mailboxB,
		server:   NewServer(queries),
		sessionA: &Session{TenantID: tenant, MailboxID: mailboxA},
		sessionB: &Session{TenantID: tenant, MailboxID: mailboxB},
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := handlers.MailboxCalendarHandler{Service: appcollab.Service{Q: queries}}
	sessionFor := func(mailbox int64) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: tenant, MailboxID: mailbox})
			c.Next()
		}
	}
	router.GET(`/calendars/events`, sessionFor(mailboxA), handler.ListEvents)
	router.POST(`/calendars/events`, sessionFor(mailboxA), handler.CreateEvent)
	router.PUT(`/calendars/events/:id`, sessionFor(mailboxA), handler.UpdateEvent)
	router.GET(`/calendars/events/:id/ics`, sessionFor(mailboxA), handler.ExportEvent)
	fixture.web = router
	return fixture
}

/* dav sends one CalDAV request for the given session. */
func dav(t *testing.T, f *pgFixture, session *Session, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if session != nil {
		req = req.WithContext(WithSession(req.Context(), *session))
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, req)
	return w
}

/* web sends one request to the web API surface of mailbox A. */
func web(t *testing.T, f *pgFixture, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	req.Header.Set(`Content-Type`, `application/json`)
	w := httptest.NewRecorder()
	f.web.ServeHTTP(w, req)
	return w
}

const singleICS = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
DTSTART:20260601T120000Z
DTEND:20260601T130000Z
SUMMARY:One
END:VEVENT
END:VCALENDAR`

func TestCalDAVPUTVisibleViaWebAPI(t *testing.T) {
	f := newPGFixture(t)
	w := dav(t, f, f.sessionA, http.MethodPut, "/caldav/calendar/rec-1@urmail.ics", recICS, nil)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	w = web(t, f, http.MethodGet, "/calendars/events?start=2026-02-01T00:00:00Z&end=2026-04-01T00:00:00Z", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `rec-1@urmail`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `2026-03-01T09:00:00Z`) || !strings.Contains(w.Body.String(), `2026-03-03T09:00:00Z`) {
		t.Fatal(`expansion wrong`, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `2026-03-02T09:00:00Z`) {
		t.Fatal(`EXDATE instance leaked`, w.Body.String())
	}
	w = web(t, f, http.MethodGet, "/calendars/events/1/ics", "")
	body := w.Body.String()
	// Attendees are stored with their iTIP PARTSTAT (WEBMAIL-010); the export
	// must reflect the stored state, defaulting to NEEDS-ACTION.
	for _, want := range []string{`RRULE:FREQ=DAILY;COUNT=3`, `EXDATE:20260302T090000Z`, `ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:ada@example.test`} {
		if !strings.Contains(body, want) {
			t.Fatal(`missing`, want, body)
		}
	}
}

func TestWebAPICreateVisibleViaCalDAV(t *testing.T) {
	f := newPGFixture(t)
	body := `{"uid":"web-1@urmail","summary":"Web","starts_at":"2026-05-01T10:00:00Z","ends_at":"2026-05-01T11:00:00Z"}`
	w := web(t, f, http.MethodPost, "/calendars/events", body)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	w = dav(t, f, f.sessionA, http.MethodGet, "/caldav/calendar/web-1@urmail.ics", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `SUMMARY:Web`) || !strings.Contains(w.Body.String(), `UID:web-1@urmail`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSyncCollectionTombstonesReal(t *testing.T) {
	f := newPGFixture(t)
	w := dav(t, f, f.sessionA, http.MethodPut, "/caldav/calendar/e1.ics", singleICS, nil)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	if w = dav(t, f, f.sessionA, http.MethodDelete, "/caldav/calendar/e1.ics", "", nil); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	w = dav(t, f, f.sessionA, `REPORT`, "/caldav/calendar/", syncBody(""), nil)
	if !strings.Contains(w.Body.String(), `404 Not Found`) {
		t.Fatal(`tombstone missing`, w.Body.String())
	}
	oldToken := between(w.Body.String(), `<D:sync-token>`, `</D:sync-token>`)
	dav(t, f, f.sessionA, http.MethodPut, "/caldav/calendar/e1.ics", singleICS, nil)
	w = dav(t, f, f.sessionA, `REPORT`, "/caldav/calendar/", syncBody(oldToken), nil)
	body := w.Body.String()
	if strings.Count(body, `e1.ics`) != 1 || !strings.Contains(body, `200 OK`) || strings.Contains(body, `404 Not Found`) {
		t.Fatal(`resurrected or duplicated`, body)
	}
	newToken := between(body, `<D:sync-token>`, `</D:sync-token>`)
	if newToken == oldToken {
		t.Fatal(`token did not advance`)
	}
}

func between(s, open, closing string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, closing)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func TestIfMatchConflict412(t *testing.T) {
	f := newPGFixture(t)
	if w := dav(t, f, f.sessionA, http.MethodPut, "/caldav/calendar/e2.ics", singleICS, nil); w.Header().Get(`ETag`) != quote+`1`+quote {
		t.Fatal(w.Code, w.Header().Get(`ETag`))
	}
	if w := dav(t, f, f.sessionA, http.MethodPut, "/caldav/calendar/e2.ics", singleICS, map[string]string{"If-Match": quote + `1` + quote}); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := dav(t, f, f.sessionA, http.MethodPut, "/caldav/calendar/e2.ics", singleICS, map[string]string{"If-Match": quote + `1` + quote}); w.Code != http.StatusPreconditionFailed {
		t.Fatal(`stale if-match`, w.Code)
	}
	if w := dav(t, f, f.sessionA, http.MethodDelete, "/caldav/calendar/e2.ics", "", map[string]string{"If-Match": quote + `1` + quote}); w.Code != http.StatusPreconditionFailed {
		t.Fatal(`stale delete`, w.Code)
	}
	if w := web(t, f, http.MethodPut, "/calendars/events/1", `{"version":1,"summary":"Clash","starts_at":"2026-06-01T12:00:00Z","ends_at":"2026-06-01T13:00:00Z"}`); w.Code != http.StatusConflict {
		t.Fatal(`web optimistic conflict`, w.Code, w.Body.String())
	}
}

func TestCrossMailboxAndAnonymousRejected(t *testing.T) {
	f := newPGFixture(t)
	if w := dav(t, f, f.sessionA, http.MethodPut, "/caldav/calendar/e3.ics", singleICS, nil); w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	if w := dav(t, f, f.sessionB, http.MethodGet, "/caldav/calendar/e3.ics", "", nil); w.Code != http.StatusNotFound {
		t.Fatal(`cross mailbox read`, w.Code)
	}
	if w := dav(t, f, nil, http.MethodGet, "/caldav/calendar/e3.ics", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatal(`anonymous`, w.Code)
	}
	if w := dav(t, f, nil, `PROPFIND`, "/caldav/calendar/", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatal(`anonymous propfind`, w.Code)
	}
	if w := dav(t, f, f.sessionB, http.MethodPut, "/caldav/calendar/e3.ics", singleICS, nil); w.Code != http.StatusCreated {
		t.Fatal(`separate mailbox create`, w.Code)
	}
	if w := dav(t, f, f.sessionA, http.MethodGet, "/caldav/calendar/e3.ics", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `ETAG-DUMMY`) && w.Header().Get(`ETag`) != quote+`1`+quote {
		t.Fatal(`A row disturbed by B`, w.Code, w.Header().Get(`ETag`))
	}
}
