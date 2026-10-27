// Package caldav reports: PROPFIND on the collection plus the REPORT verbs. Only
// calendar-multiget, sync-collection and a basic calendar-query are
// implemented; anything else is rejected with 403 instead of a
// pseudo-success. Sync tokens are change log ids, so tombstones stay
// visible and deleted resources never come back as 200.
package caldav

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	restorestate "github.com/bushixiucai/urmail/internal/domain/restore"
	"github.com/bushixiucai/urmail/internal/store"
)

// pgTime converts a time to a PostgreSQL timestamptz value.
func pgTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

// fullRange covers every storable event time.
var (
	fullStart = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	fullEnd   = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
)

// eventProps renders the requested properties of one event resource.
func eventProps(row store.MailboxEvents, props map[string]bool) string {
	want := func(name string) bool { return len(props) == 0 || props[name] }
	var b strings.Builder
	if want("resourcetype") {
		b.WriteString("<D:resourcetype/>\n")
	}
	if want("getetag") {
		b.WriteString("<D:getetag>" + xmlEscape(etag(row.Version)) + "</D:getetag>\n")
	}
	if want("getcontenttype") {
		b.WriteString("<D:getcontenttype>text/calendar; charset=utf-8</D:getcontenttype>\n")
	}
	if want("displayname") {
		b.WriteString("<D:displayname>" + xmlEscape(row.Uid+".ics") + "</D:displayname>\n")
	}
	if want("calendar-data") {
		if body, err := eventBody(row); err == nil {
			b.WriteString("<C:calendar-data>" + xmlEscape(body) + "</C:calendar-data>\n")
		}
	}
	return b.String()
}

// propfindCalendar answers PROPFIND depth 0/1 on the collection.
func (s *Server) propfindCalendar(w http.ResponseWriter, r *http.Request, p appcollab.Principal) {
	depth := requestDepth(r)
	props := requestProps(r.Body)
	rows, err := s.Store.ListEvents(r.Context(), p, fullStart, fullEnd, 1000)
	if err != nil {
		http.Error(w, "calendar unavailable", http.StatusBadGateway)
		return
	}
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + "\n")
	b.WriteString("<D:response><D:href>/caldav/calendar/</D:href><D:propstat><D:prop>\n")
	b.WriteString("<D:resourcetype><D:collection/><C:calendar/></D:resourcetype>\n")
	b.WriteString("<D:displayname>Calendar</D:displayname>\n")
	b.WriteString("<C:supported-calendar-component-set><C:comp name=\"VEVENT\"/></C:supported-calendar-component-set>\n")
	b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>\n")
	if depth == 1 {
		for _, row := range rows {
			b.WriteString("<D:response><D:href>" + xmlEscape(resourceHref(row.Uid)) + "</D:href><D:propstat><D:prop>\n")
			b.WriteString(eventProps(row, props))
			b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>\n")
		}
	}
	b.WriteString("</D:multistatus>\n")
	writeXML(w, http.StatusMultiStatus, b.String())
}

// reportReq is the parsed body of one REPORT request.
type reportReq struct {
	kind       string
	hrefs      []string
	token      string
	limit      int
	props      map[string]bool
	rangeStart *time.Time
	rangeEnd   *time.Time
}

