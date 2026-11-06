package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainSettings "github.com/bushixiucai/urmail/internal/domain/settings"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// BrandingStore separates public visual lookups from platform-only mutations.
type BrandingStore interface {
	PlatformPublicThemeSettings(context.Context, int64) ([]store.PlatformPublicThemeSettingsRow, error)
	PublicBrandByHost(context.Context, string) (store.PublicBrandByHostRow, error)
	PublicBrandBySlug(context.Context, string) (store.PublicBrandBySlugRow, error)
	PlatformManagedBrands(context.Context) ([]store.PlatformManagedBrandsRow, error)
	PlatformSaveBrand(context.Context, store.PlatformSaveBrandParams) (store.PlatformSaveBrandRow, error)
	PlatformListBrandHosts(context.Context) ([]store.PlatformListBrandHostsRow, error)
	PlatformBindBrandHost(context.Context, store.PlatformBindBrandHostParams) (store.PlatformBindBrandHostRow, error)
	PlatformUnbindBrandHost(context.Context, string) (int64, error)
	PlatformSetBrandStatus(context.Context, store.PlatformSetBrandStatusParams) (int64, error)
	PlatformDeleteBrand(context.Context, int64) (int64, error)
	PlatformResetBrandTheme(context.Context, int64) (int64, error)
}

// BrandingHandler resolves visual branding and manages authorized host bindings.
type BrandingHandler struct {
	Store       BrandingStore
	PrimaryHost string
	Reconcile   ReconcileTrigger
}

// NewBrandingHandler wires the platform-scoped branding store.
func NewBrandingHandler(s BrandingStore) *BrandingHandler {
	if queries, ok := s.(*store.Queries); ok && queries == nil {
		s = nil
	}
	return &BrandingHandler{Store: s}
}

// PublicBrand is deliberately independent of the persisted row: no settings,
// tenant IDs, credentials, host inventory or arbitrary theme keys are exposed.
type PublicBrand struct {
	Slug         string            `json:"slug"`
	Name         string            `json:"name"`
	Theme        map[string]string `json:"theme"`
	LogoURL      *string           `json:"logo_url"`
	FaviconURL   *string           `json:"favicon_url"`
	PrimaryColor *string           `json:"primary_color"`
}

// Public resolves authorized host branding before link or default branding.
func (h *BrandingHandler) Public(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	fallback := PublicBrand{Slug: "default", Name: "urMail.cn", Theme: map[string]string{}}
	if h.Store == nil {
		c.JSON(http.StatusOK, gin.H{"brand": fallback, "source": "default"})
		return
	}
	var row store.PublicBrandBySlugRow
	var found bool
	source := "default"
	if host, ok := normalizeBrandHost(c.Request.Host); ok {
		byHost, err := requestStore(c, h.Store).PublicBrandByHost(c, host)
		if err == nil {
			row = store.PublicBrandBySlugRow(byHost)
			found = true
			source = "host"
		} else if !errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusServiceUnavailable, "DB_ERROR")
			return
		}
	}
	if !found {
		slug := c.Query("brand")
		if slug != "" && slugPattern.MatchString(slug) {
			var err error
			row, err = requestStore(c, h.Store).PublicBrandBySlug(c, slug)
			if err == nil {
				found = true
				source = "link"
			} else if !errors.Is(err, pgx.ErrNoRows) {
				authError(c, http.StatusServiceUnavailable, "DB_ERROR")
				return
			}
		}
	}
	if !found {
		var err error
		row, err = requestStore(c, h.Store).PublicBrandBySlug(c, "default")
		if err == nil {
			found = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			authError(c, http.StatusServiceUnavailable, "DB_ERROR")
			return
		}
	}
	if !found {
		c.JSON(http.StatusOK, gin.H{"brand": fallback, "source": "default"})
		return
	}
	rows, err := requestStore(c, h.Store).PlatformPublicThemeSettings(c, row.ID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	entries := make([]domainSettings.SettingEntry, 0, len(rows))
	for _, setting := range rows {
		entries = append(entries, domainSettings.SettingEntry{Scope: domainSettings.Scope(setting.Scope), ScopeID: setting.ScopeID, Key: setting.Key, Value: setting.Value})
	}
	effective, err := domainSettings.BuildEffectiveTheme(domainSettings.BrandThemeSource{BrandID: row.ID, BrandSlug: row.Slug, Theme: row.Theme, PrimaryColor: textPtr(row.PrimaryColor)}, entries)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	theme, _ := sanitizeBrandTheme(effective.Theme)
	primary := effective.PrimaryColor
	if primary != nil && !validBrandThemeValue("primary", *primary) {
		primary = nil
	}
	c.JSON(http.StatusOK, gin.H{"brand": PublicBrand{Slug: row.Slug, Name: row.Name, Theme: theme, LogoURL: safeVisualURL(row.LogoUrl.String), FaviconURL: safeVisualURL(row.FaviconUrl.String), PrimaryColor: primary}, "source": source})
}

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var colorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
var radiusPattern = regexp.MustCompile(`^\d{1,4}(\.\d{1,3})?(px|rem|em|%)?$`)

