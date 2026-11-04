package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/airetrieval"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/aianswer"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// AISearchAsk serves POST /api/v1/mailbox/ai/search-ask: cross-mail Q&A
// answered strictly from the caller's own mailbox, with citations re-verified
// against live authorization before the response is returned. It reuses the
// mailbox session authentication of the AI draft routes; tenant admin and
// platform roles gain no implicit mailbox content access.
type AISearchAsk struct {
	Pool   *db.Pool
	Policy func(*gin.Context, webmail.Session, string, bool) bool
	// Store overrides the PostgreSQL chunk store in offline tests.
	Store    airetrieval.ChunkStore
	Dialer   webmail.Dialer
	Vault    webmail.CredentialVault
	ModelFor func(ctx context.Context, tenantID int64) (aianswer.Model, error)
	// Accounting records usage and enforces the tenant budget. The zero
	// value (nil Pool) disables accounting: offline tests are unaffected.
	Accounting        AISearchAskAccounting
	SessionModelFor   func(context.Context, webmail.Session, string) (aianswer.Model, error)
	ReserveForSession func(context.Context, webmail.Session, string, string) (*AIUsageReservation, error)
}

// Handle answers one question from the mailbox principal's indexed mail.
func (h *AISearchAsk) Handle(c *gin.Context) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	if (h.Pool == nil && h.Store == nil) || h.Dialer == nil || h.Vault == nil || h.ModelFor == nil {
		authError(c, http.StatusServiceUnavailable, "AI_UNAVAILABLE")
		return
	}
	var req struct {
		Question            string `json:"question"`
		ConfigurationDigest string `json:"configuration_digest"`
		Confirmed           bool   `json:"confirmed"`
		PreviewOnly         bool   `json:"preview_only"`
		ContentDigest       string `json:"content_digest"`
		Folder              string `json:"folder_enc"`
		After               string `json:"after"`
		Before              string `json:"before"`
		Sender              string `json:"sender"`
		Subject             string `json:"subject"`
		IncludeTrash        bool   `json:"include_trash"`
		IncludeSpam         bool   `json:"include_spam"`
		IncludeJunk         bool   `json:"include_junk"`
		IncludeDrafts       bool   `json:"include_drafts"`
		IncludeSpecial      bool   `json:"include_special"`
	}
	if !bindMailboxMessage(c, &req) {
		return
	}
	if h.Policy != nil && !h.Policy(c, session, req.ConfigurationDigest, req.Confirmed || req.PreviewOnly) {
		return
	}
	question, err := aianswer.Question(req.Question)
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	var scoped askScopeSource
	if req.Folder != "" {
		var err error
		scoped.folder, err = webmail.DecodeFolderName(req.Folder)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	if req.After != "" {
		value, err := time.Parse("2006-01-02", req.After)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		scoped.after = value
	}
	if req.Before != "" {
		value, err := time.Parse("2006-01-02", req.Before)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		scoped.before = value
	}
	if !scoped.after.IsZero() && !scoped.before.IsZero() && !scoped.after.Before(scoped.before) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	scoped.sender = strings.TrimSpace(req.Sender)
	scoped.subject = strings.TrimSpace(req.Subject)
	scoped.includeTrash = req.IncludeTrash || req.IncludeSpecial
	scoped.includeSpam = req.IncludeSpam || req.IncludeJunk || req.IncludeSpecial
	scoped.includeDrafts = req.IncludeDrafts || req.IncludeSpecial

	// Budget enforcement happens BEFORE any provider call; an exhausted
	// budget is a distinct, actionable error for the client.
	var reservation *AIUsageReservation
	var reserveErr error
	if !req.PreviewOnly {
		if h.ReserveForSession != nil {
			reservation, reserveErr = h.ReserveForSession(c.Request.Context(), session, question, req.ConfigurationDigest)
		} else {
			reservation, reserveErr = h.Accounting.Reserve(c.Request.Context(), session.TenantID, session.MailboxID, question)
		}
	}
	err = reserveErr
	if err != nil {
		if errors.Is(err, ErrAIBudgetExceeded) {
			authError(c, http.StatusTooManyRequests, "AI_BUDGET_EXCEEDED")
			return
		}
		if errors.Is(err, ErrNotAccounted) {
			reservation = nil
		} else {
			authError(c, http.StatusBadGateway, "AI_SEARCH_ASK_UNAVAILABLE")
			return
		}
	}
	settled := false
	modelCalled := false
	defer func() {
		if !settled {
			if modelCalled {
				reservation.Settle(context.Canceled, false, 0, 0, func(_, _ int64) (int64, error) { return 0, nil })
			} else {
				reservation.releaseUnused()
			}
		}
	}()
	model, err := h.ModelFor(c.Request.Context(), session.TenantID)
	if h.SessionModelFor != nil {
		model, err = h.SessionModelFor(c.Request.Context(), session, req.ConfigurationDigest)
	}
	if err != nil || model == nil {
		authError(c, http.StatusServiceUnavailable, "AI_UNAVAILABLE")
		return
	}
	source, closeSource, ok := h.dialSource(c, session)
	if !ok {
		return
	}
	defer closeSource()
	canonicalSource := source
	scoped.base = canonicalSource
	if native, ok := canonicalSource.(airetrieval.WebmailSource); ok {
		scoped.client = native.Client
	}
	source = scoped

	principal := airetrieval.Principal{TenantID: session.TenantID, MailboxID: session.MailboxID}
	chunkStore := h.chunkStore()
	// Incremental refresh first so deleted or expunged messages can never be
	// served from stale index rows. Index refresh is idempotent (content
	// digest comparison), so transient transport failures are retried with a
	// small capped backoff. Generation calls below are NEVER retried: the
	// user retries those.
	reindex := func() error {
		_, err := (airetrieval.Indexer{Store: chunkStore, Source: canonicalSource}).Reindex(c.Request.Context(), principal)
		return err
	}
	if err := ai.RetryIdempotent(c.Request.Context(), ai.RetryConfig{Attempts: 3, Base: 100 * time.Millisecond, Max: 500 * time.Millisecond, RetryIf: func(err error) bool {
		return errors.Is(err, context.DeadlineExceeded) || c.Request.Context().Err() == nil && ai.ClassifyFailure(err) != ai.ClassFatal && !errors.Is(err, context.Canceled)
	}}, reindex); err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return
	}
	chunkStore = askScopeStore{ChunkStore: chunkStore, source: scoped}
	previewStopped := errors.New("mailbox AI preview ready")
	policyDenied := errors.New("mailbox AI policy denied before export")
	consentChanged := errors.New("mailbox AI preview changed")
	var previewSources []aianswer.Source
	previewDigest := func(sources []aianswer.Source) string {
		data, _ := json.Marshal(struct {
			Configuration, Question, Folder, After, Before string
			Sender, Subject                                string
			IncludeTrash, IncludeSpam, IncludeDrafts       bool
			Sources                                        []aianswer.Source
		}{req.ConfigurationDigest, question, req.Folder, req.After, req.Before, req.Sender, req.Subject, scoped.includeTrash, scoped.includeSpam, scoped.includeDrafts, sources})
		return fmt.Sprintf("%x", sha256.Sum256(data))
	}
	countedModel := func(ctx context.Context, question string, sources []aianswer.Source) (string, []string, error) {
		previewSources = sources
		if req.PreviewOnly {
			return "", nil, previewStopped
		}
		if h.Policy != nil && (req.ContentDigest == "" || req.ContentDigest != previewDigest(sources)) {
			return "", nil, consentChanged
		}
		if h.Policy != nil && !h.Policy(c, session, req.ConfigurationDigest, req.Confirmed) {
			return "", nil, policyDenied
		}
		modelCalled = true
		return model(ctx, question, sources)
	}
	var observed ai.CompletionResponse
	askCtx := airetrieval.WithUsageRecorder(c.Request.Context(), func(usage ai.CompletionResponse) { observed = usage })
	answer, askErr := (airetrieval.Pipeline{Store: chunkStore, Source: source, Model: countedModel}).Ask(askCtx, principal, question)
	if errors.Is(askErr, policyDenied) {
		return
	}
	if req.PreviewOnly && (askErr == nil || errors.Is(askErr, previewStopped)) {
		c.JSON(200, gin.H{"content_digest": previewDigest(previewSources), "question": question, "sources": previewSources})
		return
	}
	if errors.Is(askErr, consentChanged) {
		authError(c, 409, "AI_CONTENT_CONSENT_STALE")
		return
	}
	// Usage recording is best-effort: it must never fail the delivered
	// answer. With no usage reported by the pipeline the reservation stays
	// charged at its worst-case estimate, matching platform accounting.
	settled = true
	if modelCalled {
		reservation.Settle(askErr, observed.UsageKnown, int64(observed.PromptTokens), int64(observed.OutputTokens), reservation.observedCost)
	} else {
		reservation.releaseUnused()
	}
	if askErr != nil {
		switch ai.ClassifyFailure(askErr) {
		case ai.ClassRateLimited:
			authError(c, http.StatusTooManyRequests, "AI_PROVIDER_RATE_LIMITED")
		case ai.ClassUnavailable:
			authError(c, http.StatusServiceUnavailable, "AI_PROVIDER_UNAVAILABLE")
		default:
			authError(c, http.StatusBadGateway, "AI_SEARCH_ASK_UNAVAILABLE")
		}
		return
	}
	if h.Policy != nil && !h.Policy(c, session, req.ConfigurationDigest, req.Confirmed) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"text": answer.Text, "citations": answer.Citations, "grounded": answer.Grounded})
}

