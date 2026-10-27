package carddav

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// URL layout helpers. The canonical hrefs use exactly one trailing slash on
// collections and "<id>.vcf" on contacts, so sync state stays stable.

func (h *Handler) principalHref() string { return h.Prefix + "/" }

func (h *Handler) bookHref(mailboxID int64) string {
	return h.Prefix + "/" + strconv.FormatInt(mailboxID, 10) + "/"
}

func (h *Handler) contactHref(mailboxID, contactID int64) string {
	return h.bookHref(mailboxID) + strconv.FormatInt(contactID, 10) + ".vcf"
}

// contactUID renders the UID this adapter guarantees on every card. The
// identity comes from the database primary key, so it survives round trips
// through both the CardDAV and the web API paths.
func contactUID(contactID int64) string {
	return strconv.FormatInt(contactID, 10) + "@urmail"
}

// contactFromRow converts one stored row into the domain contact.
func contactFromRow(row store.MailboxContacts) (collab.Contact, error) {
	contact := collab.Contact{
		DisplayName: row.DisplayName,
		GivenName:   row.GivenName,
		FamilyName:  row.FamilyName,
		Note:        row.Note,
	}
	if err := json.Unmarshal(row.Emails, &contact.Emails); err != nil {
		return collab.Contact{}, err
	}
	if err := json.Unmarshal(row.Phones, &contact.Phones); err != nil {
		return collab.Contact{}, err
	}
	return contact, nil
}

// renderCard returns the vCard body with the guaranteed UID line.
func renderCard(contactID int64, contact collab.Contact) (string, error) {
	card, err := collab.VCard(contact)
	if err != nil {
		return "", err
	}
	const versionLine = "VERSION:3.0\r\n"
	uid := "UID:" + contactUID(contactID) + "\r\n"
	return insertAfter(card, versionLine, uid), nil
}

// insertAfter places add right after the first occurrence of marker.
func insertAfter(body, marker, add string) string {
	idx := strings.Index(body, marker)
	if idx < 0 {
		return add + body
	}
	at := idx + len(marker)
	return body[:at] + add + body[at:]
}

// serveGet returns one contact resource as vCard with its ETag.
func (h *Handler) serveGet(w http.ResponseWriter, r *http.Request, p principal, _ int64, rest string) {
	id, ok := splitResource(rest)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	row, err := h.Service.Q.PlatformGetContact(r.Context(), store.PlatformGetContactParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
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
	w.Header().Set("ETag", etag(row.Version))
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(card))
}

