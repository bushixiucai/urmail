// Package caldav adapts the mailbox calendar foundation to CalDAV
// (RFC 4791) with RFC 6578 sync-collection. PostgreSQL stays the only
// copy of event data; iCalendar bodies are rendered from columns on
// every request and never stored. Every request is bound to the mailbox
// session principal; without one nothing is visible.
package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// defaultCalendarName matches the web API default calendar.
const defaultCalendarName = "Calendar"

// maxBodyBytes bounds one iCalendar PUT.
const maxBodyBytes = 200000

// syncBatch caps the change rows read per sync-collection report.
const syncBatch = 2000

// syncIDPrefix namespaces sync tokens (the URN of DAV sync-token values).
const syncIDPrefix = `urn:urmail:sync:`

// resourcePattern is the only resource shape inside the calendar.
var resourcePattern = regexp.MustCompile(`^/caldav/calendar/([A-Za-z0-9@._-]+)[.]ics$`)

// EventStore is the calendar surface this package needs. It is backed by
// the shared collaboration store, never by a second data source.
type EventStore interface {
	EnsureCalendar(ctx context.Context, p appcollab.Principal, name, timezone string) (store.MailboxCalendars, error)
	CreateEvent(ctx context.Context, p appcollab.Principal, calendarID int64, uid string, in collab.Event) (store.PlatformInsertEventRow, error)
	UpdateEvent(ctx context.Context, p appcollab.Principal, id, version int64, in collab.Event) (store.PlatformUpdateEventRow, error)
	GetEventByUID(ctx context.Context, p appcollab.Principal, uid string) (store.MailboxEvents, error)
	GetEventAny(ctx context.Context, p appcollab.Principal, id int64) (store.MailboxEvents, error)
	DeleteEvent(ctx context.Context, p appcollab.Principal, id, version int64) (store.PlatformDeleteEventRow, error)
	ListEvents(ctx context.Context, p appcollab.Principal, start, end time.Time, limit int32) ([]store.MailboxEvents, error)
	ListEventChanges(ctx context.Context, p appcollab.Principal, after int64, limit int32) ([]store.MailboxChanges, error)
}

// QueryStore is the shared collaboration store plus the UID and
// tombstone lookups this package adds to the shared SQL file.
type QueryStore interface {
	appcollab.Store
	PlatformGetEventByUID(ctx context.Context, arg store.PlatformGetEventByUIDParams) (store.MailboxEvents, error)
	PlatformGetEventAny(ctx context.Context, arg store.PlatformGetEventAnyParams) (store.MailboxEvents, error)
}

// ServiceStore adapts the shared collaboration store to EventStore.
type ServiceStore struct{ Q QueryStore }

// EnsureCalendar creates the mailbox default calendar once.
func (s ServiceStore) EnsureCalendar(ctx context.Context, p appcollab.Principal, name, timezone string) (store.MailboxCalendars, error) {
	return appcollab.Service{Q: s.Q}.EnsureCalendar(ctx, p, name, timezone)
}

// CreateEvent inserts one event and its change log row.
func (s ServiceStore) CreateEvent(ctx context.Context, p appcollab.Principal, calendarID int64, uid string, in collab.Event) (store.PlatformInsertEventRow, error) {
	return appcollab.Service{Q: s.Q}.CreateEvent(ctx, p, calendarID, uid, in)
}

// UpdateEvent refuses a stale version.
func (s ServiceStore) UpdateEvent(ctx context.Context, p appcollab.Principal, id, version int64, in collab.Event) (store.PlatformUpdateEventRow, error) {
	return appcollab.Service{Q: s.Q}.UpdateEvent(ctx, p, id, version, in)
}

// GetEventByUID returns one live event of this mailbox by UID.
func (s ServiceStore) GetEventByUID(ctx context.Context, p appcollab.Principal, uid string) (store.MailboxEvents, error) {
	return s.Q.PlatformGetEventByUID(ctx, store.PlatformGetEventByUIDParams{TenantID: p.TenantID, MailboxID: p.MailboxID, Uid: uid})
}

// GetEventAny returns one event row including tombstones.
func (s ServiceStore) GetEventAny(ctx context.Context, p appcollab.Principal, id int64) (store.MailboxEvents, error) {
	return s.Q.PlatformGetEventAny(ctx, store.PlatformGetEventAnyParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
}

// DeleteEvent tombstones one event at the expected version.
func (s ServiceStore) DeleteEvent(ctx context.Context, p appcollab.Principal, id, version int64) (store.PlatformDeleteEventRow, error) {
	return s.Q.PlatformDeleteEvent(ctx, store.PlatformDeleteEventParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id, Version: version})
}