func normalizeBrandHost(raw string) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if value, port, err := net.SplitHostPort(host); err == nil {
		if _, err = strconv.ParseUint(port, 10, 16); err != nil {
			return "", false
		}
		host = value
	} else if strings.Contains(host, ":") {
		return "", false
	}
	host = strings.TrimSuffix(host, ".")
	return host, validation.IsValidHostname(host)
}
func safeVisualURL(raw string) *string {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil
	}
	return &raw
}
func validBrandThemeValue(key, value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	if key == "radius" {
		return radiusPattern.MatchString(value)
	}
	switch key {
	case "primary", "secondary", "background", "foreground", "accent", "ring":
	default:
		return false
	}
	if colorPattern.MatchString(value) {
		return true
	}
	switch strings.ToLower(value) {
	case "transparent", "currentcolor", "white", "black", "red", "green", "blue", "orange", "yellow", "purple", "gray", "grey":
		return true
	}
	return false
}
func sanitizeBrandTheme(raw []byte) (map[string]string, bool) {
	clean := map[string]string{}
	if len(raw) == 0 {
		return clean, true
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return clean, false
	}
	valid := true
	for key, rawValue := range values {
		var value string
		if json.Unmarshal(rawValue, &value) != nil || !validBrandThemeValue(key, value) {
			valid = false
			continue
		}
		clean[key] = value
	}
	return clean, valid
}

// ManagedBrand adds administrative status to the public visual fields.
type ManagedBrand struct {
	PublicBrand
	ID        int64              `json:"id"`
	Status    string             `json:"status"`
	CreatedAt pgtype.Timestamptz `json:"created_at"`
}

func managedBrand(row store.PlatformManagedBrandsRow) ManagedBrand {
	theme, _ := sanitizeBrandTheme(row.Theme)
	return ManagedBrand{PublicBrand: PublicBrand{Slug: row.Slug, Name: row.Name, Theme: theme, LogoURL: textPtr(row.LogoUrl), FaviconURL: textPtr(row.FaviconUrl), PrimaryColor: textPtr(row.PrimaryColor)}, ID: row.ID, Status: row.Status, CreatedAt: row.CreatedAt}
}

// List returns platform-managed brands without raw settings.
func (h *BrandingHandler) List(c *gin.Context) {
	rows, err := requestStore(c, h.Store).PlatformManagedBrands(c)
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	out := make([]ManagedBrand, 0, len(rows))
	for _, row := range rows {
		out = append(out, managedBrand(row))
	}
	c.JSON(http.StatusOK, gin.H{"brands": out})
}