func (h *AISearchAsk) dialSource(c *gin.Context, session webmail.Session) (airetrieval.MailSource, func(), bool) {
	tokenHash, _ := c.Get(middleware.ContextMailboxTokenHash)
	hash, _ := tokenHash.(string)
	password, ok := h.Vault.Get(hash)
	if !ok || password == "" {
		// Bearer still valid in PG but the IMAP secret is gone: re-login.
		authError(c, http.StatusUnauthorized, "MAILBOX_REAUTH_REQUIRED")
		return nil, nil, false
	}
	client, err := h.Dialer.Dial(c.Request.Context(), session.Address, password)
	if err != nil {
		authError(c, http.StatusBadGateway, "IMAP_UNAVAILABLE")
		return nil, nil, false
	}
	return airetrieval.WebmailSource{Client: client}, func() { _ = client.Close() }, true
}

// chunkStore returns the injected store or the PostgreSQL implementation.
func (h *AISearchAsk) chunkStore() airetrieval.ChunkStore {
	if h.Store != nil {
		return h.Store
	}
	return airetrieval.PGChunkStore{Pool: h.Pool}
}

// Status reports the retrieval index status for the current mailbox.
func (h *AISearchAsk) Status(c *gin.Context) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	principal := airetrieval.Principal{TenantID: session.TenantID, MailboxID: session.MailboxID}
	store := h.chunkStore()
	if store == nil {
		authError(c, http.StatusServiceUnavailable, "AI_UNAVAILABLE")
		return
	}
	states, err := store.MessageStates(c.Request.Context(), principal)
	if err != nil {
		authError(c, http.StatusBadGateway, "AI_UNAVAILABLE")
		return
	}
	foldersMap := map[string]int{}
	for key := range states {
		foldersMap[key.Folder]++
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":          true,
		"indexed_messages": len(states),
		"folders":          foldersMap,
		"status":           "ready",
	})
}

