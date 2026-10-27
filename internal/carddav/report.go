package carddav

import (
	"encoding/xml"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/domain/collab"
	restorestate "github.com/bushixiucai/urmail/internal/domain/restore"
	"github.com/bushixiucai/urmail/internal/store"
)

// serveReport dispatches the three implemented reports. Any other report is
// rejected with 403: there is no pseudo-success for unimplemented features.
func (h *Handler) serveReport(w http.ResponseWriter, r *http.Request, p principal, _ int64, rest string) {
	if rest != "" {
		// Reports target the addressbook collection.
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.ContentLength <= 0 {
		http.Error(w, "missing report body", http.StatusBadRequest)
		return
	}
	raw, err := readBody(w, r)
	if err != nil {
		http.Error(w, "bad report body", http.StatusBadRequest)
		return
	}
	root, err := peekRoot(raw)
	if err != nil {
		http.Error(w, "bad report body", http.StatusBadRequest)
		return
	}
	switch root {
	case "addressbook-query":
		h.reportQuery(w, raw, r, p)
	case "addressbook-multiget":
		h.reportMultiget(w, raw, r, p)
	case "sync-collection":
		h.reportSync(w, raw, r, p)
	default:
		http.Error(w, "unsupported report", http.StatusForbidden)
	}
}

// peekRoot returns the local name of the report root element. The body is
// parsed once more inside each handler; bodies are size limited so this is
// cheap and keeps each handler self-contained.
func peekRoot(raw []byte) (string, error) {
	var probe struct {
		XMLName xml.Name
	}
	if err := parseBody(raw, &probe); err != nil {
		return "", err
	}
	return probe.XMLName.Local, nil
}

// reportQuery answers carddav:addressbook-query with a basic text match on
// the FN or UID vCard properties. Matching is case-insensitive substring on
// the normalized field, evaluated over the live contact set.
func (h *Handler) reportQuery(w http.ResponseWriter, raw []byte, r *http.Request, p principal) {
	var req addressbookQueryReq
	if err := parseBody(raw, &req); err != nil {
		http.Error(w, "bad report body", http.StatusBadRequest)
		return
	}
	rows, err := h.Service.Q.PlatformListContacts(r.Context(), store.PlatformListContactsParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID, PageLimit: 1000,
	})
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	m := newMultistatus()
	for _, row := range rows {
		contact, err := contactFromRow(row)
		if err != nil {
			continue
		}
		card, err := renderCard(row.ID, contact)
		if err != nil {
			continue
		}
		if !matchesFilter(req.Filter.PropFilters, contact, contactUID(row.ID)) {
			continue
		}
		h.addCardResponse(m, row, card, requestedProps(req.Prop.Names))
	}
	writeMultistatus(w, m)
}

// matchesFilter evaluates the basic text-match set. All prop-filters must
// match (RFC 6352 section 10.5); an empty filter matches everything.
func matchesFilter(filters []propFilterReq, contact collab.Contact, uid string) bool {
	for _, filter := range filters {
		if filter.TextMatch == nil {
			// A prop-filter without a match condition tests presence.
			if !vCardPropertyPresent(filter.Name, contact, uid) {
				return false
			}
			continue
		}
		value, present := vCardProperty(filter.Name, contact, uid)
		matched := present && strings.Contains(strings.ToLower(value), strings.ToLower(strings.TrimSpace(filter.TextMatch.Value)))
		if filter.TextMatch.Negate == "yes" || filter.TextMatch.Negate == "1" || filter.TextMatch.Negate == "true" {
			matched = !matched
		}
		if !matched {
			return false
		}
	}
	return true
}

// vCardProperty returns the FN or UID property value this server guarantees.
// Other property filters test presence only via the contact fields.
func vCardProperty(name string, contact collab.Contact, uid string) (string, bool) {
	switch strings.ToUpper(name) {
	case "FN":
		return contact.DisplayName, contact.DisplayName != ""
	case "UID":
		return uid, true
	case "N":
		return strings.Join([]string{contact.FamilyName, contact.GivenName}, " "), true
	default:
		return "", false
	}
}

func vCardPropertyPresent(name string, contact collab.Contact, uid string) bool {
	_, ok := vCardProperty(name, contact, uid)
	return ok
}

