package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailboxCorporateDirectoryHandler handles both read-only mailbox directory access
// and tenant administrative directory management (WP06, I13).
type MailboxCorporateDirectoryHandler struct {
	Pool  *db.Pool
	Store *store.Queries
}

type corporateContactJSON struct {
	ID           int64           `json:"id"`
	DisplayName  string          `json:"display_name"`
	GivenName    string          `json:"given_name,omitempty"`
	FamilyName   string          `json:"family_name,omitempty"`
	Organization string          `json:"organization,omitempty"`
	Department   string          `json:"department,omitempty"`
	JobTitle     string          `json:"job_title,omitempty"`
	Emails       json.RawMessage `json:"emails"`
	Phones       json.RawMessage `json:"phones"`
	Addresses    json.RawMessage `json:"addresses,omitempty"`
	Note         string          `json:"note,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
}

type directorySettingsRequest struct {
	Enabled bool `json:"enabled"`
}

type directoryContactRequest struct {
	DisplayName  string          `json:"display_name"`
	GivenName    string          `json:"given_name"`
	FamilyName   string          `json:"family_name"`
	Organization string          `json:"organization"`
	Department   string          `json:"department"`
	JobTitle     string          `json:"job_title"`
	Emails       json.RawMessage `json:"emails"`
	Phones       json.RawMessage `json:"phones"`
	Addresses    json.RawMessage `json:"addresses"`
	Note         string          `json:"note"`
}

func formatCorporateContact(c store.TenantDirectoryContacts) corporateContactJSON {
	emails := c.Emails
	if len(emails) == 0 || !json.Valid(emails) {
		emails = []byte("[]")
	}
	phones := c.Phones
	if len(phones) == 0 || !json.Valid(phones) {
		phones = []byte("[]")
	}
	addresses := c.Addresses
	if len(addresses) == 0 || !json.Valid(addresses) {
		addresses = []byte("[]")
	}
	return corporateContactJSON{
		ID:           c.ID,
		DisplayName:  c.DisplayName,
		GivenName:    c.GivenName,
		FamilyName:   c.FamilyName,
		Organization: c.Organization,
		Department:   c.Department,
		JobTitle:     c.JobTitle,
		Emails:       json.RawMessage(emails),
		Phones:       json.RawMessage(phones),
		Addresses:    json.RawMessage(addresses),
		Note:         c.Note,
		CreatedAt:    c.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		UpdatedAt:    c.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
}

// List returns corporate directory contacts for mailbox users ONLY IF enabled is true.
func (h *MailboxCorporateDirectoryHandler) List(c *gin.Context) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	var q *store.Queries
	if h.Pool != nil {
		tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
		if err != nil {
			authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q = store.New(tx)
	} else if h.Store != nil {
		q = h.Store
	} else {
		authError(c, http.StatusServiceUnavailable, "DIRECTORY_UNAVAILABLE")
		return
	}

	settings, err := q.PlatformGetTenantDirectorySettings(ctx, session.TenantID)
	if err != nil || !settings.Enabled {
		c.JSON(http.StatusOK, gin.H{"contacts": []corporateContactJSON{}, "enabled": false})
		return
	}

	rows, err := q.PlatformListPublishedCorporateContacts(ctx, session.TenantID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}

	contacts := make([]corporateContactJSON, len(rows))
	for i, row := range rows {
		contacts[i] = formatCorporateContact(row)
	}
	c.JSON(http.StatusOK, gin.H{"contacts": contacts, "enabled": true})
}

// Search performs a substring search across published corporate directory contacts.
func (h *MailboxCorporateDirectoryHandler) Search(c *gin.Context) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}

	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusOK, gin.H{"contacts": []corporateContactJSON{}})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	var q *store.Queries
	if h.Pool != nil {
		tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
		if err != nil {
			authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q = store.New(tx)
	} else if h.Store != nil {
		q = h.Store
	} else {
		authError(c, http.StatusServiceUnavailable, "DIRECTORY_UNAVAILABLE")
		return
	}

	settings, err := q.PlatformGetTenantDirectorySettings(ctx, session.TenantID)
	if err != nil || !settings.Enabled {
		c.JSON(http.StatusOK, gin.H{"contacts": []corporateContactJSON{}})
		return
	}

	rows, err := q.PlatformSearchPublishedCorporateContacts(ctx, store.PlatformSearchPublishedCorporateContactsParams{
		TenantID: session.TenantID,
		Query:    query,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}

	contacts := make([]corporateContactJSON, len(rows))
	for i, row := range rows {
		contacts[i] = formatCorporateContact(row)
	}
	c.JSON(http.StatusOK, gin.H{"contacts": contacts})
}

// resolveAdminTenant returns tenantID for admin requests.
func (h *MailboxCorporateDirectoryHandler) resolveAdminTenant(c *gin.Context) (int64, bool) {
	if tid, ok := middleware.GetTenantID(c); ok && tid > 0 {
		return tid, true
	}
	if session, ok := middleware.GetMailboxSession(c); ok && session.TenantID > 0 {
		return session.TenantID, true
	}
	authError(c, http.StatusUnauthorized, "UNAUTHORIZED")
	return 0, false
}

func (h *MailboxCorporateDirectoryHandler) getAdminQueries(c *gin.Context, tenantID int64) (*store.Queries, func(), error) {
	if v, ok := middleware.QueriesFromCtx(c); ok {
		if q, ok := v.(*store.Queries); ok {
			return q, func() {}, nil
		}
	}
	if h.Pool != nil {
		tx, err := h.Pool.TenantTx(c.Request.Context(), tenantID)
		if err != nil {
			return nil, nil, err
		}
		return store.New(tx), func() { _ = tx.Rollback(c.Request.Context()) }, nil
	}
	if h.Store != nil {
		return h.Store, func() {}, nil
	}
	return nil, nil, errors.New("no store available")
}

// GetSettings returns directory enablement status for the tenant administrator.
func (h *MailboxCorporateDirectoryHandler) GetSettings(c *gin.Context) {
	tenantID, ok := h.resolveAdminTenant(c)
	if !ok {
		return
	}
	q, cleanup, err := h.getAdminQueries(c, tenantID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DIRECTORY_UNAVAILABLE")
		return
	}
	defer cleanup()

	settings, err := q.PlatformGetTenantDirectorySettings(c.Request.Context(), tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusOK, gin.H{"enabled": false})
			return
		}
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":    settings.Enabled,
		"updated_at": settings.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
	})
}

// UpdateSettings allows an admin to enable or disable corporate directory.
func (h *MailboxCorporateDirectoryHandler) UpdateSettings(c *gin.Context) {
	tenantID, ok := h.resolveAdminTenant(c)
	if !ok {
		return
	}
	var req directorySettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	q, cleanup, err := h.getAdminQueries(c, tenantID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DIRECTORY_UNAVAILABLE")
		return
	}
	defer cleanup()

	res, err := q.PlatformSetTenantDirectorySettings(c.Request.Context(), store.PlatformSetTenantDirectorySettingsParams{
		TenantID: tenantID,
		Enabled:  req.Enabled,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":    res.Enabled,
		"updated_at": res.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
	})
}

// AdminListContacts lists all directory contacts for the tenant.
func (h *MailboxCorporateDirectoryHandler) AdminListContacts(c *gin.Context) {
	tenantID, ok := h.resolveAdminTenant(c)
	if !ok {
		return
	}
	q, cleanup, err := h.getAdminQueries(c, tenantID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DIRECTORY_UNAVAILABLE")
		return
	}
	defer cleanup()

	rows, err := q.PlatformListTenantDirectoryContacts(c.Request.Context(), tenantID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	contacts := make([]corporateContactJSON, len(rows))
	for i, r := range rows {
		contacts[i] = formatCorporateContact(r)
	}
	c.JSON(http.StatusOK, gin.H{"contacts": contacts})
}

// AdminCreateContact creates a new directory contact.
func (h *MailboxCorporateDirectoryHandler) AdminCreateContact(c *gin.Context) {
	tenantID, ok := h.resolveAdminTenant(c)
	if !ok {
		return
	}
	var req directoryContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if len(displayName) == 0 || len(displayName) > 200 {
		authError(c, http.StatusBadRequest, "DISPLAY_NAME_INVALID")
		return
	}
	emailsRaw := req.Emails
	if len(emailsRaw) == 0 || !json.Valid(emailsRaw) {
		emailsRaw = []byte("[]")
	}
	phonesRaw := req.Phones
	if len(phonesRaw) == 0 || !json.Valid(phonesRaw) {
		phonesRaw = []byte("[]")
	}
	addressesRaw := req.Addresses
	if len(addressesRaw) == 0 || !json.Valid(addressesRaw) {
		addressesRaw = []byte("[]")
	}

	q, cleanup, err := h.getAdminQueries(c, tenantID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DIRECTORY_UNAVAILABLE")
		return
	}
	defer cleanup()

	created, err := q.PlatformInsertTenantDirectoryContact(c.Request.Context(), store.PlatformInsertTenantDirectoryContactParams{
		TenantID:     tenantID,
		DisplayName:  displayName,
		GivenName:    req.GivenName,
		FamilyName:   req.FamilyName,
		Organization: req.Organization,
		Department:   req.Department,
		JobTitle:     req.JobTitle,
		Emails:       emailsRaw,
		Phones:       phonesRaw,
		Addresses:    addressesRaw,
		Note:         req.Note,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, formatCorporateContact(created))
}

// AdminUpdateContact updates an existing corporate directory contact.
func (h *MailboxCorporateDirectoryHandler) AdminUpdateContact(c *gin.Context) {
	tenantID, ok := h.resolveAdminTenant(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_ID")
		return
	}
	var req directoryContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if len(displayName) == 0 || len(displayName) > 200 {
		authError(c, http.StatusBadRequest, "DISPLAY_NAME_INVALID")
		return
	}
	emailsRaw := req.Emails
	if len(emailsRaw) == 0 || !json.Valid(emailsRaw) {
		emailsRaw = []byte("[]")
	}
	phonesRaw := req.Phones
	if len(phonesRaw) == 0 || !json.Valid(phonesRaw) {
		phonesRaw = []byte("[]")
	}
	addressesRaw := req.Addresses
	if len(addressesRaw) == 0 || !json.Valid(addressesRaw) {
		addressesRaw = []byte("[]")
	}

	q, cleanup, err := h.getAdminQueries(c, tenantID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DIRECTORY_UNAVAILABLE")
		return
	}
	defer cleanup()

	updated, err := q.PlatformUpdateTenantDirectoryContact(c.Request.Context(), store.PlatformUpdateTenantDirectoryContactParams{
		TenantID:     tenantID,
		ID:           id,
		DisplayName:  displayName,
		GivenName:    req.GivenName,
		FamilyName:   req.FamilyName,
		Organization: req.Organization,
		Department:   req.Department,
		JobTitle:     req.JobTitle,
		Emails:       emailsRaw,
		Phones:       phonesRaw,
		Addresses:    addressesRaw,
		Note:         req.Note,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusNotFound, "CONTACT_NOT_FOUND")
			return
		}
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, formatCorporateContact(updated))
}

// AdminDeleteContact deletes a corporate directory contact.
func (h *MailboxCorporateDirectoryHandler) AdminDeleteContact(c *gin.Context) {
	tenantID, ok := h.resolveAdminTenant(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_ID")
		return
	}

	q, cleanup, err := h.getAdminQueries(c, tenantID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DIRECTORY_UNAVAILABLE")
		return
	}
	defer cleanup()

	deleted, err := q.PlatformDeleteTenantDirectoryContact(c.Request.Context(), store.PlatformDeleteTenantDirectoryContactParams{
		TenantID: tenantID,
		ID:       id,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if deleted == 0 {
		authError(c, http.StatusNotFound, "CONTACT_NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// RegisterMailboxCorporateDirectoryRoutes registers webmail mailbox user endpoints.
func RegisterMailboxCorporateDirectoryRoutes(group *gin.RouterGroup, h *MailboxCorporateDirectoryHandler) {
	group.GET("/corporate-directory", h.List)
	group.GET("/corporate-directory/search", h.Search)
}

// RegisterAdminCorporateDirectoryRoutes registers tenant admin management endpoints.
func RegisterAdminCorporateDirectoryRoutes(group *gin.RouterGroup, h *MailboxCorporateDirectoryHandler) {
	group.GET("/directory/settings", h.GetSettings)
	group.PUT("/directory/settings", h.UpdateSettings)
	group.GET("/corporate-directory/settings", h.GetSettings)
	group.PUT("/corporate-directory/settings", h.UpdateSettings)
	group.GET("/directory/contacts", h.AdminListContacts)
	group.POST("/directory/contacts", h.AdminCreateContact)
	group.PUT("/directory/contacts/:id", h.AdminUpdateContact)
	group.DELETE("/directory/contacts/:id", h.AdminDeleteContact)
	group.GET("/corporate-directory/contacts", h.AdminListContacts)
	group.POST("/corporate-directory/contacts", h.AdminCreateContact)
	group.PUT("/corporate-directory/contacts/:id", h.AdminUpdateContact)
	group.DELETE("/corporate-directory/contacts/:id", h.AdminDeleteContact)
}