// Save validates and saves editable brand fields by slug.
func (h *BrandingHandler) Save(c *gin.Context) {
	var req struct {
		BrandInput
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !slugPattern.MatchString(req.Slug) || strings.TrimSpace(req.Name) == "" {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}
	if (req.Status != "active" && req.Status != "disabled") || (req.Slug == "default" && req.Status != "active") {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	theme, valid := sanitizeBrandTheme(req.Theme)
	if !valid || (req.LogoURL != "" && safeVisualURL(req.LogoURL) == nil) || (req.FaviconURL != "" && safeVisualURL(req.FaviconURL) == nil) || (req.PrimaryColor != "" && !validBrandThemeValue("primary", req.PrimaryColor)) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	encoded, _ := json.Marshal(theme)
	brand, err := requestStore(c, h.Store).PlatformSaveBrand(c, store.PlatformSaveBrandParams{Slug: req.Slug, Name: strings.TrimSpace(req.Name), Theme: encoded, LogoUrl: textOrNull(req.LogoURL), FaviconUrl: textOrNull(req.FaviconURL), PrimaryColor: textOrNull(req.PrimaryColor), Status: req.Status})
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"brand": managedBrand(store.PlatformManagedBrandsRow(brand))})
}

// Hosts lists authorized host mappings for platform administrators.
func (h *BrandingHandler) Hosts(c *gin.Context) {
	rows, err := requestStore(c, h.Store).PlatformListBrandHosts(c)
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"hosts": rows})
}

// BindHost authorizes a hostname for one active brand.
func (h *BrandingHandler) BindHost(c *gin.Context) {
	var req struct {
		Hostname string `json:"hostname" binding:"required"`
		BrandID  int64  `json:"brand_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	hostname, ok := normalizeBrandHost(req.Hostname)
	if !ok || strings.Contains(req.Hostname, ":") {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if hostname == h.PrimaryHost {
		authError(c, 409, "BRAND_HOST_CONFLICT")
		return
	}
	if q, ok := requestStore(c, h.Store).(interface {
		PlatformBrandHostReserved(context.Context, string) (bool, error)
	}); ok {
		reserved, err := q.PlatformBrandHostReserved(c, hostname)
		if err != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		if reserved {
			authError(c, 409, "BRAND_HOST_CONFLICT")
			return
		}
	}
	host, err := requestStore(c, h.Store).PlatformBindBrandHost(c, store.PlatformBindBrandHostParams{Hostname: hostname, ID: req.BrandID})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusConflict, "BRAND_HOST_CONFLICT")
		return
	}
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": host})
}

// UnbindHost removes an existing hostname authorization.
func (h *BrandingHandler) UnbindHost(c *gin.Context) {
	host, ok := normalizeBrandHost(c.Param("hostname"))
	if !ok {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	affected, err := requestStore(c, h.Store).PlatformUnbindBrandHost(c, host)
	if err == nil && affected > 0 && h.Reconcile != nil {
		middleware.AfterCommit(c, func() { h.Reconcile.Trigger() })
	}
	brandMutationResult(c, affected, err)
}

// SetStatus enables or disables a non-default brand.
func (h *BrandingHandler) SetStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	var req struct {
		Status string `json:"status"`
	}
	if err != nil || c.ShouldBindJSON(&req) != nil || (req.Status != "active" && req.Status != "disabled") {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	n, err := requestStore(c, h.Store).PlatformSetBrandStatus(c, store.PlatformSetBrandStatusParams{ID: id, Status: req.Status})
	brandMutationResult(c, n, err)
}

// Delete removes an unreferenced non-default brand.
func (h *BrandingHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	n, err := requestStore(c, h.Store).PlatformDeleteBrand(c, id)
	brandMutationResult(c, n, err)
}

// ResetTheme clears a brand's visual fields and brand-level theme override.
func (h *BrandingHandler) ResetTheme(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	n, err := requestStore(c, h.Store).PlatformResetBrandTheme(c, id)
	brandMutationResult(c, n, err)
}

func brandMutationResult(c *gin.Context, n int64, err error) {
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			authError(c, http.StatusConflict, "CONFLICT")
			return
		}
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	if n == 0 {
		authError(c, http.StatusNotFound, "NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