// Clear purges all indexed chunks for the current mailbox.
func (h *AISearchAsk) Clear(c *gin.Context) {
	session, ok := middleware.GetMailboxSession(c)
	if !ok || session.TenantID <= 0 || session.MailboxID <= 0 {
		authError(c, http.StatusUnauthorized, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	principal := airetrieval.Principal{TenantID: session.TenantID, MailboxID: session.MailboxID}
	store := h.chunkStore()
	if store == nil {
		authError(c, http.StatusServiceUnavailable, "AI_UNAVAILABLE")
		return
	}
	if err := store.PurgeChunks(c.Request.Context(), principal); err != nil {
		authError(c, http.StatusBadGateway, "AI_UNAVAILABLE")
		return
	}
	c.JSON(http.StatusOK, gin.H{"purged": true, "status": "cleared"})
}

type aiConfirmedTargetKey struct{}

// TenantCitationModel builds citation models from the tenant AI provider
// configuration stored in PostgreSQL.
func TenantCitationModel(pool *db.Pool, secrets SecretsBox, cloudEndpoints, localEndpoints []string) func(ctx context.Context, tenantID int64) (aianswer.Model, error) {
	return func(ctx context.Context, tenantID int64) (aianswer.Model, error) {
		tx, err := pool.PlatformTx(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		config, err := store.New(tx).PlatformGetAIProvider(ctx, tenantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ai.ErrProviderDisabled
		}
		if err != nil {
			return nil, err
		}
		if expected, _ := ctx.Value(aiConfirmedTargetKey{}).(string); expected != "" && expected != config.ConfigurationDigest {
			return nil, errAITargetChanged
		}
		if !config.Enabled {
			return nil, ai.ErrProviderDisabled
		}
		credential := ""
		if config.Provider != ai.ProviderOllama {
			if secrets == nil {
				return nil, ai.ErrProviderDisabled
			}
			if credential, err = secrets.DecryptString(config.EncryptedKey); err != nil {
				return nil, ai.ErrProviderDisabled
			}
		}
		provider, err := ai.ConfiguredProvider(config.Provider, config.Endpoint, config.Model, credential, cloudEndpoints, localEndpoints)
		if err != nil {
			return nil, err
		}
		return airetrieval.CitationModel(ai.WithBreaker(provider, "citation"), config.Model), nil
	}
}
