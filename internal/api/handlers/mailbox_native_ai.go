package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"golang.org/x/net/html"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/airetrieval"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/aianswer"
	"github.com/bushixiucai/urmail/internal/domain/mailboxsettings"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// NativeMailboxAI reuses the authoritative mailbox principal and AI ledger.
// Neither proposed recipients nor sending actions exist in the model schema.
type NativeMailboxAI struct {
	Native  *MailboxNativeHandler
	Service *MailboxAIService
	Search  *AISearchAsk
}

// RegisterNativeMailboxAI binds owner-scoped native AI actions.
func RegisterNativeMailboxAI(group *gin.RouterGroup, native *MailboxNativeHandler, service *MailboxAIService, search *AISearchAsk) {
	h := &NativeMailboxAI{Native: native, Service: service, Search: search}
	group.GET("/ai/target", h.Target)
	group.GET("/ai/context", h.Context)
	group.POST("/ai/assist", h.Assist)
	group.GET("/ai/citations/:id", h.Citation)
	group.POST("/ai/models", h.Models)
	if search != nil {
		search.Policy = h.requireTarget
		search.ReserveForSession = func(ctx context.Context, sess webmail.Session, question, expected string) (*AIUsageReservation, error) {
			cfg, custom, err := h.getUserAI(ctx, sess)
			if err != nil {
				return nil, err
			}
			if !custom {
				return search.Accounting.Reserve(ctx, sess.TenantID, sess.MailboxID, question)
			}
			if cfg.Digest != expected {
				return nil, errAITargetChanged
			}
			return search.Accounting.ReserveCustom(ctx, sess.TenantID, sess.MailboxID, question, cfg)
		}

		search.SessionModelFor = func(ctx context.Context, sess webmail.Session, expected string) (aianswer.Model, error) {
			cfg, custom, err := h.getUserAI(ctx, sess)
			if err != nil {
				return nil, err
			}
			if !custom {
				return search.ModelFor(context.WithValue(ctx, aiConfirmedTargetKey{}, expected), sess.TenantID)
			}
			if cfg.Digest != expected {
				return nil, errAITargetChanged
			}
			provider, err := h.customProvider(cfg)
			if err != nil {
				return nil, err
			}
			return airetrieval.CitationModel(provider, cfg.Model), nil
		}
	}
}

type nativeAIInput struct {
	Operation           string  `json:"operation"`
	ConfigurationDigest string  `json:"configuration_digest"`
	Confirmed           bool    `json:"confirmed"`
	PreviewOnly         bool    `json:"preview_only"`
	ContentDigest       string  `json:"content_digest"`
	RequestKey          string  `json:"request_key"`
	Folder              string  `json:"folder_enc"`
	UID                 uint32  `json:"uid"`
	UIDValidity         uint32  `json:"uid_validity"`
	Selection           *string `json:"selection"`
	Draft               string  `json:"draft"`
	Body                string  `json:"body"`
	Subject             string  `json:"subject"`
	Instruction         string  `json:"instruction"`
	Language            string  `json:"target_language"`
}

type userAICustomConfig struct {
	mailboxsettings.AIConfig
	Digest string
}

