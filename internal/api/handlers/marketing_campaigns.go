package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/marketing"
	"github.com/bushixiucai/urmail/internal/store"
)

// MarketingHandler manages durable tenant campaigns; configuring actual egress
// remains a platform operation. No create/import endpoint implicitly sends.
type MarketingHandler struct {
	Store    *store.Queries
	Platform *store.Queries
	Secrets  SecretsBox
}

func marketingTenant(c *gin.Context) (int64, bool) {
	tenant, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "NO_TENANT_CONTEXT")
	}
	return tenant, ok
}

func marketingID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return 0, false
	}
	return id, true
}

func marketingNameValid(s string) bool { return len(strings.TrimSpace(s)) > 0 && len(s) <= 200 }

// Channels returns only safe channel metadata, never encrypted credentials.
func (h *MarketingHandler) Channels(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListMarketingChannels(c, tenant)
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"channels": rows})
}

// ConfigureChannel accepts platform-authorized, explicitly dedicated egress.
func (h *MarketingHandler) ConfigureChannel(c *gin.Context) {
	tenant, ok := marketingID(c, "id")
	if !ok {
		return
	}
	if h.Platform == nil {
		authError(c, http.StatusServiceUnavailable, "MARKETING_UNAVAILABLE")
		return
	}
	var input struct {
		Name    string               `json:"name"`
		Kind    string               `json:"kind"`
		Enabled *bool                `json:"enabled"`
		SMTP    marketing.SMTPConfig `json:"smtp"`
	}
	if c.ShouldBindJSON(&input) != nil {
		authError(c, http.StatusBadRequest, "MARKETING_CHANNEL_INVALID")
		return
	}
	q := requestStore(c, h.Platform)
	if c.Param("channel_id") != "" {
		channelID, valid := marketingID(c, "channel_id")
		if !valid {
			return
		}
		existing, err := q.PlatformGetMarketingChannel(c, channelID)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && existing.TenantID != tenant {
			authError(c, http.StatusNotFound, "NOT_FOUND")
			return
		}
		if supportError(c, err) {
			return
		}
		if existing.Kind == "dedicated_ip" {
			// Unsupported legacy records are readable and explicitly disable-only.
			// Closing them preserves their original transport and secret metadata.
			if input.Enabled == nil || *input.Enabled || (input.Kind != "" && input.Kind != "dedicated_ip") || (input.Name != "" && input.Name != existing.Name) || input.SMTP != (marketing.SMTPConfig{}) {
				authError(c, http.StatusConflict, "MARKETING_CHANNEL_UNSUPPORTED")
				return
			}
			updated, err := q.PlatformUpdateMarketingChannel(c, store.PlatformUpdateMarketingChannelParams{TenantID: tenant, ID: channelID, Name: existing.Name, Kind: existing.Kind, SenderAddress: existing.SenderAddress, EncryptedConfig: existing.EncryptedConfig, Enabled: false})
			if supportError(c, err) {
				return
			}
			middleware.SetPlatformAuditTenant(c, tenant)
			middleware.SetPlatformAuditDetail(c, map[string]any{"operation": "disable_unsupported_legacy_channel", "channel_id": channelID, "kind": existing.Kind})
			c.JSON(http.StatusOK, gin.H{"channel": updated, "capability_available": false})
			return
		}
	}
	if input.Kind == "dedicated_ip" {
		authError(c, http.StatusConflict, "MARKETING_CHANNEL_UNSUPPORTED")
		return
	}
	if !marketingNameValid(input.Name) || input.SMTP.Validate(input.Kind) != nil {
		authError(c, http.StatusBadRequest, "MARKETING_CHANNEL_INVALID")
		return
	}
	if h.Secrets == nil {
		authError(c, http.StatusServiceUnavailable, "MARKETING_UNAVAILABLE")
		return
	}
	enabled := input.Enabled != nil && *input.Enabled
	input.SMTP.From = strings.ToLower(input.SMTP.From)
	payload, err := json.Marshal(input.SMTP) //nolint:gosec // Plaintext is immediately encrypted for private storage and is never returned.
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	encrypted, err := h.Secrets.EncryptString(string(payload))
	if err != nil || !strings.HasPrefix(encrypted, "enc:v1:") {
		authError(c, http.StatusServiceUnavailable, "MARKETING_UNAVAILABLE")
		return
	}
	if c.Param("channel_id") != "" {
		channelID, valid := marketingID(c, "channel_id")
		if !valid {
			return
		}
		updated, updateErr := requestStore(c, h.Platform).PlatformUpdateMarketingChannel(c, store.PlatformUpdateMarketingChannelParams{TenantID: tenant, ID: channelID, Name: input.Name, Kind: input.Kind, SenderAddress: input.SMTP.From, EncryptedConfig: encrypted, Enabled: enabled})
		if errors.Is(updateErr, pgx.ErrNoRows) {
			authError(c, http.StatusNotFound, "NOT_FOUND")
			return
		}
		if updateErr != nil {
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return
		}
		c.JSON(http.StatusOK, gin.H{"channel": updated, "capability_available": input.Kind == "dedicated_gateway"})
		return
	}
	row, err := requestStore(c, h.Platform).PlatformCreateMarketingChannel(c, store.PlatformCreateMarketingChannelParams{TenantID: tenant, Name: input.Name, Kind: input.Kind, SenderAddress: input.SMTP.From, EncryptedConfig: encrypted, Enabled: enabled})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"channel": row, "capability_available": input.Kind == "dedicated_gateway"})
}

// Templates lists bounded tenant content records.
func (h *MarketingHandler) Templates(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListMarketingTemplates(c, tenant)
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"templates": rows})
}

