package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/mailspam"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailboxSpamFeedback implements the MAIL-002 user-correction endpoint:
// POST /api/v1/mailbox/spam/feedback (mailbox bearer session required),
// as well as explicit permanent sender allow/deny rules (WP07, I12).
//
// Every statement runs inside a MailboxTx (SET LOCAL urmail.tenant_id / urmail.mailbox_id):
// row-level security fails closed, so mailbox B and tenant 2 can never see
// or modify mailbox A's rules or training input.
type MailboxSpamFeedback struct {
	Pool      *db.Pool
	Reconcile ReconcileTrigger
}

// spamFeedbackRequest is the body for user spam/ham corrections.
type spamFeedbackRequest struct {
	MessageID   string `json:"message_id"`
	Folder      string `json:"folder"`
	Verdict     string `json:"verdict"`
	FromAddress string `json:"from_address"`
	Subject     string `json:"subject"`
}

// SenderRule represents one sender allow/deny policy entry.
type SenderRule struct {
	Scope   string `json:"scope"`
	Address string `json:"address"`
	Domain  string `json:"domain"`
	List    string `json:"list"`
}

// senderRuleRequest is the body for explicit sender rule addition/deletion.
type senderRuleRequest struct {
	Address string `json:"address"`
	Domain  string `json:"domain"`
	List    string `json:"list"`
}

const maxFeedbackSubject = 998

// Feedback applies one user correction.
func (h *MailboxSpamFeedback) Feedback(c *gin.Context) {
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "SPAM_FEEDBACK_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	var req spamFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "SPAM_FEEDBACK_INVALID")
		return
	}
	req.Verdict = strings.ToLower(strings.TrimSpace(req.Verdict))
	if req.Verdict != "spam" && req.Verdict != "ham" {
		authError(c, http.StatusBadRequest, "SPAM_FEEDBACK_INVALID")
		return
	}
	req.MessageID = strings.TrimSpace(req.MessageID)
	if len(req.MessageID) == 0 || len(req.MessageID) > 998 || strings.ContainsAny(req.MessageID, "\x00\r\n") {
		authError(c, http.StatusBadRequest, "SPAM_FEEDBACK_INVALID")
		return
	}
	req.Folder = strings.TrimSpace(req.Folder)
	if len(req.Folder) == 0 || len(req.Folder) > 1024 || strings.ContainsAny(req.Folder, "\x00\r\n") {
		authError(c, http.StatusBadRequest, "SPAM_FEEDBACK_INVALID")
		return
	}
	if len(req.Subject) > maxFeedbackSubject || strings.ContainsAny(req.Subject, "\x00\r\n") {
		authError(c, http.StatusBadRequest, "SPAM_FEEDBACK_INVALID")
		return
	}
	fromAddress := strings.ToLower(strings.TrimSpace(req.FromAddress))
	fromDomain := ""
	if !validFeedbackSender(fromAddress) {
		fromAddress = ""
	} else {
		fromDomain = senderDomain(fromAddress)
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	inserted, err := q.MailboxSpamInsertFeedback(ctx, store.MailboxSpamInsertFeedbackParams{
		TenantID:      session.TenantID,
		MailboxID:     session.MailboxID,
		MessageID:     req.MessageID,
		Folder:        req.Folder,
		Verdict:       req.Verdict,
		SenderAddress: fromAddress,
		SenderDomain:  fromDomain,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}

	if fromAddress != "" {
		keep, drop := "allow", "deny"
		if req.Verdict == "spam" {
			keep, drop = "deny", "allow"
		}
		if _, err = q.MailboxSpamSetSenderList(ctx, store.MailboxSpamSetSenderListParams{
			TenantID:  session.TenantID,
			MailboxID: session.MailboxID,
			Address:   fromAddress,
			Domain:    fromDomain,
			List:      keep,
		}); err != nil {
			authError(c, http.StatusServiceUnavailable, "DB_ERROR")
			return
		}
		if _, err = q.MailboxSpamRemoveSenderList(ctx, store.MailboxSpamRemoveSenderListParams{
			TenantID:  session.TenantID,
			MailboxID: session.MailboxID,
			Address:   fromAddress,
			Domain:    fromDomain,
			List:      drop,
		}); err != nil {
			authError(c, http.StatusServiceUnavailable, "DB_ERROR")
			return
		}
		if inserted > 0 {
			if err = applyTokenDeltas(ctx, q, session.TenantID, session.MailboxID, req.Subject, fromAddress, req.Verdict); err != nil {
				authError(c, http.StatusServiceUnavailable, "DB_ERROR")
				return
			}
		}
	}

	if err = tx.Commit(ctx); err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}
	mailspam.RecordFeedback(req.Verdict)
	c.JSON(http.StatusOK, gin.H{"status": "recorded", "verdict": req.Verdict, "application_state": "pending_configuration"})
}