func (h *NativeMailboxAI) getUserAI(ctx context.Context, sess webmail.Session) (*userAICustomConfig, bool, error) {
	if h.Service == nil || h.Service.Pool == nil {
		return nil, false, nil
	}
	row, err := store.New(h.Service.Pool).PlatformGetMailboxSettings(ctx, store.PlatformGetMailboxSettingsParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var prefs struct {
		AI *mailboxsettings.AIConfig `json:"ai"`
	}
	if err = json.Unmarshal(row.Preferences, &prefs); err != nil {
		return nil, false, err
	}
	if prefs.AI == nil || !prefs.AI.UseCustom {
		return nil, false, nil
	}
	raw, _ := json.Marshal(prefs.AI) //nolint:gosec // G117: Hash/validate the persisted encrypted configuration; never serialize it to HTTP.
	cfg, err := mailboxsettings.ParseAI(raw)
	if err != nil {
		return nil, false, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(append(raw, []byte(fmt.Sprint(row.Version))...)))
	if cfg.APIKey != "" {
		if h.Service.Secrets == nil || !strings.HasPrefix(cfg.APIKey, "enc:v1:") {
			return nil, false, ai.ErrProviderDisabled
		}
		cfg.APIKey, err = h.Service.Secrets.DecryptString(cfg.APIKey)
		if err != nil {
			return nil, false, err
		}
	}
	return &userAICustomConfig{AIConfig: cfg, Digest: digest}, true, nil
}

func (h *NativeMailboxAI) customProvider(cfg *userAICustomConfig) (ai.Provider, error) {
	if cfg.Provider == ai.ProviderOllama {
		return ai.ConfiguredProvider(cfg.Provider, cfg.Endpoint, cfg.Model, "", nil, h.Service.LocalEndpoints)
	}
	return ai.DynamicProvider(cfg.Provider, cfg.Endpoint, cfg.Model, cfg.APIKey)
}

func (h *NativeMailboxAI) customGovernance(c *gin.Context, sess webmail.Session, cfg *userAICustomConfig) bool {
	governance, err := store.New(h.Service.Pool).PlatformGetAIProvider(c, sess.TenantID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		authError(c, 503, "AI_UNAVAILABLE")
		return false
	}
	personalMode := "public"
	var approvedEndpoints []string
	localOnly := false
	if err == nil {
		if governance.PersonalMode != "" {
			personalMode = governance.PersonalMode
		}
		approvedEndpoints = governance.ApprovedEndpoints
		localOnly = governance.LocalOnly
	}

	switch personalMode {
	case "disabled":
		authError(c, 403, "AI_PERSONAL_BYOK_DISABLED")
		return false
	case "approved":
		targetEndpoint := strings.TrimRight(cfg.Endpoint, "/")
		allowed := false
		for _, ep := range approvedEndpoints {
			if strings.TrimRight(ep, "/") == targetEndpoint {
				allowed = true
				break
			}
		}
		if !allowed {
			authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
			return false
		}
	case "public":
		if cfg.Provider != ai.ProviderOllama && !strings.HasPrefix(cfg.Endpoint, "https://") {
			authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
			return false
		}
	default:
		authError(c, 403, "AI_PERSONAL_BYOK_DISABLED")
		return false
	}

	if localOnly && cfg.Provider != ai.ProviderOllama {
		authError(c, 403, "AI_LOCAL_ONLY")
		return false
	}
	return true
}

func (h *NativeMailboxAI) principal(c *gin.Context) (webmail.Session, bool) {
	sess, ok := middleware.GetMailboxSession(c)
	if !ok || sess.TenantID <= 0 || sess.MailboxID <= 0 {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return sess, false
	}
	if h.Native == nil || !h.Native.Enabled || h.Service == nil || h.Service.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return sess, false
	}
	return sess, true
}