// reportMultiget answers carddav:addressbook-multiget. Each href is resolved
// inside the authenticated mailbox; foreign hrefs fail closed as 404.
func (h *Handler) reportMultiget(w http.ResponseWriter, raw []byte, r *http.Request, p principal) {
	var req multigetReq
	if err := parseBody(raw, &req); err != nil {
		http.Error(w, "bad report body", http.StatusBadRequest)
		return
	}
	if len(req.Hrefs) == 0 {
		http.Error(w, "multiget requires hrefs", http.StatusBadRequest)
		return
	}
	m := newMultistatus()
	for _, href := range req.Hrefs {
		rest, mailboxID, err := h.splitPath(strings.TrimSpace(href))
		if err != nil || mailboxID != p.MailboxID {
			// Never leak the existence of another mailbox's contacts.
			m.addResponse(href, nil, nil, "HTTP/1.1 404 Not Found")
			continue
		}
		id, ok := splitResource(rest)
		if !ok {
			m.addResponse(href, nil, nil, "HTTP/1.1 404 Not Found")
			continue
		}
		row, err := h.Service.Q.PlatformGetContact(r.Context(), store.PlatformGetContactParams{
			TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			m.addResponse(href, nil, nil, "HTTP/1.1 404 Not Found")
			continue
		}
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		contact, err := contactFromRow(row)
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		card, err := renderCard(row.ID, contact)
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		h.addCardResponse(m, row, card, requestedProps(req.Prop.Names))
	}
	writeMultistatus(w, m)
}

// reportSync answers DAV:sync-collection (RFC 6578). The token is the last
// mailbox_changes id served. Tombstones come back as responses with a 404
// status and are never resurrected: deletion is a permanent change row and
// re-created contacts get a fresh identity.
func (h *Handler) reportSync(w http.ResponseWriter, raw []byte, r *http.Request, p principal) {
	var req syncCollectionReq
	if err := parseBody(raw, &req); err != nil {
		http.Error(w, "bad report body", http.StatusBadRequest)
		return
	}
	epoch, epochErr := h.restoreEpoch(r.Context())
	if epochErr != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	cursor := int64(0)
	if strings.TrimSpace(req.Token) != "" {
		var ok bool
		cursor, ok = restorestate.ParseSyncToken("urn:urmail:carddav:", req.Token, epoch)
		if !ok {
			// Unknown token: the client must resync from scratch.
			if epoch != 1 {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<D:error xmlns:D="DAV:"><D:valid-sync-token/></D:error>`))
			} else {
				http.Error(w, "invalid sync token", http.StatusBadRequest)
			}
			return
		}
	}
	changes, err := h.Service.Q.PlatformListChanges(r.Context(), store.PlatformListChangesParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID, ID: cursor, PageLimit: 1000,
	})
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	m := newMultistatus()
	// The latest change per contact decides the reported state.
	final := make(map[int64]store.MailboxChanges, len(changes))
	order := make([]int64, 0, len(changes))
	newCursor := cursor
	for _, change := range changes {
		if change.ID > newCursor {
			newCursor = change.ID
		}
		if change.ResourceType != "contact" {
			continue
		}
		if _, seen := final[change.ResourceID]; !seen {
			order = append(order, change.ResourceID)
		}
		final[change.ResourceID] = change
	}
	for _, id := range order {
		change := final[id]
		if change.Deleted {
			m.addResponse(h.contactHref(p.MailboxID, id), nil, nil, "HTTP/1.1 404 Not Found")
			continue
		}
		row, err := h.Service.Q.PlatformGetContact(r.Context(), store.PlatformGetContactParams{
			TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Deleted after the change row was written; report the tombstone.
			m.addResponse(h.contactHref(p.MailboxID, id), nil, nil, "HTTP/1.1 404 Not Found")
			continue
		}
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		contact, err := contactFromRow(row)
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		card, err := renderCard(row.ID, contact)
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		h.addCardResponse(m, row, card, nil)
	}
	m.setToken(restorestate.SyncToken("urn:urmail:carddav:", epoch, newCursor))
	writeMultistatus(w, m)
}

// addCardResponse renders one contact card with the requested properties.
func (h *Handler) addCardResponse(m *multistatusBuilder, row store.MailboxContacts, card string, requested map[string]bool) {
	props := []string{
		propResourcetypeContact,
		propGetetag(row.Version),
		propContentTypeCard,
		propAddressData(card),
	}
	props = filterProps(props, requested, false)
	m.addResponse(h.contactHref(row.MailboxID, row.ID), props, nil, "")
}
