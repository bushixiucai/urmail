package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// CardDAVReport returns the authenticated mailbox address book as vCards.
// It is a read-only subset, not a complete CardDAV server.
func (h MailboxContactsHandler) CardDAVReport(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	rows, err := h.Service.Q.PlatformListContacts(c.Request.Context(), store.PlatformListContactsParams{TenantID: p.TenantID, MailboxID: p.MailboxID, PageLimit: 1000})
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	var body string
	for _, row := range rows {
		contact := collab.Contact{DisplayName: row.DisplayName, GivenName: row.GivenName, FamilyName: row.FamilyName, Note: row.Note}
		_ = json.Unmarshal(row.Emails, &contact.Emails)
		_ = json.Unmarshal(row.Phones, &contact.Phones)
		card, err := collab.VCard(contact)
		if err != nil {
			authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
			return
		}
		body += card
	}
	c.Data(http.StatusOK, "text/vcard; charset=utf-8", []byte(body))
}

// CardDAVPropfind answers PROPFIND discovery with a minimal collection
// listing so CardDAV clients can enumerate the addressbook home.
func (h MailboxContactsHandler) CardDAVPropfind(c *gin.Context) {
	if _, ok := h.principal(c); !ok {
		return
	}
	body := `<?xml version="1.0" encoding="UTF-8"?><multistatus xmlns="DAV:"><response><propstat><prop><resourcetype><collection/></resourcetype></prop><status>HTTP/1.1 200 OK</status></propstat></response></multistatus>`
	c.Data(http.StatusMultiStatus, "application/xml; charset=utf-8", []byte(body))
}

// CardDAVWrite rejects write methods; the addressbook is read-only over CardDAV.
func (h MailboxContactsHandler) CardDAVWrite(c *gin.Context) {
	if _, ok := h.principal(c); !ok {
		return
	}
	authError(c, http.StatusNotImplemented, "CARDDAV_WRITE_NOT_IMPLEMENTED")
}