// Target describes the current provider without exposing credentials.
func (h *NativeMailboxAI) Target(c *gin.Context) {
	sess, ok := h.principal(c)
	if !ok {
		return
	}
	userAI, custom, configErr := h.getUserAI(c.Request.Context(), sess)
	if configErr != nil {
		authError(c, 503, "AI_UNAVAILABLE")
		return
	}
	if custom {
		if !h.customGovernance(c, sess, userAI) {
			return
		}
		if _, err := h.customProvider(userAI); err != nil {
			authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
			return
		}
		digest := userAI.Digest
		c.JSON(200, gin.H{
			"enabled":               true,
			"allowed":               true,
			"is_custom":             true,
			"provider":              userAI.Provider,
			"model":                 userAI.Model,
			"endpoint":              userAI.Endpoint,
			"configuration_digest":  digest,
			"scope_hash":            AICloudScopeHash,
			"requires_confirmation": true,
		})
		return
	}
	cfg, err := store.New(h.Service.Pool).PlatformGetAIProvider(c, sess.TenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, gin.H{"enabled": false, "allowed": false})
		return
	}
	if err != nil {
		authError(c, 503, "AI_UNAVAILABLE")
		return
	}
	target, err := aiTargetPolicy(c, store.New(h.Service.Pool), cfg)
	if err != nil {
		authError(c, 503, "AI_UNAVAILABLE")
		return
	}
	target["enabled"] = cfg.Enabled
	c.JSON(200, target)
}
func (h *NativeMailboxAI) requireTarget(c *gin.Context, sess webmail.Session, digest string, confirmed bool) bool {
	if h.Native != nil && h.Native.Sessions != nil {
		current, err := h.Native.Sessions.Get(c, sess.TokenHash)
		if err != nil || current.TenantID != sess.TenantID || current.MailboxID != sess.MailboxID {
			authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
			return false
		}
	}
	if !confirmed {
		authError(c, 403, "AI_CONTENT_CONSENT_REQUIRED")
		return false
	}
	if h.Service == nil || h.Service.Pool == nil {
		authError(c, 503, "AI_DISABLED")
		return false
	}
	userAI, custom, configErr := h.getUserAI(c.Request.Context(), sess)
	if configErr != nil {
		authError(c, 503, "AI_UNAVAILABLE")
		return false
	}
	if custom {
		if digest == "" || digest != userAI.Digest {
			authError(c, 409, "AI_CLOUD_CONSENT_STALE")
			return false
		}
		if !h.customGovernance(c, sess, userAI) {
			return false
		}
		if _, err := h.customProvider(userAI); err != nil {
			authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
			return false
		}
		return true
	}
	cfg, err := store.New(h.Service.Pool).PlatformGetAIProvider(c, sess.TenantID)
	if err != nil || !cfg.Enabled {
		authError(c, 503, "AI_DISABLED")
		return false
	}
	if digest == "" || digest != cfg.ConfigurationDigest {
		authError(c, 409, "AI_CLOUD_CONSENT_STALE")
		return false
	}
	target, err := aiTargetPolicy(c, store.New(h.Service.Pool), cfg)
	if err != nil {
		authError(c, 503, "AI_UNAVAILABLE")
		return false
	}
	if allowed, _ := target["allowed"].(bool); !allowed {
		authError(c, 403, "AI_CLOUD_CONSENT_REQUIRED")
		return false
	}
	return true
}
func plainAIMessage(msg *webmail.Message) string {
	if msg.Text != "" {
		return msg.Text
	}
	tokenizer := html.NewTokenizer(strings.NewReader(msg.HTML))
	var out strings.Builder
	skip := 0
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.StartTagToken || kind == html.EndTagToken {
			token := tokenizer.Token()
			if token.Data == "script" || token.Data == "style" {
				if kind == html.StartTagToken {
					skip++
				} else if skip > 0 {
					skip--
				}
			}
		}
		if kind == html.TextToken && skip == 0 {
			out.WriteString(tokenizer.Token().Data)
		}
	}
	return out.String()
}
func (h *NativeMailboxAI) readContext(c *gin.Context, folderEnc string, uid, validity uint32) (string, *webmail.Message, bool) {
	folder, err := webmail.DecodeFolderName(folderEnc)
	if err != nil || uid == 0 || validity == 0 {
		authError(c, 400, "INVALID_PARAMS")
		return "", nil, false
	}
	client, _, ok := h.Native.dialIMAP(c)
	if !ok {
		return "", nil, false
	}
	defer func() { _ = client.Close() }()
	current, err := client.FolderUIDValidity(c, folder)
	if err != nil || current != validity {
		authError(c, 409, "AI_SOURCE_CHANGED")
		return "", nil, false
	}
	msg, err := client.GetMessage(c, folder, uid)
	if err != nil || msg == nil {
		authError(c, 404, "MESSAGE_NOT_FOUND")
		return "", nil, false
	}
	if msg.UIDValidity != 0 && msg.UIDValidity != validity {
		authError(c, 409, "AI_SOURCE_CHANGED")
		return "", nil, false
	}
	return folder, msg, true
}

// Context previews canonical message text before processing.
func (h *NativeMailboxAI) Context(c *gin.Context) {
	if _, ok := h.principal(c); !ok {
		return
	}
	uid, e1 := strconv.ParseUint(c.Query("uid"), 10, 32)
	validity, e2 := strconv.ParseUint(c.Query("uid_validity"), 10, 32)
	if e1 != nil || e2 != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	_, msg, ok := h.readContext(c, c.Query("folder_enc"), uint32(uid), uint32(validity))
	if !ok {
		return
	}
	body := plainAIMessage(msg)
	if len(body) > 65536 {
		body = string([]rune(body)[:min(len([]rune(body)), 12000)])
	}
	c.JSON(200, gin.H{"subject": msg.Subject, "body": body, "truncated": len(body) < len(plainAIMessage(msg))})
}

