package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/sieve"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailboxBridgeHandler accepts only authenticated service-to-service requests.
// It never consumes a panel session, user id, requested tenant id or mailbox id.
type MailboxBridgeHandler struct {
	Pool  *db.Pool
	Key   []byte
	AI    *MailboxAIService
	Sieve SieveActivator
}

type mailboxBridgeRequest struct {
	Action              string            `json:"action"`
	Address             string            `json:"address,omitempty"`
	Session             string            `json:"session,omitempty"`
	Ticket              string            `json:"ticket,omitempty"`
	Scope               string            `json:"scope,omitempty"`
	Folder              string            `json:"folder,omitempty"`
	UID                 int64             `json:"uid,omitempty"`
	UIDValidity         int64             `json:"uid_validity,omitempty"`
	RequestKey          string            `json:"request_key,omitempty"`
	Input               ai.OperationInput `json:"input,omitempty"`
	Rules               *sieve.Rules      `json:"rules,omitempty"`
	Confirmed           bool              `json:"confirmed,omitempty"`
	ConfigurationDigest string            `json:"configuration_digest,omitempty"`
	OldPassword         string            `json:"old_password,omitempty"`
	NewPassword         string            `json:"new_password,omitempty"`
	ExpectedVersion     string            `json:"expected_version,omitempty"`
}