// ListEvents returns live events in a window; callers use the full range
// when the calendar contents are needed regardless of dates.
func (s ServiceStore) ListEvents(ctx context.Context, p appcollab.Principal, start, end time.Time, limit int32) ([]store.MailboxEvents, error) {
	return s.Q.PlatformListEvents(ctx, store.PlatformListEventsParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID,
		RangeStart: appcollab.Timestamp(pgTime(start)), RangeEnd: appcollab.Timestamp(pgTime(end)), PageLimit: limit,
	})
}

// ListEventChanges streams event change rows after a change id.
func (s ServiceStore) ListEventChanges(ctx context.Context, p appcollab.Principal, after int64, limit int32) ([]store.MailboxChanges, error) {
	rows, err := s.Q.PlatformListChanges(ctx, store.PlatformListChangesParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: after, PageLimit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]store.MailboxChanges, 0, len(rows))
	for _, row := range rows {
		if row.ResourceType == "event" {
			out = append(out, row)
		}
	}
	return out, nil
}

// Server serves CalDAV for exactly one authenticated mailbox principal.
type Server struct{ Store EventStore }

// NewServer builds the CalDAV adapter over the collaboration store.
func NewServer(q QueryStore) *Server { return &Server{Store: ServiceStore{Q: q}} }

// Mount wires the server into a gin engine with mailbox-session auth. The
// principal travels through the request context only.
func Mount(engine gin.IRouter, auth gin.HandlerFunc, q QueryStore) {
	server := NewServer(q)
	serve := func(c *gin.Context) {
		if session, ok := middleware.GetMailboxSession(c); ok {
			ctx := WithSession(c.Request.Context(), Session{TenantID: session.TenantID, MailboxID: session.MailboxID})
			c.Request = c.Request.WithContext(ctx)
		}
		server.ServeHTTP(c.Writer, c.Request)
	}
	engine.Any("/.well-known/caldav", auth, serve)
	engine.Any("/caldav/*path", auth, serve)
}

// Session is the authenticated mailbox principal.
type Session struct{ TenantID, MailboxID int64 }

type sessionKey struct{}

// WithSession stores the principal in the request context.
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func (s *Server) principal(r *http.Request) (appcollab.Principal, bool) {
	val, ok := r.Context().Value(sessionKey{}).(Session)
	if !ok || s.Store == nil || val.TenantID <= 0 || val.MailboxID <= 0 {
		return appcollab.Principal{}, false
	}
	return appcollab.Principal{TenantID: val.TenantID, MailboxID: val.MailboxID}, true
}

