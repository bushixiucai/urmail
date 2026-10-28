package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/supportdiag"
)

// A receipt binds the exact preview to its author and tenant. It expires before
// it can be reused as an unrelated ticket's long-lived diagnostic authority.
type diagnosticReceipt struct {
	Bundle  supportdiag.Bundle `json:"bundle"`
	Actor   int64              `json:"actor"`
	Expires int64              `json:"expires"`
}

func (h *SupportHandler) diagnosticMAC(payload []byte) []byte {
	mac := hmac.New(sha256.New, h.DiagnosticKey)
	_, _ = mac.Write([]byte("urmail/support-diagnostics/v1\x00"))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
func (h *SupportHandler) readDiagnosticReceipt(raw string, tenant, actor int64) ([]byte, error) {
	if len(h.DiagnosticKey) < 32 || len(raw) > 32768 {
		return nil, errors.New("invalid diagnostic receipt")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid diagnostic receipt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, h.diagnosticMAC(payload)) {
		return nil, errors.New("invalid diagnostic signature")
	}
	var receipt diagnosticReceipt
	if json.Unmarshal(payload, &receipt) != nil || receipt.Actor != actor || receipt.Bundle.TenantID != tenant || receipt.Bundle.Version != supportdiag.BundleVersion || receipt.Expires < time.Now().Unix() {
		return nil, errors.New("expired or mismatched diagnostic receipt")
	}
	return json.Marshal(receipt.Bundle)
}

// Diagnostics collects tenant-owned aggregate counters only after explicit
// consent. No logs, addresses, message content, configuration values or secrets
// are accepted from the browser or read from the database.
func (h *SupportHandler) Diagnostics(c *gin.Context) {
	tenant, actor, ok := supportActor(c)
	if !ok {
		return
	}
	role, _ := c.Get(middleware.ContextRole)
	if role != "owner" && role != "admin" {
		authError(c, 403, "FORBIDDEN")
		return
	}
	var in struct {
		Consent bool `json:"consent"`
	}
	if c.ShouldBindJSON(&in) != nil || !in.Consent {
		authError(c, 400, "SUPPORT_DIAGNOSTIC_CONSENT_REQUIRED")
		return
	}
	if len(h.DiagnosticKey) < 32 {
		authError(c, 503, "SUPPORT_DIAGNOSTIC_UNAVAILABLE")
		return
	}
	q := requestStore(c, h.Store)
	counts, err := q.SupportDiagnosticMailboxCounts(c.Request.Context(), tenant)
	if supportError(c, err) {
		return
	}
	now := time.Now().UTC()
	rows, err := q.TenantDeliveryOutcomeCounts(c.Request.Context(), store.TenantDeliveryOutcomeCountsParams{TenantID: pgtype.Int8{Int64: tenant, Valid: true}, OccurredAt: pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true}, WindowEnd: pgtype.Timestamptz{Time: now, Valid: true}})
	if supportError(c, err) {
		return
	}
	input := supportdiag.Input{TenantID: tenant, GeneratedAt: now, ShareWithSupport: true, ClientVersion: "web", Mailboxes: supportdiag.MailboxFlags{Total: counts.Total, Active: counts.Active, Suspended: counts.Suspended, OverQuota: counts.OverQuota}}
	for _, row := range rows {
		switch row.Status {
		case "sent":
			input.Delivery.Sent = row.Count
		case "deferred":
			input.Delivery.Deferred = row.Count
		case "bounced", "expired":
			input.Delivery.Failed += row.Count
		}
	}
	bundle, err := supportdiag.Build(input)
	if supportError(c, err) {
		return
	}
	// These facts are platform-wide or not sampled by this endpoint; never emit
	// default zeros as though a configuration, backup or queue probe succeeded.
	fields := bundle.Fields[:0]
	for _, field := range bundle.Fields {
		if strings.HasPrefix(field.Name, "config.") || strings.HasPrefix(field.Name, "backup.") || field.Name == "delivery.queued_now" {
			continue
		}
		fields = append(fields, field)
	}
	bundle.Fields = fields
	bundle.Fields = append(bundle.Fields, supportdiag.Field{Name: "config.observation", Value: "not_collected"}, supportdiag.Field{Name: "backup.observation", Value: "not_collected"}, supportdiag.Field{Name: "queue.observation", Value: "not_collected"})
	payload, err := json.Marshal(diagnosticReceipt{Bundle: bundle, Actor: actor, Expires: now.Add(15 * time.Minute).Unix()})
	if supportError(c, err) {
		return
	}
	proof := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(h.diagnosticMAC(payload))
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"bundle": bundle, "receipt": proof, "expires_at": now.Add(15 * time.Minute)})
}
