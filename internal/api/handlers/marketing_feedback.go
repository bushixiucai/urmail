package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/marketing"
	"github.com/bushixiucai/urmail/internal/store"
)

// PreviewCampaign reports consent/suppression counts without scheduling work.
func (h *MarketingHandler) PreviewCampaign(c *gin.Context) {
	tenant, ok := marketingTenant(c)
	if !ok {
		return
	}
	id, ok := marketingID(c, "campaign_id")
	if !ok {
		return
	}
	row, err := requestStore(c, h.Store).PreviewMarketingCampaign(c, store.PreviewMarketingCampaignParams{TenantID: tenant, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"preview": row})
}

// ResolveAttempt requires an explicit platform finding before retrying uncertain SMTP.
func (h *MarketingHandler) ResolveAttempt(c *gin.Context) {
	tenant, ok := marketingID(c, "id")
	if !ok {
		return
	}
	id, ok := marketingID(c, "recipient_id")
	if !ok {
		return
	}
	var input struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Outcome != "accepted" && input.Outcome != "not_submitted" || len(strings.TrimSpace(input.Reason)) == 0 || len(input.Reason) > 2048 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	resolved, err := requestStore(c, h.Platform).PlatformResolveMarketingAttempt(c, store.PlatformResolveMarketingAttemptParams{TenantID: tenant, RecipientID: id, Outcome: input.Outcome, Reason: input.Reason})
	if err != nil {
		authError(c, http.StatusInternalServerError, "DB_ERROR")
		return
	}
	if !resolved {
		authError(c, http.StatusConflict, "MARKETING_STATE_CONFLICT")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "resolved"})
}

// Feedback accepts only the configured gateway's signed, fresh event. Gateway
// secrets never enter query strings, responses, logs or tenant channel DTOs.
func (h *MarketingHandler) Feedback(c *gin.Context) {
	if h.Secrets == nil || h.Platform == nil {
		authError(c, http.StatusServiceUnavailable, "MARKETING_UNAVAILABLE")
		return
	}
	channelID, ok := marketingID(c, "channel_id")
	if !ok {
		return
	}
	channel, err := requestStore(c, h.Platform).PlatformGetMarketingChannel(c, channelID)
	if err != nil {
		authError(c, http.StatusBadRequest, "MARKETING_FEEDBACK_INVALID")
		return
	}
	if !strings.HasPrefix(channel.EncryptedConfig, "enc:v1:") {
		authError(c, http.StatusServiceUnavailable, "MARKETING_UNAVAILABLE")
		return
	}
	plain, err := h.Secrets.DecryptString(channel.EncryptedConfig)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "MARKETING_UNAVAILABLE")
		return
	}
	var cfg marketing.SMTPConfig
	if json.Unmarshal([]byte(plain), &cfg) != nil || len(cfg.FeedbackKey) < 32 {
		authError(c, http.StatusServiceUnavailable, "MARKETING_UNAVAILABLE")
		return
	}
	timestamp := c.GetHeader("X-Urmail-Timestamp")
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || unix < time.Now().Add(-5*time.Minute).Unix() || unix > time.Now().Add(time.Minute).Unix() {
		authError(c, http.StatusBadRequest, "MARKETING_FEEDBACK_INVALID")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	signature, err := hex.DecodeString(c.GetHeader("X-Urmail-Signature"))
	mac := hmac.New(sha256.New, []byte(cfg.FeedbackKey))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		authError(c, http.StatusBadRequest, "MARKETING_FEEDBACK_INVALID")
		return
	}
	var input struct {
		EventID     string `json:"event_id"`
		RecipientID int64  `json:"recipient_id"`
		Status      string `json:"status"`
	}
	if json.Unmarshal(body, &input) != nil || input.RecipientID <= 0 || len(input.EventID) == 0 || len(input.EventID) > 200 || input.Status != "bounced" && input.Status != "complaint" && input.Status != "deferred" {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	canonical, _ := json.Marshal(input)
	digest := sha256.Sum256(canonical)
	result, err := requestStore(c, h.Platform).PlatformRecordMarketingFeedback(c, store.PlatformRecordMarketingFeedbackParams{ChannelID: channelID, EventID: input.EventID, RecipientID: input.RecipientID, Status: input.Status, PayloadHash: hex.EncodeToString(digest[:])})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if result == "conflict" || result == "not_found" {
		authError(c, http.StatusConflict, "MARKETING_FEEDBACK_INVALID")
		return
	}
	c.Status(http.StatusNoContent)
}