// ServeHTTP dispatches one CalDAV request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("DAV", "1, 2, calendar-access")
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT")
		w.WriteHeader(http.StatusOK)
		return
	}
	p, ok := s.principal(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="caldav"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := r.URL.Path
	if path == "/.well-known/caldav" {
		w.Header().Set("Location", "/caldav/")
		w.WriteHeader(http.StatusFound)
		return
	}
	switch {
	case path == "/caldav" || path == "/caldav/":
		s.servePrincipal(w, r)
	case path == "/caldav/calendar" || path == "/caldav/calendar/":
		s.serveCalendar(w, r, p)
	case resourcePattern.MatchString(path):
		s.serveResource(w, r, p, resourcePattern.FindStringSubmatch(path)[1])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// servePrincipal answers PROPFIND on the principal and calendar home.
func (s *Server) servePrincipal(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PROPFIND" {
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + "\n")
	b.WriteString("<D:response><D:href>/caldav/</D:href><D:propstat><D:prop>\n")
	b.WriteString("<D:resourcetype><D:collection/><D:principal/></D:resourcetype>\n")
	b.WriteString("<D:current-user-principal><D:href>/caldav/</D:href></D:current-user-principal>\n")
	b.WriteString("<C:calendar-home-set><D:href>/caldav/</D:href></C:calendar-home-set>\n")
	b.WriteString("<D:displayname>caldav</D:displayname>\n")
	b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>\n")
	b.WriteString("</D:multistatus>\n")
	writeXML(w, http.StatusMultiStatus, b.String())
}

// serveCalendar handles the calendar collection: PROPFIND and REPORT.
func (s *Server) serveCalendar(w http.ResponseWriter, r *http.Request, p appcollab.Principal) {
	switch r.Method {
	case "PROPFIND":
		s.propfindCalendar(w, r, p)
	case "REPORT":
		s.report(w, r, p)
	default:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, REPORT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) serveResource(w http.ResponseWriter, r *http.Request, p appcollab.Principal, uid string) {
	row, err := s.Store.GetEventByUID(r.Context(), p, uid)
	if err != nil {
		if !errors.Is(err, appcollab.ErrNotFound) {
			http.Error(w, "calendar unavailable", http.StatusBadGateway)
			return
		}
		if r.Method != http.MethodPut {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		row = store.MailboxEvents{}
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		body, err := eventBody(row)
		if err != nil {
			http.Error(w, "calendar unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("ETag", etag(row.Version))
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			// body is a plain iCalendar resource served as text/calendar,
			// not an HTML document; no escaping applies.
			_, _ = io.WriteString(w, body) //nolint:gosec // text/calendar body, not HTML
		}
	case http.MethodPut:
		s.putResource(w, r, p, uid, row)
	case http.MethodDelete:
		s.deleteResource(w, r, p, row)
	case "PROPFIND":
		props := requestProps(r.Body)
		var b strings.Builder
		b.WriteString(xmlHeader)
		b.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + "\n")
		b.WriteString("<D:response><D:href>" + xmlEscape(resourceHref(uid)) + "</D:href><D:propstat><D:prop>\n")
		b.WriteString(eventProps(row, props))
		b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>\n</D:multistatus>\n")
		writeXML(w, http.StatusMultiStatus, b.String())
	default:
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// putResource creates or updates one event under conditional headers.
// If-Match carries the optimistic version; a stale or missing match is a
// 412. If-None-Match means create only. A deleted UID is never
// resurrected in place: a new PUT creates a new resource id and the
// change log keeps the tombstone for the old one.
func (s *Server) putResource(w http.ResponseWriter, r *http.Request, p appcollab.Principal, uid string, row store.MailboxEvents) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxBodyBytes {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	event, extras, err := parseEventWithExtras(string(raw))
	if err != nil {
		http.Error(w, "invalid iCalendar", http.StatusBadRequest)
		return
	}
	if extras.UID != "" && extras.UID != uid {
		http.Error(w, "uid mismatch", http.StatusBadRequest)
		return
	}
	exists := row.ID != 0
	if !resourcePreconditions(r, exists, etagValue(row.Version)) {
		preconditionFailed(w)
		return
	}
	cal, err := s.Store.EnsureCalendar(r.Context(), p, defaultCalendarName, event.Timezone)
	if errors.Is(err, collab.ErrInvalidEvent) {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "calendar unavailable", http.StatusBadGateway)
		return
	}
	if exists {
		updated, err := s.Store.UpdateEvent(r.Context(), p, row.ID, row.Version, event)
		if errors.Is(err, appcollab.ErrConflict) || errors.Is(err, appcollab.ErrNotFound) {
			preconditionFailed(w)
			return
		}
		if errors.Is(err, collab.ErrInvalidEvent) {
			http.Error(w, "invalid event", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "calendar unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("ETag", etag(updated.Version))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	created, err := s.Store.CreateEvent(r.Context(), p, cal.ID, uid, event)
	if errors.Is(err, collab.ErrInvalidEvent) {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "calendar unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("ETag", etag(created.Version))
	w.Header().Set("Location", resourceHref(uid))
	w.WriteHeader(http.StatusCreated)
}

// deleteResource tombstones the event; the change log keeps a 404 marker.
func (s *Server) deleteResource(w http.ResponseWriter, r *http.Request, p appcollab.Principal, row store.MailboxEvents) {
	if !resourcePreconditions(r, true, etagValue(row.Version)) {
		preconditionFailed(w)
		return
	}
	if _, err := s.Store.DeleteEvent(r.Context(), p, row.ID, row.Version); err != nil {
		if errors.Is(err, appcollab.ErrConflict) || errors.Is(err, appcollab.ErrNotFound) {
			preconditionFailed(w)
			return
		}
		http.Error(w, "calendar unavailable", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func preconditionFailed(w http.ResponseWriter) {
	body := xmlHeader + `<D:error xmlns:D="DAV:"><D:precondition-failed/></D:error>` + "\n"
	writeXML(w, http.StatusPreconditionFailed, body)
}

func writeXML(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// RestoreEpoch exposes the non-secret instance generation to the protocol.
func (s ServiceStore) RestoreEpoch(ctx context.Context) (int64, error) {
	if source, ok := s.Q.(interface {
		PlatformRestoreSyncEpoch(context.Context) (int64, error)
	}); ok {
		return source.PlatformRestoreSyncEpoch(ctx)
	}
	return 1, nil
}