// ListSenders returns the active allow/deny sender rules for this mailbox and tenant.
func (h *MailboxSpamFeedback) ListSenders(c *gin.Context) {
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "SPAM_FEEDBACK_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT scope, address, domain, list FROM mailbox_spam_senders WHERE tenant_id = $1 AND (mailbox_id = 0 OR mailbox_id = $2) ORDER BY address ASC, domain ASC`, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	defer rows.Close()

	rules := make([]SenderRule, 0)
	for rows.Next() {
		var r SenderRule
		if err := rows.Scan(&r.Scope, &r.Address, &r.Domain, &r.List); err != nil {
			authError(c, http.StatusServiceUnavailable, "DB_ERROR")
			return
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

// AddSender adds or updates a sender allow/deny rule.
func (h *MailboxSpamFeedback) AddSender(c *gin.Context) {
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "SPAM_FEEDBACK_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	var req senderRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "SPAM_RULE_INVALID")
		return
	}
	req.List = strings.ToLower(strings.TrimSpace(req.List))
	if req.List != "allow" && req.List != "deny" {
		authError(c, http.StatusBadRequest, "SPAM_RULE_INVALID")
		return
	}
	address := strings.ToLower(strings.TrimSpace(req.Address))
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	switch {
	case address != "":
		if !validFeedbackSender(address) {
			authError(c, http.StatusBadRequest, "SPAM_RULE_INVALID")
			return
		}
		if domain == "" {
			domain = senderDomain(address)
		}
	case domain != "":
		if !validation.IsValidHostname(domain) {
			authError(c, http.StatusBadRequest, "SPAM_RULE_INVALID")
			return
		}
	default:
		authError(c, http.StatusBadRequest, "SPAM_RULE_INVALID")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	opposite := "allow"
	if req.List == "allow" {
		opposite = "deny"
	}
	if _, err := q.MailboxSpamSetSenderList(ctx, store.MailboxSpamSetSenderListParams{
		TenantID:  session.TenantID,
		MailboxID: session.MailboxID,
		Address:   address,
		Domain:    domain,
		List:      req.List,
	}); err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	_, _ = q.MailboxSpamRemoveSenderList(ctx, store.MailboxSpamRemoveSenderListParams{
		TenantID:  session.TenantID,
		MailboxID: session.MailboxID,
		Address:   address,
		Domain:    domain,
		List:      opposite,
	})

	if err := tx.Commit(ctx); err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if h.Reconcile != nil {
		h.Reconcile.Trigger()
	}
	c.JSON(http.StatusOK, gin.H{"saved": true})
}

// DeleteSender removes a sender allow/deny rule.
func (h *MailboxSpamFeedback) DeleteSender(c *gin.Context) {
	if h.Pool == nil {
		authError(c, http.StatusServiceUnavailable, "SPAM_FEEDBACK_UNAVAILABLE")
		return
	}
	session, ok := middleware.GetMailboxSession(c)
	if !ok {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	var req senderRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Support query parameters as fallback
		req.Address = c.Query("address")
		req.Domain = c.Query("domain")
		req.List = c.Query("list")
	}
	req.List = strings.ToLower(strings.TrimSpace(req.List))
	address := strings.ToLower(strings.TrimSpace(req.Address))
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if address != "" && domain == "" {
		domain = senderDomain(address)
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tx, err := h.Pool.MailboxTx(ctx, session.TenantID, session.MailboxID)
	if err != nil {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	deleted, err := q.MailboxSpamRemoveSenderList(ctx, store.MailboxSpamRemoveSenderListParams{
		TenantID:  session.TenantID,
		MailboxID: session.MailboxID,
		Address:   address,
		Domain:    domain,
		List:      req.List,
	})
	if err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		authError(c, http.StatusServiceUnavailable, "DB_ERROR")
		return
	}
	if h.Reconcile != nil && deleted > 0 {
		h.Reconcile.Trigger()
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted > 0})
}

// RegisterMailboxSpamSenderRoutes registers sender allow/deny endpoints.
func RegisterMailboxSpamSenderRoutes(group *gin.RouterGroup, h *MailboxSpamFeedback) {
	group.GET("/spam/senders", h.ListSenders)
	group.POST("/spam/senders", h.AddSender)
	group.DELETE("/spam/senders", h.DeleteSender)
}

// applyTokenDeltas writes the learned token counts for one feedback event.
func applyTokenDeltas(ctx context.Context, q *store.Queries, tenantID, mailboxID int64, subject, fromAddress, verdict string) error {
	for token, delta := range mailspam.LearnFromFeedback(subject, fromAddress, verdict) {
		if _, err := q.MailboxSpamApplyTokenDelta(ctx, store.MailboxSpamApplyTokenDeltaParams{
			TenantID:  tenantID,
			MailboxID: mailboxID,
			Token:     token,
			SpamDelta: delta.Spam,
			HamDelta:  delta.Ham,
		}); err != nil {
			return err
		}
	}
	return nil
}

// validFeedbackSender accepts one concrete address (local@domain).
func validFeedbackSender(address string) bool {
	if len(address) < 6 || len(address) > 254 {
		return false
	}
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return false
	}
	if strings.ContainsAny(address, " \t\"'<>;,\\") || strings.ContainsAny(address, "\x00\r\n") {
		return false
	}
	for _, r := range address {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

// senderDomain returns the lowercased domain part of a validated address.
func senderDomain(address string) string {
	_, domain, ok := strings.Cut(address, "@")
	if !ok {
		return ""
	}
	return domain
}