// servePropfind answers DAV:propfind with depth 0 or 1 on the principal, the
// addressbook collection, or a single contact resource.
func (h *Handler) servePropfind(w http.ResponseWriter, r *http.Request, p principal, mailboxID int64, rest string) {
	var req propfindReq
	if r.ContentLength > 0 {
		raw, err := readBody(w, r)
		if err == nil {
			err = parseBody(raw, &req)
		}
		if err != nil {
			http.Error(w, "bad propfind body", http.StatusBadRequest)
			return
		}
	}
	depth, ok := depthOf(r.Header.Get("Depth"))
	if !ok {
		http.Error(w, "bad Depth", http.StatusBadRequest)
		return
	}
	isCollection := rest == ""
	if !isCollection && depth != 0 {
		// Depth 1 on a contact resource is meaningless; reject it instead
		// of pretending the resource is a collection.
		http.Error(w, "Depth 1 not allowed on contacts", http.StatusBadRequest)
		return
	}
	requested := requestedProps(req.Prop.Names)
	m := newMultistatus()
	if isCollection {
		h.propfindCollection(w, m, r, p, mailboxID, rest, depth, requested, req.Propname != nil)
	} else {
		id, ok := splitResource(rest)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		row, err := h.Service.Q.PlatformGetContact(r.Context(), store.PlatformGetContactParams{
			TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		h.addContactResponse(m, row, requested, req.Propname != nil, true)
	}
	writeMultistatus(w, m)
}

// servePrincipalPropfind answers PROPFIND depth 0 on the principal URL.
func (h *Handler) servePrincipalPropfind(w http.ResponseWriter, r *http.Request, _ principal) {
	var req propfindReq
	if r.ContentLength > 0 {
		raw, err := readBody(w, r)
		if err == nil {
			err = parseBody(raw, &req)
		}
		if err != nil {
			http.Error(w, "bad propfind body", http.StatusBadRequest)
			return
		}
	}
	ok := []string{
		propResourcetypeRoot,
		propDisplayName("urmail carddav principal"),
		currentUserPrincipal(h.principalHref()),
		"<D:principal-URL>" + propHref(h.principalHref()) + "</D:principal-URL>",
		propSupportedReportSet(),
	}
	ok = filterProps(ok, requestedProps(req.Prop.Names), req.Propname != nil)
	m := newMultistatus()
	m.addResponse(h.principalHref(), ok, nil, "")
	writeMultistatus(w, m)
}

// propfindCollection emits the collection response and, with depth 1, every
// live contact inside it.
func (h *Handler) propfindCollection(w http.ResponseWriter, m *multistatusBuilder, r *http.Request, p principal, mailboxID int64, _ string, depth int, requested map[string]bool, propname bool) {
	book, err := h.Service.EnsureAddressbook(r.Context(), appPrincipal(p), "Contacts")
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	ok := []string{
		propResourcetypeBook,
		propDisplayName(book.Name),
		currentUserPrincipal(h.principalHref()),
	}
	ok = append(ok, propSupportedReportSet())
	ok = filterProps(ok, requested, propname)
	m.addResponse(h.bookHref(mailboxID), ok, nil, "")
	if depth < 1 {
		return
	}
	rows, err := h.Service.Q.PlatformListContacts(r.Context(), store.PlatformListContactsParams{
		TenantID: p.TenantID, MailboxID: p.MailboxID, PageLimit: 1000,
	})
	if err != nil {
		// The collection itself was served; report children best effort.
		return
	}
	for _, row := range rows {
		h.addContactResponse(m, row, requested, propname, false)
	}
}

// addContactResponse renders one contact resource into the multistatus.
func (h *Handler) addContactResponse(m *multistatusBuilder, row store.MailboxContacts, requested map[string]bool, propname, withAddressData bool) {
	contact, err := contactFromRow(row)
	if err != nil {
		return
	}
	card, err := renderCard(row.ID, contact)
	if err != nil {
		return
	}
	props := []string{
		propResourcetypeContact,
		propGetetag(row.Version),
		propContentTypeCard,
	}
	if withAddressData {
		props = append(props, propAddressData(card))
	}
	props = filterProps(props, requested, propname)
	m.addResponse(h.contactHref(row.MailboxID, row.ID), props, nil, "")
}

// requestedProps maps requested property local names. Empty (no DAV:prop)
// means the default set; allprop also means the default set because this
// server has exactly the live properties listed above.
func requestedProps(names []xml.Name) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name.Local] = true
	}
	return out
}

// filterProps narrows the emitted set to the request. propname returns only
// the names with empty values.
func filterProps(props []string, requested map[string]bool, propname bool) []string {
	if requested == nil && !propname {
		return props
	}
	out := make([]string, 0, len(props))
	for _, prop := range props {
		name := propLocalName(prop)
		if requested != nil && !requested[name] {
			continue
		}
		if propname {
			out = append(out, "<"+name+"/>")
			continue
		}
		out = append(out, prop)
	}
	return out
}

// propLocalName extracts the local name from one generated property fragment.
func propLocalName(prop string) string {
	open := 1
	if len(prop) > 2 && prop[2] == ':' {
		open = 3
	}
	end := open
	for end < len(prop) && prop[end] != '>' && prop[end] != ' ' && prop[end] != '/' {
		end++
	}
	return prop[open:end]
}

func depthOf(header string) (int, bool) {
	switch header {
	case "0":
		return 0, true
	case "", "1", "infinity":
		// infinity is answered as 1; this server never recurses further.
		return 1, true
	default:
		return 0, false
	}
}

func etag(version int64) string {
	return "\"" + strconv.FormatInt(version, 10) + "\""
}

// appPrincipal converts the handler principal to the collab service type.
func appPrincipal(p principal) appcollab.Principal {
	return appcollab.Principal{TenantID: p.TenantID, MailboxID: p.MailboxID}
}

// writeMultistatus sends a 207 document.
func writeMultistatus(w http.ResponseWriter, m *multistatusBuilder) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write(m.bytes())
}
