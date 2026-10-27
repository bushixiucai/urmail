package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	appcollab "github.com/bushixiucai/urmail/internal/collab"
	"github.com/bushixiucai/urmail/internal/domain/collab"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailboxContactsHandler serves contacts for the authenticated mailbox only.
type MailboxContactsHandler struct {
	Service    appcollab.Service
	GroupStore appcollab.GroupMemberStore
	Groups     contactGroupStore
}

type contactGroupStore interface {
	PlatformInsertContactGroup(context.Context, store.PlatformInsertContactGroupParams) (store.MailboxContactGroups, error)
	PlatformDeleteContactGroup(context.Context, store.PlatformDeleteContactGroupParams) (bool, error)
	PlatformListContactGroups(context.Context, store.PlatformListContactGroupsParams) ([]store.MailboxContactGroups, error)
}

type contactRequest struct {
	AddressbookID int64          `json:"addressbook_id"`
	Version       int64          `json:"version"`
	DisplayName   string         `json:"display_name"`
	GivenName     string         `json:"given_name"`
	FamilyName    string         `json:"family_name"`
	Emails        []collab.Email `json:"emails"`
	Phones        []collab.Phone `json:"phones"`
	Note          string         `json:"note"`
}

func (h MailboxContactsHandler) principal(c *gin.Context) (appcollab.Principal, bool) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return appcollab.Principal{}, false
	}
	return appcollab.Principal{TenantID: session.TenantID, MailboxID: session.MailboxID}, true
}

// ListContacts returns the mailbox address book page.
func (h MailboxContactsHandler) ListContacts(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	rows, err := h.Service.Q.PlatformListContacts(c.Request.Context(), store.PlatformListContactsParams{TenantID: p.TenantID, MailboxID: p.MailboxID, PageLimit: 100})
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"contacts": rows})
}

// CreateContact stores one normalized contact.
func (h MailboxContactsHandler) CreateContact(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	var req contactRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	book, err := h.Service.EnsureAddressbook(c.Request.Context(), p, "Contacts")
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	row, err := h.Service.CreateContact(c.Request.Context(), p, book.ID, contactFrom(req))
	if errors.Is(err, collab.ErrInvalidContact) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusCreated, row)
}

// UpdateContact requires the version the user last read.
func (h MailboxContactsHandler) UpdateContact(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	var req contactRequest
	if !bindMailboxMessage(c, &req) || req.Version <= 0 {
		if !c.IsAborted() {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		}
		return
	}
	row, err := h.Service.UpdateContact(c.Request.Context(), p, id, req.Version, contactFrom(req))
	if errors.Is(err, collab.ErrInvalidContact) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if errors.Is(err, appcollab.ErrConflict) || errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusConflict, "CONTACT_CONFLICT")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, row)
}

// DeleteContact tombstones one contact at the expected version.
func (h MailboxContactsHandler) DeleteContact(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	version, verr := strconv.ParseInt(c.Query("version"), 10, 64)
	if err != nil || verr != nil || id <= 0 || version <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if _, err = h.Service.Q.PlatformDeleteContact(c.Request.Context(), store.PlatformDeleteContactParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id, Version: version}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusConflict, "CONTACT_CONFLICT")
			return
		}
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// ExportContact returns one contact as vCard.
func (h MailboxContactsHandler) ExportContact(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := h.Service.Q.PlatformGetContact(c.Request.Context(), store.PlatformGetContactParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "CONTACT_NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	contact := collab.Contact{DisplayName: row.DisplayName, GivenName: row.GivenName, FamilyName: row.FamilyName, Note: row.Note}
	_ = json.Unmarshal(row.Emails, &contact.Emails)
	_ = json.Unmarshal(row.Phones, &contact.Phones)
	body, err := collab.VCard(contact)
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.Data(http.StatusOK, "text/vcard; charset=utf-8", []byte(body))
}

// ExportContactsVCF streams every contact in the mailbox address book as one
// vCard (3.0) document, ready for import into standard clients.
// GET /mailbox/contacts/export.vcf
func (h MailboxContactsHandler) ExportContactsVCF(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	rows, err := h.Service.Q.PlatformListContacts(c.Request.Context(), store.PlatformListContactsParams{TenantID: p.TenantID, MailboxID: p.MailboxID, PageLimit: 1000})
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	var b strings.Builder
	for _, row := range rows {
		contact := collab.Contact{DisplayName: row.DisplayName, GivenName: row.GivenName, FamilyName: row.FamilyName, Note: row.Note}
		_ = json.Unmarshal(row.Emails, &contact.Emails)
		_ = json.Unmarshal(row.Phones, &contact.Phones)
		card, err := collab.VCard(contact)
		if err != nil {
			authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
			return
		}
		b.WriteString(card)
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "contacts.vcf"}))
	c.Data(http.StatusOK, "text/vcard; charset=utf-8", []byte(b.String()))
}