// Bridge verifies the service proof before resolving a mailbox principal.
func (h *MailboxBridgeHandler) Bridge(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var tid, mid int64
	var input mailboxBridgeRequest
	rejectionAudited := false
	defer func() {
		if h.Pool == nil || c.Writer.Status() < 400 || rejectionAudited {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": mid, "status": c.Writer.Status(), "outcome": "rejected"})
		action := "mailbox.bridge.rejected"
		if input.Action == "change_password" {
			action = "mailbox.password.rejected"
		}
		_, auditErr := store.New(h.Pool).CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tid, Valid: tid > 0}, Action: action, TargetType: pgtype.Text{String: "mailbox", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(mid, 10), Valid: mid > 0}, Detail: detail})
		if auditErr != nil {
			middleware.RecordAuditFailure(false)
			zap.L().Error("write mailbox rejection audit", zap.Error(auditErr))
		}
	}()
	if h.Pool == nil || len(h.Key) < 32 {
		authError(c, 503, "MAILBOX_BRIDGE_DISABLED")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 262145))
	if err != nil || len(body) > 262144 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if mailboxprincipal.VerifyRequest(h.Key, c.Request.URL.Path, c.GetHeader("X-Urmail-Timestamp"), c.GetHeader("X-Urmail-Nonce"), c.GetHeader("X-Urmail-Signature"), body, time.Now()) != nil {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	if json.Unmarshal(body, &input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	tx, err := h.Pool.PlatformTx(c)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	defer func() { _ = tx.Rollback(c) }()
	q := store.New(tx)
	if q.PlatformPruneRoundcubeRequests(c) != nil || q.PlatformPruneMailboxTickets(c) != nil || q.PlatformPruneMailboxSessions(c) != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if nonceErr := q.PlatformRecordRoundcubeNonce(c, c.GetHeader("X-Urmail-Nonce")); nonceErr != nil {
		var pgErr *pgconn.PgError
		if !errors.As(nonceErr, &pgErr) || pgErr.Code != "23505" {
			authError(c, 503, "DB_ERROR")
			return
		}
		authError(c, 409, "MAILBOX_BRIDGE_REPLAY")
		return
	}
	var response any
	var afterCommit func() (any, error)
	switch input.Action {
	case "login":
		if len(input.Address) == 0 || len(input.Address) > 320 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		token, hash, tokenErr := mailboxprincipal.NewToken()
		if tokenErr != nil {
			authError(c, 500, "INTERNAL_ERROR")
			return
		}
		session, sessionErr := q.PlatformCreateMailboxSession(c, store.PlatformCreateMailboxSessionParams{TokenHash: hash, Address: input.Address})
		if sessionErr != nil {
			mailboxAuthorizationError(c, sessionErr)
			return
		}
		tid, mid = session.TenantID, session.MailboxID
		response = gin.H{"session": token, "expires_at": session.ExpiresAt}
	case "change_password":
		changed, passwordErr := changeMailboxPassword(c.Request.Context(), q, input)
		tid, mid = changed.TenantID, changed.MailboxID
		if passwordErr != nil {
			status, code := 503, "DB_ERROR"
			switch {
			case errors.Is(passwordErr, errMailboxPasswordPolicy):
				status, code = 400, "MAILBOX_PASSWORD_POLICY"
			case errors.Is(passwordErr, errMailboxPasswordCurrent):
				status, code = 403, "MAILBOX_PASSWORD_CURRENT_INVALID"
			case errors.Is(passwordErr, errMailboxPasswordConflict):
				status, code = 409, "MAILBOX_PASSWORD_CHANGED"
			case errors.Is(passwordErr, errMailboxPasswordRate):
				status, code = 429, "MAILBOX_PASSWORD_RATE_LIMITED"
			case errors.Is(passwordErr, pgx.ErrNoRows):
				status, code = 403, "MAILBOX_AUTHORIZATION_DENIED"
			}
			if tid > 0 && mid > 0 && status < 500 {
				detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": mid, "status": status, "outcome": "rejected"})
				if _, auditErr := q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tid, Valid: true}, Action: "mailbox.password.rejected", TargetType: pgtype.Text{String: "mailbox", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(mid, 10), Valid: true}, Detail: detail}); auditErr != nil {
					middleware.RecordAuditFailure(true)
					authError(c, 503, "AUDIT_UNAVAILABLE")
					return
				}
				if tx.Commit(c) != nil {
					authError(c, 503, "DB_ERROR")
					return
				}
				rejectionAudited = true
			}
			authError(c, status, code)
			return
		}

		response = gin.H{"changed": true, "reauthentication_required": true, "imap_revocation": "pending"}
	case "logout":
		session, sessionErr := q.PlatformGetMailboxSessionForRevocation(c, mailboxprincipal.HashToken(input.Session))
		if errors.Is(sessionErr, pgx.ErrNoRows) {
			response = gin.H{"revoked": true}
			break
		}
		if sessionErr != nil {
			mailboxAuthorizationError(c, sessionErr)
			return
		}
		tid, mid = session.TenantID, session.MailboxID
		if q.PlatformRevokeMailboxSession(c, session.TokenHash) != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		response = gin.H{"revoked": true}
	case "ai_policy":
		session, sessionErr := q.PlatformGetMailboxSession(c, mailboxprincipal.HashToken(input.Session))
		if sessionErr != nil {
			mailboxAuthorizationError(c, sessionErr)
			return
		}
		tid, mid = session.TenantID, session.MailboxID
		cfg, cfgErr := q.PlatformGetAIProvider(c, tid)
		switch {
		case errors.Is(cfgErr, pgx.ErrNoRows):
			response = gin.H{"allowed": false, "scope_hash": AICloudScopeHash, "scope_key": "page.ai.cloud_scope"}
		case cfgErr != nil:
			authError(c, 503, "DB_ERROR")
			return
		default:
			response, cfgErr = aiTargetPolicy(c, q, cfg)
			if cfgErr != nil {
				authError(c, 503, "DB_ERROR")
				return
			}
		}
	case "authorize":
		if !mailboxprincipal.ValidReference(input.Scope, input.Folder, input.UID, input.UIDValidity) {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		token, hash, tokenErr := mailboxprincipal.NewToken()
		if tokenErr != nil {
			authError(c, 500, "INTERNAL_ERROR")
			return
		}
		ticket, ticketErr := q.PlatformCreateMailboxTicket(c, store.PlatformCreateMailboxTicketParams{TokenHash: hash, Scope: input.Scope, Folder: input.Folder, MessageUid: input.UID, UidValidity: input.UIDValidity, SessionHash: mailboxprincipal.HashToken(input.Session)})
		if ticketErr != nil {
			mailboxAuthorizationError(c, ticketErr)
			return
		}
		tid, mid = ticket.TenantID, ticket.MailboxID
		response = gin.H{"ticket": token, "scope": ticket.Scope, "expires_at": ticket.ExpiresAt}
	case "consume", "ai", "get_sieve", "set_sieve":
		if (input.Action == "ai" && input.Scope != "ai_context") || ((input.Action == "get_sieve" || input.Action == "set_sieve") && input.Scope != "sieve") {
			authError(c, 403, "MAILBOX_AUTHORIZATION_DENIED")
			return
		}
		if !mailboxprincipal.ValidReference(input.Scope, input.Folder, input.UID, input.UIDValidity) {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		principal, consumeErr := q.PlatformConsumeMailboxTicket(c, store.PlatformConsumeMailboxTicketParams{TokenHash: mailboxprincipal.HashToken(input.Ticket), Scope: input.Scope, Folder: input.Folder, MessageUid: input.UID, UidValidity: input.UIDValidity})
		if consumeErr != nil {
			mailboxAuthorizationError(c, consumeErr)
			return
		}
		tid, mid = principal.TenantID, principal.MailboxID
		response = gin.H{"principal": principal}
		switch input.Action {
		case "ai":
			if h.AI == nil {
				authError(c, 503, "AI_DISABLED")
				return
			}
			afterCommit = func() (any, error) {
				result, runErr := h.AI.Run(c.Request.Context(), principal, input.RequestKey, input.Input, input.ConfigurationDigest)
				return gin.H{"result": result}, runErr
			}
		case "get_sieve":
			mailbox, getErr := q.PlatformGetMailboxSieve(c, store.PlatformGetMailboxSieveParams{TenantID: tid, ID: mid})
			if getErr != nil {
				mailboxAuthorizationError(c, getErr)
				return
			}
			rules := sieve.Rules{}
			if mailbox.SieveScript.Valid && mailbox.SieveScript.String != "" {
				rules, getErr = sieve.Decode(mailbox.SieveScript.String)
				if getErr != nil {
					authError(c, 409, "SIEVE_SOURCE_INVALID")
					return
				}
			}
			response = gin.H{"rules": rules, "state": "desired", "version": sieve.Digest(mailbox.SieveScript.String)}
		case "set_sieve":
			if !input.Confirmed || input.Rules == nil {
				authError(c, 400, "SIEVE_CONFIRMATION_REQUIRED")
				return
			}
			if h.Sieve == nil {
				authError(c, 503, "SIEVE_DISABLED")
				return
			}
			mailbox, versionErr := q.PlatformGetMailboxSieve(c, store.PlatformGetMailboxSieveParams{TenantID: tid, ID: mid})
			if versionErr != nil || len(input.ExpectedVersion) != 64 || input.ExpectedVersion != sieve.Digest(mailbox.SieveScript.String) {
				authError(c, 409, "SIEVE_VERSION_CONFLICT")
				return
			}
			script, compileErr := sieve.Compile(*input.Rules)
			if compileErr != nil {
				authError(c, 400, "SIEVE_RULES_INVALID")
				return
			}
			changed, saveErr := q.PlatformSetMailboxSieve(c, store.PlatformSetMailboxSieveParams{TenantID: tid, ID: mid, SieveScript: pgtype.Text{String: script, Valid: true}, ExpectedScript: mailbox.SieveScript.String})
			if saveErr == nil && changed != 1 {
				authError(c, 409, "SIEVE_VERSION_CONFLICT")
				return
			}
			if saveErr != nil {
				authError(c, 503, "DB_ERROR")
				return
			}
			afterCommit = func() (any, error) {
				applyErr := h.Sieve.ActivateSieve(c.Request.Context(), tid, mid, sieve.Digest(script))
				return gin.H{"rules": input.Rules, "state": "active", "version": sieve.Digest(script)}, applyErr
			}
		}
	default:
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	phase := "authorization"
	scope := input.Scope
	var targetID pgtype.Text
	if input.Action == "change_password" {
		scope = ""
		phase = "credentials"
		targetID = pgtype.Text{String: strconv.FormatInt(mid, 10), Valid: mid > 0}
	}
	detail, _ := json.Marshal(gin.H{"actor_type": "mailbox", "mailbox_id": mid, "scope": scope, "status": 200, "outcome": "succeeded", "phase": phase})
	if _, err = q.CreateAuditLog(c, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tid, Valid: tid > 0}, Action: "mailbox." + input.Action, TargetType: pgtype.Text{String: "mailbox", Valid: true}, TargetID: targetID, Detail: detail}); err != nil {
		middleware.RecordAuditFailure(true)
		authError(c, 503, "AUDIT_UNAVAILABLE")
		return
	}
	if tx.Commit(c) != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if afterCommit != nil {
		response, err = afterCommit()
		if err != nil {
			switch {
			case errors.Is(err, errAIBudget):
				authError(c, 429, "AI_QUOTA_UNAVAILABLE")
			case errors.Is(err, errAIIntent):
				authError(c, 409, "AI_REQUEST_EXISTS")
			case errors.Is(err, ai.ErrInvalidRequest):
				authError(c, 400, "INVALID_PARAMS")
			case errors.Is(err, errAIConsent):
				authError(c, 403, "AI_CLOUD_CONSENT_REQUIRED")
			case errors.Is(err, errAITargetChanged):
				authError(c, 409, "AI_CLOUD_CONSENT_STALE")
			case errors.Is(err, ai.ErrProviderDisabled) || errors.Is(err, ai.ErrLocalOnly) || errors.Is(err, ai.ErrUnauthorized):
				authError(c, 403, "AI_DISABLED")
			default:
				authError(c, 502, "MAILBOX_OPERATION_FAILED")
			}
			return
		}
		if !recordSieveAudit(c, store.New(h.Pool), tid, mid, "mailbox."+input.Action+".completed") {
			return
		}
	}
	c.JSON(http.StatusOK, response)
}

func mailboxAuthorizationError(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 403, "MAILBOX_AUTHORIZATION_DENIED")
	} else {
		authError(c, 503, "DB_ERROR")
	}
}
