package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailspam"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailboxSpamFeedback implements the MAIL-002 user-correction endpoint:
// POST /api/v1/mailbox/spam/feedback (mailbox bearer session required).
//
// One call writes the feedback row and upserts the per-mailbox sender
// allow/deny entry derived from the message's From address, so the next
// recipient-scoped Sieve configuration for that mailbox honors the correction. Every statement
// runs inside a MailboxTx (SET LOCAL urmail.tenant_id / urmail.mailbox_id):
// row-level security fails closed, so mailbox B and tenant 2 can never see
// or modify mailbox A's rules or training input, even with a buggy
// predicate.
type MailboxSpamFeedback struct {
	// Pool is the tenant-scoped (urmail_app) pool. The platform pool is
	// deliberately NOT used: the RLS policies on the mailbox_spam_* tables
	// are the authoritative isolation boundary.
	Pool      *db.Pool
	Reconcile ReconcileTrigger
}

// spamFeedbackRequest is the documented body. from_address and subject are
// optional but recommended: the webmail client already displays them, and
// without a from_address only the feedback row (no sender rule) is written.
type spamFeedbackRequest struct {
	MessageID   string `json:"message_id"`
	Folder      string `json:"folder"`
	Verdict     string `json:"verdict"`
	FromAddress string `json:"from_address"`
	Subject     string `json:"subject"`
}

// maxFeedbackSubject bounds the optional learning input; it matches the
// RFC 5322 practical header limit the webmail client enforces.
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
	// The sender identity is normalized, never trusted structurally: an
	// invalid value writes the feedback row without a sender rule instead of
	// poisoning the policy tables.
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
		// The correction IS the policy: reporting spam denies the sender,
		// reporting ham allows it. The opposite-list entry is removed so the
		// latest verdict always wins deterministically.
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
		// Training input: bounded token deltas from the subject and sender
		// identity, scoped to this mailbox only.
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

// validFeedbackSender accepts one concrete address (local@domain), matching
// the strictness the allow/deny tables require for deterministic matching.
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