// parseReport reads one REPORT body without interpreting semantics.
func parseReport(body io.Reader) reportReq {
	req := reportReq{props: map[string]bool{}, limit: -1}
	if body == nil {
		return req
	}
	dec := xml.NewDecoder(io.LimitReader(body, 1<<20))
	depth := 0
	inProp := false
	propDepth := 0
	capture := false
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch elem := tok.(type) {
		case xml.StartElement:
			depth++
			if req.kind == "" && depth == 1 {
				req.kind = elem.Name.Local
			}
			if elem.Name.Local == "time-range" {
				req.rangeStart = parseStampAttr(elem, "start")
				req.rangeEnd = parseStampAttr(elem, "end")
			}
			if inProp && depth-1 == propDepth {
				req.props[elem.Name.Local] = true
			}
			if elem.Name.Local == "prop" && elem.Name.Space == "DAV:" {
				inProp = true
				propDepth = depth
			}
			if elem.Name.Local == "sync-token" || elem.Name.Local == "href" || elem.Name.Local == "nresults" {
				capture = true
				text.Reset()
			}
			if elem.Name.Local == "limit" {
				req.limit = -1
			}
		case xml.CharData:
			if capture {
				text.Write(elem)
			}
		case xml.EndElement:
			if capture {
				value := strings.TrimSpace(text.String())
				switch elem.Name.Local {
				case "sync-token":
					req.token = value
				case "href":
					req.hrefs = append(req.hrefs, value)
				case "nresults":
					if n, cerr := strconv.Atoi(value); cerr == nil {
						req.limit = n
					}
				}
				capture = false
			}
			if inProp && elem.Name.Local == "prop" && depth == propDepth {
				inProp = false
			}
			depth--
		}
	}
	return req
}

// parseStampAttr reads one iCalendar UTC timestamp attribute.
func parseStampAttr(elem xml.StartElement, name string) *time.Time {
	for _, attr := range elem.Attr {
		if attr.Name.Local != name {
			continue
		}
		if t, err := time.Parse(stampLayout, attr.Value); err == nil {
			return &t
		}
	}
	return nil
}

// report dispatches the supported REPORT types and rejects the rest.
func (s *Server) report(w http.ResponseWriter, r *http.Request, p appcollab.Principal) {
	req := parseReport(r.Body)
	switch req.kind {
	case "calendar-multiget":
		s.calendarMultiget(w, r, p, req)
	case "sync-collection":
		s.syncCollection(w, r, p, req)
	case "calendar-query":
		s.calendarQuery(w, r, p, req)
	default:
		unsupportedReport(w)
	}
}

// unsupportedReport answers 403 instead of pretending success.
func unsupportedReport(w http.ResponseWriter) {
	body := xmlHeader + `<D:error xmlns:D="DAV:"><D:supported-report/></D:error>` + "\n"
	writeXML(w, http.StatusForbidden, body)
}

// uidFromHref maps one request href back to an event UID.
func uidFromHref(href string) string {
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if m := resourcePattern.FindStringSubmatch(parsed.Path); m != nil {
		return m[1]
	}
	return ""
}

// response404 is one deleted or unknown resource in a multistatus.
func response404(b *strings.Builder, href string) {
	b.WriteString("<D:response><D:href>" + xmlEscape(href) + "</D:href><D:status>HTTP/1.1 404 Not Found</D:status></D:response>\n")
}

// calendarMultiget returns the named resources with data or 404 each.
func (s *Server) calendarMultiget(w http.ResponseWriter, r *http.Request, p appcollab.Principal, req reportReq) {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + "\n")
	for _, href := range req.hrefs {
		uid := uidFromHref(href)
		if uid == "" {
			response404(&b, href)
			continue
		}
		row, err := s.Store.GetEventByUID(r.Context(), p, uid)
		if err != nil {
			response404(&b, href)
			continue
		}
		b.WriteString("<D:response><D:href>" + xmlEscape(resourceHref(uid)) + "</D:href><D:propstat><D:prop>\n")
		b.WriteString(eventProps(row, req.props))
		b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>\n")
	}
	b.WriteString("</D:multistatus>\n")
	writeXML(w, http.StatusMultiStatus, b.String())
}

// syncResult is one deduplicated change keyed by href, so a recreated
// UID collapses into its newest change instead of resurrecting the old
// resource in the same report.
type syncResult struct {
	changeID   int64
	href       string
	deleted    bool
	resourceID int64
}

