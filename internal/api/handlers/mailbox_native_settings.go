package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/mailboxsettings"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// MailboxSettings is one mailbox's self-service identity snapshot.
type MailboxSettings struct {
	IdentityProfiles json.RawMessage `json:"identity_profiles"`
	DisplayName      string          `json:"display_name"`
	SignatureText    string          `json:"signature_text"`
	SignatureHTML    string          `json:"signature_html"`
	Preferences      json.RawMessage `json:"preferences"`
	Version          int64           `json:"version"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// MailboxSettingsSave carries an optimistic-concurrency settings write.
type MailboxSettingsSave struct {
	IdentityProfiles json.RawMessage
	TenantID         int64
	MailboxID        int64
	DisplayName      string
	SignatureText    string
	SignatureHTML    string
	Preferences      json.RawMessage
	ExpectedVersion  int64
}

// ErrSettingsConflict reports a lost optimistic-concurrency race.
var ErrSettingsConflict = errors.New("MAILBOX_SETTINGS_VERSION_CONFLICT")

// ErrInvalidSettingsVersion rejects out-of-range optimistic versions.
var ErrInvalidSettingsVersion = errors.New("invalid settings version")

// MailboxSelfSettingsStore reads and writes only the authenticated mailbox's
// own settings, quota and authorized sending identities.
type MailboxSelfSettingsStore interface {
	Settings(ctx context.Context, tenantID, mailboxID int64) (MailboxSettings, error)
	SaveSettings(ctx context.Context, save MailboxSettingsSave) (int64, error)
	Quota(ctx context.Context, tenantID, mailboxID int64) (used, total int64, err error)
	SenderIdentities(ctx context.Context, tenantID int64, sender string) ([]string, error)
	SenderIdentityAllowed(ctx context.Context, tenantID int64, sender, from string) (bool, error)
}

// StoreMailboxSelfSettings adapts platform queries through a platform
// transaction; every statement is bound to the session tenant and mailbox.
type StoreMailboxSelfSettings struct {
	Pool *db.Pool
}

func (s StoreMailboxSelfSettings) tx(ctx context.Context) (pgx.Tx, *store.Queries, error) {
	if s.Pool == nil {
		return nil, nil, pgx.ErrNoRows
	}
	tx, err := s.Pool.PlatformTx(ctx)
	if err != nil {
		return nil, nil, err
	}
	return tx, store.New(tx), nil
}

// Settings returns the stored snapshot, or an empty version-0 snapshot.
func (s StoreMailboxSelfSettings) Settings(ctx context.Context, tenantID, mailboxID int64) (MailboxSettings, error) {
	tx, q, err := s.tx(ctx)
	if err != nil {
		return MailboxSettings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := q.PlatformGetMailboxSettings(ctx, store.PlatformGetMailboxSettingsParams{TenantID: tenantID, MailboxID: mailboxID})
	if errors.Is(err, pgx.ErrNoRows) {
		return MailboxSettings{Preferences: json.RawMessage("{}")}, nil
	}
	if err != nil {
		return MailboxSettings{}, err
	}
	return MailboxSettings{
		DisplayName:      row.DisplayName,
		SignatureText:    row.SignatureText,
		SignatureHTML:    row.SignatureHtml,
		Preferences:      row.Preferences,
		IdentityProfiles: row.IdentityProfiles,
		Version:          int64(row.Version),
		UpdatedAt:        row.UpdatedAt.Time,
	}, nil
}

// SaveSettings applies the optimistic write: update first, insert only when
// no row exists yet. A row created by a concurrent writer yields a conflict.
func (s StoreMailboxSelfSettings) SaveSettings(ctx context.Context, save MailboxSettingsSave) (int64, error) {
	if save.ExpectedVersion < 0 || save.ExpectedVersion > math.MaxInt32 {
		return 0, ErrInvalidSettingsVersion
	}
	tx, q, err := s.tx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	params := store.PlatformUpdateMailboxSettingsParams{
		TenantID:         save.TenantID,
		MailboxID:        save.MailboxID,
		DisplayName:      save.DisplayName,
		SignatureText:    save.SignatureText,
		SignatureHtml:    save.SignatureHTML,
		Preferences:      save.Preferences,
		IdentityProfiles: save.IdentityProfiles,
		ExpectedVersion:  int32(save.ExpectedVersion),
	}
	n, err := q.PlatformUpdateMailboxSettings(ctx, params)
	if err != nil {
		return 0, err
	}
	if n == 1 {
		if err = tx.Commit(ctx); err != nil {
			return 0, err
		}
		return save.ExpectedVersion + 1, nil
	}
	if _, getErr := q.PlatformGetMailboxSettings(ctx, store.PlatformGetMailboxSettingsParams{TenantID: save.TenantID, MailboxID: save.MailboxID}); getErr == nil {
		return 0, ErrSettingsConflict
	} else if !errors.Is(getErr, pgx.ErrNoRows) {
		return 0, getErr
	}
	if save.ExpectedVersion != 0 {
		return 0, ErrSettingsConflict
	}
	inserted, err := q.PlatformInsertMailboxSettings(ctx, store.PlatformInsertMailboxSettingsParams{
		TenantID:         save.TenantID,
		MailboxID:        save.MailboxID,
		DisplayName:      save.DisplayName,
		SignatureText:    save.SignatureText,
		SignatureHtml:    save.SignatureHTML,
		Preferences:      save.Preferences,
		IdentityProfiles: save.IdentityProfiles,
	})
	if err != nil {
		return 0, err
	}
	if inserted != 1 {
		return 0, ErrSettingsConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return 1, nil
}

// Quota returns server-authoritative usage for the mailbox.
func (s StoreMailboxSelfSettings) Quota(ctx context.Context, tenantID, mailboxID int64) (int64, int64, error) {
	tx, q, err := s.tx(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := q.PlatformGetMailboxQuota(ctx, store.PlatformGetMailboxQuotaParams{TenantID: tenantID, MailboxID: mailboxID})
	if err != nil {
		return 0, 0, err
	}
	return row.BytesUsed, row.QuotaBytes, nil
}

// SenderIdentities lists the alias addresses this mailbox may send From,
// using the same reviewed sender_logins grants the send path enforces.
func (s StoreMailboxSelfSettings) SenderIdentities(ctx context.Context, tenantID int64, sender string) ([]string, error) {
	tx, q, err := s.tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := q.PlatformListMailboxSenderIdentities(ctx, store.PlatformListMailboxSenderIdentitiesParams{TenantID: tenantID, Sender: sender})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.LocalPart+"@"+row.Fqdn)
	}
	return out, nil
}

// SenderIdentityAllowed rechecks one From candidate against the same grants.
func (s StoreMailboxSelfSettings) SenderIdentityAllowed(ctx context.Context, tenantID int64, sender, from string) (bool, error) {
	tx, q, err := s.tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	n, err := q.PlatformMailboxSenderIdentityAllowed(ctx, store.PlatformMailboxSenderIdentityAllowedParams{TenantID: tenantID, Sender: sender, FromAddress: from})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

type mailboxSettingsRequest struct {
	IdentityProfiles json.RawMessage `json:"identity_profiles"`
	DisplayName      string          `json:"display_name"`
	SignatureText    string          `json:"signature_text"`
	SignatureHTML    string          `json:"signature_html"`
	Preferences      json.RawMessage `json:"preferences"`
	ExpectedVersion  int64           `json:"expected_version"`
}

func (h *MailboxNativeHandler) settingsReady(c *gin.Context) (webmail.Session, bool) {
	if !h.requireEnabled(c) || h.SelfSettings == nil {
		authError(c, http.StatusServiceUnavailable, "WEBMAIL_DISABLED")
		return webmail.Session{}, false
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return webmail.Session{}, false
	}
	return session, true
}

// GetSettings returns the mailbox's own signature, display name and
// preferences with the optimistic version.
func (h *MailboxNativeHandler) GetSettings(c *gin.Context) {
	session, ok := h.settingsReady(c)
	if !ok {
		return
	}
	settings, err := h.SelfSettings.Settings(c.Request.Context(), session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if len(settings.Preferences) == 0 {
		settings.Preferences = json.RawMessage("{}")
	}
	c.JSON(http.StatusOK, gin.H{
		"display_name":      settings.DisplayName,
		"identity_profiles": json.RawMessage(defaultProfiles(settings.IdentityProfiles)),
		"signature_text":    settings.SignatureText,
		"signature_html":    settings.SignatureHTML,
		"preferences":       publicMailboxPreferences(settings.Preferences),
		"version":           settings.Version,
		"updated_at":        settings.UpdatedAt.UTC().Format(time.RFC3339),
		"address":           session.Address,
	})
}

// PutSettings validates and stores settings under optimistic concurrency.
func (h *MailboxNativeHandler) PutSettings(c *gin.Context) {
	session, ok := h.settingsReady(c)
	if !ok {
		return
	}
	if c.Request.ContentLength > 131072 {
		authError(c, http.StatusRequestEntityTooLarge, "MAILBOX_SETTINGS_TOO_LARGE")
		return
	}
	var req mailboxSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if req.ExpectedVersion < 0 || req.ExpectedVersion > 2147483646 {
		authError(c, http.StatusBadRequest, "MAILBOX_SETTINGS_VERSION_INVALID")
		return
	}
	name, err := mailboxsettings.DisplayName(req.DisplayName)
	if err != nil {
		authError(c, http.StatusBadRequest, mailboxsettings.ErrDisplayName.Error())
		return
	}
	text, err := mailboxsettings.Signature(req.SignatureText, mailboxsettings.MaxSignatureText)
	if err != nil {
		authError(c, http.StatusBadRequest, mailboxsettings.ErrSignature.Error())
		return
	}
	html, err := mailboxsettings.Signature(req.SignatureHTML, mailboxsettings.MaxSignatureHTML)
	if err != nil {
		authError(c, http.StatusBadRequest, mailboxsettings.ErrSignature.Error())
		return
	}
	html, _ = webmail.SanitizeHTML(html)
	prefs, err := mailboxsettings.Preferences(req.Preferences)
	if err != nil {
		authError(c, http.StatusBadRequest, mailboxsettings.ErrPreferences.Error())
		return
	}
	previous, err := h.SelfSettings.Settings(c.Request.Context(), session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if previous.Version != req.ExpectedVersion {
		authError(c, http.StatusConflict, "MAILBOX_SETTINGS_VERSION_CONFLICT")
		return
	}
	prefs, err = h.protectMailboxAI(prefs, previous.Preferences)
	if err != nil {
		if errors.Is(err, mailboxsettings.ErrPreferences) || errors.Is(err, mailboxsettings.ErrAICredentialTarget) {
			authError(c, http.StatusBadRequest, err.Error())
		} else {
			authError(c, http.StatusServiceUnavailable, "AI_UNAVAILABLE")
		}
		return
	}
	// Legacy clients omit keys they have never heard of: restore them from
	// the stored preferences so a full-object PUT cannot silently reset
	// newer preferences such as allow_all_remote_images.
	prefs, err = mergeMailboxPreferences(prefs, previous.Preferences)
	if err != nil {
		authError(c, http.StatusBadRequest, mailboxsettings.ErrPreferences.Error())
		return
	}
	profiles, ok := h.normalizeIdentityProfiles(c, session, req.IdentityProfiles)
	if !ok {
		return
	}
	// The primary identity keeps its original scalar columns for old clients.
	// A profile editor may submit all identities together; canonicalize the
	// primary presentation here instead of keeping a second conflicting copy.
	if len(profiles) > 0 {
		var presentations map[string]mailboxIdentityProfile
		if json.Unmarshal(profiles, &presentations) == nil {
			if primary, ok := presentations[session.Address]; ok {
				name, text, html = primary.DisplayName, primary.SignatureText, primary.SignatureHTML
				delete(presentations, session.Address)
				profiles, _ = json.Marshal(presentations)
			}
		}
	}
	if !h.validatePresentationPreferences(c, session, prefs) {
		return
	}
	version, err := h.SelfSettings.SaveSettings(c.Request.Context(), MailboxSettingsSave{
		TenantID:         session.TenantID,
		MailboxID:        session.MailboxID,
		DisplayName:      name,
		SignatureText:    text,
		SignatureHTML:    html,
		Preferences:      prefs,
		IdentityProfiles: profiles,
		ExpectedVersion:  req.ExpectedVersion,
	})
	if errors.Is(err, ErrSettingsConflict) {
		authError(c, http.StatusConflict, "MAILBOX_SETTINGS_VERSION_CONFLICT")
		return
	}
	if errors.Is(err, ErrInvalidSettingsVersion) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"saved": true, "version": version})
}

// ListIdentities returns the addresses this mailbox may send From.
func (h *MailboxNativeHandler) ListIdentities(c *gin.Context) {
	session, ok := h.settingsReady(c)
	if !ok {
		return
	}
	addresses, err := h.SelfSettings.SenderIdentities(c.Request.Context(), session.TenantID, session.Address)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	type identity struct {
		Address string `json:"address"`
		Primary bool   `json:"primary"`
	}
	out := []identity{{Address: session.Address, Primary: true}}
	seen := map[string]bool{session.Address: true}
	for _, address := range addresses {
		if !seen[address] {
			seen[address] = true
			out = append(out, identity{Address: address, Primary: false})
		}
	}
	c.JSON(http.StatusOK, gin.H{"identities": out})
}

// GetQuota returns server-authoritative used/total bytes for the mailbox.
func (h *MailboxNativeHandler) GetQuota(c *gin.Context) {
	session, ok := h.settingsReady(c)
	if !ok {
		return
	}
	used, total, err := h.SelfSettings.Quota(c.Request.Context(), session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	const defaultMaxMessageBytes int64 = 26214400 // 25 MiB (encoded) default
	c.JSON(http.StatusOK, gin.H{"used_bytes": used, "quota_bytes": total, "max_message_bytes": defaultMaxMessageBytes})
}

// normalizeFromCandidate lowercases and bounds a client-provided From.
func normalizeFromCandidate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 998 || strings.ContainsAny(raw, "\r\n\x00") {
		return ""
	}
	address, err := mail.ParseAddress(raw)
	if err != nil || address.Address == "" {
		return ""
	}
	return strings.ToLower(address.Address)
}

func defaultProfiles(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}