// Assist generates a suggestion after explicit owner consent.
func (h *NativeMailboxAI) Assist(c *gin.Context) {
	sess, ok := h.principal(c)
	if !ok {
		return
	}
	var req nativeAIInput
	if !bindMailboxMessage(c, &req) {
		return
	}
	userAI, custom, configErr := h.getUserAI(c.Request.Context(), sess)
	if configErr != nil {
		authError(c, 503, "AI_UNAVAILABLE")
		return
	}
	if custom && userAI.Digest != req.ConfigurationDigest {
		authError(c, 409, "AI_CLOUD_CONSENT_STALE")
		return
	}
	if !h.requireTarget(c, sess, req.ConfigurationDigest, req.Confirmed || req.PreviewOnly) {
		return
	}
	input := ai.OperationInput{Operation: req.Operation, Body: req.Body, Subject: req.Subject, TargetLanguage: req.Language, Instruction: req.Instruction, Draft: req.Draft}
	principal := store.PlatformConsumeMailboxTicketRow{TenantID: sess.TenantID, MailboxID: sess.MailboxID, SessionHash: sess.TokenHash, Address: sess.Address, Scope: "ai_compose"}
	var digest string
	if req.UID != 0 {
		folder, msg, ok := h.readContext(c, req.Folder, req.UID, req.UIDValidity)
		if !ok {
			return
		}
		fullBody := plainAIMessage(msg)
		digest = airetrieval.ContentDigest(fullBody)
		selected := fullBody
		if req.Selection != nil {
			if strings.TrimSpace(*req.Selection) == "" {
				authError(c, 400, "AI_SELECTED_CONTENT_REQUIRED")
				return
			}
			if !strings.Contains(fullBody, *req.Selection) {
				authError(c, 409, "AI_SOURCE_CHANGED")
				return
			}
			selected = *req.Selection
		}
		// F06 / D08: Reading context must NOT stuff unselected body into Draft!
		// Selected text (req.Selection) must be verified against message body; unselected body must NOT be sent to model.
		// Draft field must only be used if there is an actual draft being polished/replied to.
		input.Draft = ""
		if req.Operation == "draft_reply" || req.Operation == "polish" {
			candidateDraft := req.Draft
			if candidateDraft == "" && req.Body != "" && req.Body != fullBody && req.Body != selected && !strings.Contains(req.Body, fullBody) {
				candidateDraft = req.Body
			}
			if candidateDraft != "" && candidateDraft != fullBody && candidateDraft != selected && !strings.Contains(candidateDraft, fullBody) {
				if req.Selection == nil || !strings.Contains(fullBody, candidateDraft) {
					input.Draft = candidateDraft
				}
			}
		}
		input.Subject = msg.Subject
		input.Body = selected
		if len(msg.From) > 0 {
			input.From = msg.From[0].Email
		}
		principal.Scope = "ai_context"
		principal.Folder = folder
		principal.MessageUid = int64(req.UID)
		principal.UidValidity = int64(req.UIDValidity)
	} else if req.Operation != "draft_new" && req.Operation != "polish" && req.Operation != "translate" && req.Operation != "summarize" && req.Operation != "classify" && req.Operation != "phishing" {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if req.Operation == "draft_new" && input.Body == "" {
		input.Body = req.Instruction
	}
	previewRequest, buildErr := ai.BuildOperation(input)
	if buildErr != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	previewRaw, _ := json.Marshal(struct {
		Configuration string
		Input         ai.OperationInput
		Folder        string
		UID, Validity uint32
	}{req.ConfigurationDigest, input, req.Folder, req.UID, req.UIDValidity})
	contentDigest := fmt.Sprintf("%x", sha256.Sum256(previewRaw))
	if req.PreviewOnly {
		provider, model, endpoint := "", "", ""
		if custom {
			provider, model, endpoint = userAI.Provider, userAI.Model, userAI.Endpoint
		} else {
			cfg, err := store.New(h.Service.Pool).PlatformGetAIProvider(c, sess.TenantID)
			if err != nil {
				authError(c, 503, "AI_UNAVAILABLE")
				return
			}
			if !cfg.Enabled || cfg.ConfigurationDigest != req.ConfigurationDigest {
				authError(c, 409, "AI_CLOUD_CONSENT_STALE")
				return
			}
			provider, model, endpoint = cfg.Provider, cfg.Model, cfg.Endpoint
		}
		c.JSON(200, gin.H{"content_digest": contentDigest, "messages": previewRequest.Messages, "provider": provider, "model": model, "endpoint": endpoint})
		return
	}
	if req.ContentDigest == "" || req.ContentDigest != contentDigest {
		authError(c, 409, "AI_CONTENT_CONSENT_STALE")
		return
	}
	if custom {
		if !h.requireTarget(c, sess, req.ConfigurationDigest, req.Confirmed) {
			return
		}
		result, err := h.Service.RunCustom(c.Request.Context(), principal, req.RequestKey, input, userAI, req.ConfigurationDigest)
		if err != nil {
			nativeAIError(c, err)
			return
		}
		if !h.verifyResultSource(c, sess, req, digest) {
			return
		}
		c.JSON(200, gin.H{"result": result})
		return
	}

	result, err := h.Service.Run(c.Request.Context(), principal, req.RequestKey, input, req.ConfigurationDigest)
	if err != nil {
		nativeAIError(c, err)
		return
	}
	if !h.verifyResultSource(c, sess, req, digest) {
		return
	}
	c.JSON(200, gin.H{"result": result})
}
func (h *NativeMailboxAI) verifyResultSource(c *gin.Context, sess webmail.Session, req nativeAIInput, digest string) bool {
	if !h.requireTarget(c, sess, req.ConfigurationDigest, req.Confirmed) {
		return false
	}
	if req.UID != 0 {
		_, msg, ok := h.readContext(c, req.Folder, req.UID, req.UIDValidity)
		if !ok {
			return false
		}
		if airetrieval.ContentDigest(plainAIMessage(msg)) != digest {
			authError(c, 409, "AI_SOURCE_CHANGED")
			return false
		}
	}
	return true
}
func nativeAIError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, mailboxsettings.ErrAIPersonalBYOKDisabled):
		authError(c, 403, "AI_PERSONAL_BYOK_DISABLED")
	case errors.Is(err, mailboxsettings.ErrAIEndpointNotApproved):
		authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
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
	case errors.Is(err, ai.ErrLocalOnly):
		authError(c, 403, "AI_LOCAL_ONLY")
	case errors.Is(err, ai.ErrProviderDisabled), errors.Is(err, ai.ErrUnauthorized):
		authError(c, 403, "AI_DISABLED")
	default:
		authError(c, 502, "AI_UNAVAILABLE")
	}
}