// syncCollection implements RFC 6578. The token is the last change log
// id the client consumed. Deleted resources are reported as 404
// responses and are never resurrected: a re-created event gets a new
// resource id, and deduplication by href keeps only the newest change
// per resource.
func (s *Server) syncCollection(w http.ResponseWriter, r *http.Request, p appcollab.Principal, req reportReq) {
	epoch := int64(1)
	if source, found := s.Store.(interface {
		RestoreEpoch(context.Context) (int64, error)
	}); found {
		value, err := source.RestoreEpoch(r.Context())
		if err != nil {
			http.Error(w, "calendar unavailable", http.StatusBadGateway)
			return
		}
		epoch = value
	}
	token, ok := restorestate.ParseSyncToken(syncIDPrefix, req.token, epoch)
	if !ok {
		if epoch != 1 {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<D:error xmlns:D="DAV:"><D:valid-sync-token/></D:error>`))
		} else {
			http.Error(w, "invalid sync token", http.StatusBadRequest)
		}
		return
	}
	rows, err := s.Store.ListEventChanges(r.Context(), p, token, syncBatch)
	if err != nil {
		http.Error(w, "calendar unavailable", http.StatusBadGateway)
		return
	}
	latest := map[string]syncResult{}
	for _, row := range rows {
		href := s.changeHref(r.Context(), p, row)
		prev, seen := latest[href]
		if !seen || prev.changeID < row.ID {
			latest[href] = syncResult{changeID: row.ID, href: href, deleted: row.Deleted, resourceID: row.ResourceID}
		}
	}
	entries := make([]syncResult, 0, len(latest))
	for _, entry := range latest {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].changeID < entries[j].changeID })
	newToken := token
	if len(rows) > 0 {
		newToken = rows[len(rows)-1].ID
	}
	if req.limit >= 0 && len(entries) > req.limit {
		entries = entries[:req.limit]
		if len(entries) > 0 {
			newToken = entries[len(entries)-1].changeID
		}
	}
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + "\n")
	for _, entry := range entries {
		if entry.deleted {
			response404(&b, entry.href)
			continue
		}
		row, err := s.Store.GetEventAny(r.Context(), p, entry.resourceID)
		if err != nil {
			response404(&b, entry.href)
			continue
		}
		b.WriteString("<D:response><D:href>" + xmlEscape(entry.href) + "</D:href><D:propstat><D:prop>\n")
		b.WriteString(eventProps(row, req.props))
		b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>\n")
	}
	b.WriteString("<D:sync-token>" + restorestate.SyncToken(syncIDPrefix, epoch, newToken) + "</D:sync-token>\n")
	b.WriteString("</D:multistatus>\n")
	writeXML(w, http.StatusMultiStatus, b.String())
}

// changeHref resolves the href of one change row, including the
// tombstone case where only the stored UID remains known.
func (s *Server) changeHref(ctx context.Context, p appcollab.Principal, row store.MailboxChanges) string {
	if stored, err := s.Store.GetEventAny(ctx, p, row.ResourceID); err == nil && stored.Uid != "" {
		return resourceHref(stored.Uid)
	}
	return "/caldav/calendar/missing-" + strconv.FormatInt(row.ResourceID, 10) + ".ics"
}

// calendarQuery implements the supported subset: a VEVENT comp filter
// with an optional time-range. Recurring events are included when any
// expanded instance overlaps the window, using the shared domain
// expansion. Filters beyond this subset are not interpreted here and
// unsupported REPORT roots are rejected in report().
func (s *Server) calendarQuery(w http.ResponseWriter, r *http.Request, p appcollab.Principal, req reportReq) {
	rows, err := s.Store.ListEvents(r.Context(), p, fullStart, fullEnd, 1000)
	if err != nil {
		http.Error(w, "calendar unavailable", http.StatusBadGateway)
		return
	}
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + "\n")
	for _, row := range rows {
		if req.rangeStart != nil && req.rangeEnd != nil {
			ev, everr := rowEvent(row)
			if everr != nil {
				continue
			}
			times, everr := collab.ExpandInstances(ev, *req.rangeStart, *req.rangeEnd)
			if everr != nil || len(times) == 0 {
				continue
			}
		}
		b.WriteString("<D:response><D:href>" + xmlEscape(resourceHref(row.Uid)) + "</D:href><D:propstat><D:prop>\n")
		b.WriteString(eventProps(row, req.props))
		b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>\n")
	}
	b.WriteString("</D:multistatus>\n")
	writeXML(w, http.StatusMultiStatus, b.String())
}