// ImportContact stores one uploaded vCard into the mailbox address book.
func (h MailboxContactsHandler) ImportContact(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	raw, err := c.GetRawData()
	if err != nil || len(raw) == 0 || len(raw) > 200000 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	contact, err := collab.ParseVCard(string(raw))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	book, err := h.Service.EnsureAddressbook(c.Request.Context(), p, "Contacts")
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	row, err := h.Service.CreateContact(c.Request.Context(), p, book.ID, contact)
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusCreated, row)
}

func contactFrom(req contactRequest) collab.Contact {
	return collab.Contact{DisplayName: req.DisplayName, GivenName: req.GivenName, FamilyName: req.FamilyName, Emails: req.Emails, Phones: req.Phones, Note: req.Note}
}

// ImportContactsCSV parses an uploaded CSV of contacts and stores the rows it
// describes for the authenticated mailbox.
func (h MailboxContactsHandler) ImportContactsCSV(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	raw, err := c.GetRawData()
	if err != nil || len(raw) == 0 || len(raw) > 1000000 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	contacts, err := collab.ParseContactsCSV(string(raw))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	book, err := h.Service.EnsureAddressbook(c.Request.Context(), p, "Contacts")
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	for _, contact := range contacts {
		if _, err = h.Service.CreateContact(c.Request.Context(), p, book.ID, contact); err != nil {
			authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
			return
		}
	}
	c.JSON(http.StatusCreated, gin.H{"imported": len(contacts)})
}

type groupMemberRequest struct {
	GroupAddressbookID   int64 `json:"group_addressbook_id"`
	ContactAddressbookID int64 `json:"contact_addressbook_id"`
	GroupID              int64 `json:"group_id"`
	ContactID            int64 `json:"contact_id"`
}

// AddGroupMember adds one contact to a contact group after validating that
// both records belong to the caller's addressbook scope.
func (h MailboxContactsHandler) AddGroupMember(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	var req groupMemberRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	if _, err := collab.Member(req.GroupAddressbookID, req.ContactAddressbookID, req.GroupID, req.ContactID); err != nil {
		authError(c, http.StatusBadRequest, "CONTACT_GROUP_SCOPE")
		return
	}
	err := appcollab.AddGroupMember(c.Request.Context(), h.GroupStore, appcollab.Principal{TenantID: p.TenantID, MailboxID: p.MailboxID}, req.GroupID, req.ContactID)
	if errors.Is(err, collab.ErrGroupScope) {
		authError(c, http.StatusBadRequest, "CONTACT_GROUP_SCOPE")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.Status(http.StatusCreated)
}

type contactGroupRequest struct {
	AddressbookID int64  `json:"addressbook_id"`
	Name          string `json:"name"`
}

// ListGroups returns the contact groups owned by the authenticated mailbox.
func (h MailboxContactsHandler) ListGroups(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	if h.Groups == nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	rows, err := h.Groups.PlatformListContactGroups(c.Request.Context(), store.PlatformListContactGroupsParams{TenantID: p.TenantID, MailboxID: p.MailboxID})
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"groups": rows})
}

// CreateGroup creates a new contact group for the authenticated mailbox.
func (h MailboxContactsHandler) CreateGroup(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	var req contactGroupRequest
	if !bindMailboxMessage(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if h.Groups == nil || req.AddressbookID <= 0 || name == "" || len(name) > 100 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := h.Groups.PlatformInsertContactGroup(c.Request.Context(), store.PlatformInsertContactGroupParams{TenantID: p.TenantID, MailboxID: p.MailboxID, ID: req.AddressbookID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "CONTACT_GROUP_NOT_FOUND")
		return
	}
	if appcollab.IsConflict(err) {
		authError(c, http.StatusConflict, "CONFLICT")
		return
	}
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": row.ID})
}

// DeleteGroup removes one contact group owned by the authenticated mailbox.
func (h MailboxContactsHandler) DeleteGroup(c *gin.Context) {
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if h.Groups == nil || err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	removed, err := h.Groups.PlatformDeleteContactGroup(c.Request.Context(), store.PlatformDeleteContactGroupParams{PTenant: p.TenantID, PMailbox: p.MailboxID, PGroup: id})
	if err != nil {
		authError(c, http.StatusBadGateway, "CONTACTS_UNAVAILABLE")
		return
	}
	if !removed {
		authError(c, http.StatusNotFound, "CONTACT_GROUP_NOT_FOUND")
		return
	}
	c.Status(http.StatusNoContent)
}