// Citation resolves a currently authorized live source.
func (h *NativeMailboxAI) Citation(c *gin.Context) {
	sess, ok := h.principal(c)
	if !ok {
		return
	}
	if h.Search == nil {
		authError(c, 503, "AI_UNAVAILABLE")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	p := airetrieval.Principal{TenantID: sess.TenantID, MailboxID: sess.MailboxID}
	chunk, found, err := h.Search.chunkStore().GetChunk(c, p, id)
	if err != nil || !found {
		authError(c, 404, "AI_SOURCE_CHANGED")
		return
	}
	source, closeSource, ok := h.Search.dialSource(c, sess)
	if !ok {
		return
	}
	defer closeSource()
	validity, uids, err := source.MessageUIDs(c, chunk.Folder)
	if err != nil || validity != chunk.UIDValidity {
		authError(c, 404, "AI_SOURCE_CHANGED")
		return
	}
	live := false
	for _, uid := range uids {
		if uid == chunk.UID {
			live = true
			break
		}
	}
	if !live {
		authError(c, 404, "AI_SOURCE_CHANGED")
		return
	}
	text, err := source.MessageText(c, chunk.Folder, chunk.UID)
	if err != nil || airetrieval.ContentDigest(text) != chunk.Digest {
		authError(c, 404, "AI_SOURCE_CHANGED")
		return
	}
	quote := chunk.Content
	if len([]rune(quote)) > 300 {
		quote = string([]rune(quote)[:300])
	}
	c.JSON(200, gin.H{"id": strconv.FormatInt(id, 10), "folder_enc": webmail.EncodeFolderName(chunk.Folder), "uid": chunk.UID, "uid_validity": chunk.UIDValidity, "quote": quote})
}

// Models tests provider connectivity and retrieves available models for mailbox users.
func (h *NativeMailboxAI) Models(c *gin.Context) {
	sess, ok := h.principal(c)
	if !ok {
		return
	}
	var input struct {
		Provider string `json:"provider"`
		Endpoint string `json:"endpoint"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
	}
	_ = c.ShouldBindJSON(&input)

	var provider ai.Provider
	var err error

	if input.Endpoint != "" {
		if input.Provider == "" {
			input.Provider = "openai"
		}
		if input.Model == "" {
			input.Model = "gpt-4o"
		}
		governance, gErr := store.New(h.Service.Pool).PlatformGetAIProvider(c, sess.TenantID)
		if gErr == nil {
			switch governance.PersonalMode {
			case "disabled":
				authError(c, 403, "AI_PERSONAL_BYOK_DISABLED")
				return
			case "approved":
				targetEndpoint := strings.TrimRight(input.Endpoint, "/")
				allowed := false
				for _, ep := range governance.ApprovedEndpoints {
					if strings.TrimRight(ep, "/") == targetEndpoint {
						allowed = true
						break
					}
				}
				if !allowed {
					authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
					return
				}
			}
			if governance.LocalOnly && input.Provider != ai.ProviderOllama {
				authError(c, 403, "AI_LOCAL_ONLY")
				return
			}
		}
		if input.APIKey == "" && input.Provider != ai.ProviderOllama {
			saved, custom, configErr := h.getUserAI(c.Request.Context(), sess)
			if configErr != nil {
				authError(c, 503, "AI_UNAVAILABLE")
				return
			}
			if custom && saved.Provider == input.Provider && strings.TrimRight(saved.Endpoint, "/") == strings.TrimRight(input.Endpoint, "/") {
				input.APIKey = saved.APIKey
			} else if gErr == nil && governance.Provider == input.Provider && strings.TrimRight(governance.Endpoint, "/") == strings.TrimRight(input.Endpoint, "/") && governance.EncryptedKey != "" && h.Service.Secrets != nil {
				decrypted, decErr := h.Service.Secrets.DecryptString(governance.EncryptedKey)
				if decErr == nil {
					input.APIKey = decrypted
				}
			}
		}

		if input.Provider == ai.ProviderOllama {
			provider, err = ai.ConfiguredProvider(input.Provider, input.Endpoint, input.Model, "", nil, h.Service.LocalEndpoints)
		} else {
			provider, err = ai.DynamicProvider(input.Provider, input.Endpoint, input.Model, input.APIKey)
		}
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	} else if userAI, custom, configErr := h.getUserAI(c.Request.Context(), sess); configErr != nil {
		authError(c, 503, "AI_UNAVAILABLE")
		return
	} else if custom {
		if !h.customGovernance(c, sess, userAI) {
			return
		}
		provider, err = h.customProvider(userAI)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	} else {
		cfg, err := store.New(h.Service.Pool).PlatformGetAIProvider(c, sess.TenantID)
		if err != nil || !cfg.Enabled {
			authError(c, 409, "AI_DISABLED")
			return
		}
		key := ""
		if cfg.Provider != "ollama" {
			if h.Service.Secrets == nil {
				authError(c, 503, "AI_DISABLED")
				return
			}
			key, err = h.Service.Secrets.DecryptString(cfg.EncryptedKey)
			if err != nil {
				authError(c, 503, "AI_DISABLED")
				return
			}
		}
		provider, err = ai.ConfiguredProvider(cfg.Provider, cfg.Endpoint, cfg.Model, key, h.Service.CloudEndpoints, h.Service.LocalEndpoints)
		if err != nil {
			authError(c, 403, "AI_ENDPOINT_FORBIDDEN")
			return
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	models, err := ai.FetchModels(ctx, provider)
	if err != nil {
		authError(c, 502, "AI_PROVIDER_UNREACHABLE")
		return
	}
	c.JSON(200, gin.H{"status": "reachable", "models": models})
}