// CreateTemplate validates subject/header boundaries and preserves plain text.
func (h *MarketingHandler) CreateTemplate(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	var input struct {
		Name     string `json:"name"`
		Subject  string `json:"subject"`
		TextBody string `json:"text_body"`
	}
	if c.ShouldBindJSON(&input) != nil || !marketingNameValid(input.Name) || len(input.Subject) == 0 || len(input.Subject) > 998 || strings.ContainsAny(input.Subject, "\r\n") || len(input.TextBody) > 1<<20 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := requestStore(c, h.Store).CreateMarketingTemplate(c, store.CreateMarketingTemplateParams{TenantID: tenant, Name: input.Name, Subject: input.Subject, TextBody: input.TextBody})
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"template": row})
}

// Lists returns bounded contact-list metadata.
func (h *MarketingHandler) Lists(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListMarketingLists(c, tenant)
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"lists": rows})
}

// CreateList creates an empty list, without import or egress side effects.
func (h *MarketingHandler) CreateList(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&input) != nil || !marketingNameValid(input.Name) {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := requestStore(c, h.Store).CreateMarketingList(c, store.CreateMarketingListParams{TenantID: tenant, Name: input.Name})
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"list": row})
}

// Contacts returns up to 1000 list entries in the tenant transaction.
func (h *MarketingHandler) Contacts(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "list_id")
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListMarketingContacts(c, store.ListMarketingContactsParams{TenantID: tenant, ListID: id})
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"contacts": rows})
}

// ImportContacts requires explicit provenance and consent. Import never sends.
func (h *MarketingHandler) ImportContacts(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "list_id")
	if !ok {
		return
	}
	var input struct {
		Contacts []struct {
			Email         string `json:"email"`
			ConsentSource string `json:"consent_source"`
			OptedIn       bool   `json:"opted_in"`
		} `json:"contacts"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Contacts) == 0 || len(input.Contacts) > 1000 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	for _, contact := range input.Contacts {
		address, err := mail.ParseAddress(contact.Email)
		if err != nil || address.Address != contact.Email || len(contact.Email) > 254 || strings.ContainsAny(contact.Email, "\r\n") || len(strings.TrimSpace(contact.ConsentSource)) == 0 || len(contact.ConsentSource) > 256 {
			authError(c, http.StatusBadRequest, "INVALID_PARAMS")
			return
		}
	}
	q := requestStore(c, h.Store)
	for _, contact := range input.Contacts {
		_, err := q.UpsertMarketingContact(c, store.UpsertMarketingContactParams{TenantID: tenant, ListID: id, Email: strings.ToLower(contact.Email), ConsentSource: contact.ConsentSource, OptedIn: contact.OptedIn})
		if err != nil {
			if respondConstraintError(c, err) {
				return
			}
			authError(c, http.StatusInternalServerError, "DB_ERROR")
			return
		}
	}
	c.JSON(http.StatusCreated, gin.H{"imported": len(input.Contacts)})
}

// Campaigns lists durable campaign states and their immutable scheduled content.
func (h *MarketingHandler) Campaigns(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListMarketingCampaigns(c, tenant)
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"campaigns": rows})
}

// CreateCampaign creates a draft referencing tenant-qualified existing resources.
func (h *MarketingHandler) CreateCampaign(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	var input struct {
		Name       string `json:"name"`
		ChannelID  int64  `json:"channel_id"`
		TemplateID int64  `json:"template_id"`
		ListID     int64  `json:"list_id"`
	}
	if c.ShouldBindJSON(&input) != nil || !marketingNameValid(input.Name) || input.ChannelID <= 0 || input.TemplateID <= 0 || input.ListID <= 0 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	row, err := requestStore(c, h.Store).CreateMarketingCampaign(c, store.CreateMarketingCampaignParams{TenantID: tenant, Name: input.Name, ChannelID: input.ChannelID, TemplateID: input.TemplateID, ListID: input.ListID})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"campaign": row})
}

// ConfirmCampaign is the explicit transition from draft to scheduled egress.
func (h *MarketingHandler) ConfirmCampaign(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "campaign_id")
	if !ok {
		return
	}
	var input struct {
		ScheduledAt time.Time `json:"scheduled_at"`
		Confirm     bool      `json:"confirm"`
	}
	if c.ShouldBindJSON(&input) != nil || !input.Confirm || input.ScheduledAt.IsZero() {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	result, err := requestStore(c, h.Store).ScheduleMarketingCampaign(c, store.ScheduleMarketingCampaignParams{TenantID: tenant, CampaignID: id, ScheduledAt: pgtype.Timestamptz{Time: input.ScheduledAt.UTC(), Valid: true}})
	if err != nil {
		if respondConstraintError(c, err) {
			return
		}
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	if result != "scheduled" && result != "running" && result != "completed" {
		authError(c, http.StatusConflict, "MARKETING_NOT_READY")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": result})
}

// ChangeCampaign pauses, resumes or cancels without changing in-flight facts.
func (h *MarketingHandler) ChangeCampaign(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "campaign_id")
	if !ok {
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Status != "paused" && input.Status != "scheduled" && input.Status != "canceled" {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Store)
	row, err := q.ChangeMarketingCampaignState(c, store.ChangeMarketingCampaignStateParams{TenantID: tenant, ID: id, Status: input.Status})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusConflict, "MARKETING_STATE_CONFLICT")
		return
	}
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}

	c.JSON(http.StatusOK, gin.H{"campaign": row})
}

// Recipients exposes bounded per-recipient results, including uncertain outcomes.
func (h *MarketingHandler) Recipients(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "campaign_id")
	if !ok {
		return
	}
	rows, err := requestStore(c, h.Store).ListMarketingRecipients(c, store.ListMarketingRecipientsParams{TenantID: tenant, CampaignID: id})
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"recipients": rows})
}
