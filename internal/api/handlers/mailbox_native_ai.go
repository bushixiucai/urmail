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
	}
}

type nativeAIInput struct {
	Operation           string  `json:"operation"`
	ConfigurationDigest string  `json:"configuration_digest"`
	Confirmed           bool    `json:"confirmed"`
	RequestKey          string  `json:"request_key"`
	Folder              string  `json:"folder_enc"`
	UID                 uint32  `json:"uid"`
	UIDValidity         uint32  `json:"uid_validity"`
	Selection           *string `json:"selection"`
	Body                string  `json:"body"`
	Subject             string  `json:"subject"`
	Instruction         string  `json:"instruction"`
	Language            string  `json:"target_language"`
}

type userAICustomConfig struct {
	UseCustom bool   `json:"use_custom"`
	Provider  string `json:"provider"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	APIKey    string `json:"api_key"`
}

func (h *NativeMailboxAI) getUserAI(ctx context.Context, sess webmail.Session) (*userAICustomConfig, bool) {
	if h.Service == nil || h.Service.Pool == nil {
		return nil, false
	}
	q := store.New(h.Service.Pool)
	row, err := q.PlatformGetMailboxSettings(ctx, store.PlatformGetMailboxSettingsParams{
		TenantID:  sess.TenantID,
		MailboxID: sess.MailboxID,
	})
	if err != nil || len(row.Preferences) == 0 {
		return nil, false
	}
	var prefs struct {
		AI *userAICustomConfig `json:"ai"`
	}
	if err := json.Unmarshal(row.Preferences, &prefs); err != nil || prefs.AI == nil {
		return nil, false
	}
	if !prefs.AI.UseCustom || strings.TrimSpace(prefs.AI.Endpoint) == "" {
		return nil, false
	}
	if prefs.AI.Provider == "" {
		prefs.AI.Provider = "openai"
	}
	return prefs.AI, true
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
	if userAI, custom := h.getUserAI(c.Request.Context(), sess); custom {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(userAI.Endpoint+":"+userAI.Model+":"+userAI.Provider)))
		c.JSON(200, gin.H{
			"enabled":               true,
			"allowed":               true,
			"is_custom":             true,
			"provider":              userAI.Provider,
			"model":                 userAI.Model,
			"endpoint":              userAI.Endpoint,
			"configuration_digest":  digest,
			"scope_hash":            AICloudScopeHash,
			"requires_confirmation": false,
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
	userAI, custom := h.getUserAI(c.Request.Context(), sess)
	if !custom {
		if !h.requireTarget(c, sess, req.ConfigurationDigest, req.Confirmed) {
			return
		}
	} else if !req.Confirmed {
		authError(c, 403, "AI_CONTENT_CONSENT_REQUIRED")
		return
	}
	input := ai.OperationInput{Operation: req.Operation, Body: req.Body, Subject: req.Subject, TargetLanguage: req.Language, Instruction: req.Instruction}
	principal := store.PlatformConsumeMailboxTicketRow{TenantID: sess.TenantID, MailboxID: sess.MailboxID, SessionHash: sess.TokenHash, Address: sess.Address, Scope: "ai_compose"}
	var digest string
	if req.UID != 0 {
		folder, msg, ok := h.readContext(c, req.Folder, req.UID, req.UIDValidity)
		if !ok {
			return
		}
		selected := plainAIMessage(msg)
		digest = airetrieval.ContentDigest(selected)
		if req.Selection != nil {
			if strings.TrimSpace(*req.Selection) == "" {
				authError(c, 400, "AI_SELECTED_CONTENT_REQUIRED")
				return
			}
			if !strings.Contains(selected, *req.Selection) {
				authError(c, 409, "AI_SOURCE_CHANGED")
				return
			}
			selected = *req.Selection
		}
		input.Draft = req.Body
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
	if custom {
		provider, err := ai.DynamicProvider(userAI.Provider, userAI.Endpoint, userAI.Model, userAI.APIKey)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		callCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		aiReq, buildErr := ai.BuildOperation(input)
		if buildErr != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if userAI.Model != "" {
			aiReq.Model = userAI.Model
		}
		completion, callErr := provider.Complete(callCtx, aiReq)
		if callErr != nil {
			nativeAIError(c, callErr)
			return
		}
		res, valErr := ai.ValidateOperation(input.Operation, completion.Content)
		if valErr != nil {
			nativeAIError(c, valErr)
			return
		}
		c.JSON(200, gin.H{"result": res})
		return
	}
	result, err := h.Service.Run(c.Request.Context(), principal, req.RequestKey, input, req.ConfigurationDigest)
	if err != nil {
		nativeAIError(c, err)
		return
	}
	// Recheck the target, live session and selected source after generation.
	if !h.requireTarget(c, sess, req.ConfigurationDigest, req.Confirmed) {
		return
	}
	current, err := h.Native.Sessions.Get(c, sess.TokenHash)
	if err != nil || current.MailboxID != sess.MailboxID || current.TenantID != sess.TenantID {
		authError(c, 401, "MAILBOX_AUTHORIZATION_DENIED")
		return
	}
	if req.UID != 0 {
		_, msg, ok := h.readContext(c, req.Folder, req.UID, req.UIDValidity)
		if !ok {
			return
		}
		if airetrieval.ContentDigest(plainAIMessage(msg)) != digest {
			authError(c, 409, "AI_SOURCE_CHANGED")
			return
		}
	}
	c.JSON(200, gin.H{"result": result})
}
func nativeAIError(c *gin.Context, err error) {
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
		provider, err = ai.DynamicProvider(input.Provider, input.Endpoint, input.Model, input.APIKey)
		if err != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	} else if userAI, custom := h.getUserAI(c.Request.Context(), sess); custom {
		provider, err = ai.DynamicProvider(userAI.Provider, userAI.Endpoint, userAI.Model, userAI.APIKey)
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
