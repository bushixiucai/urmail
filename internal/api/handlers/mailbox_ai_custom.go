package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/domain/mailboxsettings"
	"github.com/bushixiucai/urmail/internal/store"
)

// customAIConfiguration keeps tenant governance while changing only the user's
// provider target. Callers hold the existing tenant billing lock.
func customAIConfiguration(ctx context.Context, q *store.Queries, cfg store.AiProviderConfigs, tenant, mailbox int64, target *userAICustomConfig, expected string) (store.AiProviderConfigs, error) {
	if target == nil || !target.UseCustom || expected == "" || target.Digest != expected {
		return cfg, errAITargetChanged
	}
	personalMode := cfg.PersonalMode
	if personalMode == "" {
		personalMode = "public"
	}
	switch personalMode {
	case "disabled":
		return cfg, mailboxsettings.ErrAIPersonalBYOKDisabled
	case "approved":
		targetEndpoint := strings.TrimRight(target.Endpoint, "/")
		allowed := false
		for _, ep := range cfg.ApprovedEndpoints {
			if strings.TrimRight(ep, "/") == targetEndpoint {
				allowed = true
				break
			}
		}
		if !allowed {
			return cfg, mailboxsettings.ErrAIEndpointNotApproved
		}
	case "public":
		if target.Provider != ai.ProviderOllama && !strings.HasPrefix(target.Endpoint, "https://") {
			return cfg, mailboxsettings.ErrAIEndpointNotApproved
		}
	default:
		return cfg, mailboxsettings.ErrAIPersonalBYOKDisabled
	}
	row, err := q.PlatformGetMailboxSettings(ctx, store.PlatformGetMailboxSettingsParams{TenantID: tenant, MailboxID: mailbox})
	if err != nil {
		return cfg, errAITargetChanged
	}
	var prefs struct {
		AI *mailboxsettings.AIConfig `json:"ai"`
	}
	if json.Unmarshal(row.Preferences, &prefs) != nil || prefs.AI == nil || !prefs.AI.UseCustom {
		return cfg, errAITargetChanged
	}
	// The persisted key is ciphertext; only its digest participates in identity.
	raw, err := json.Marshal(prefs.AI) //nolint:gosec // G117: persisted credential ciphertext is hashed locally and never exported or logged.
	if err != nil {
		return cfg, errAITargetChanged
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(append(raw, []byte(fmt.Sprint(row.Version))...)))
	current, parseErr := mailboxsettings.ParseAI(raw)
	if parseErr != nil || digest != expected || current.Provider != target.Provider || current.Model != target.Model || current.Endpoint != target.Endpoint {
		return cfg, errAITargetChanged
	}
	if cfg.LocalOnly && target.Provider != ai.ProviderOllama {
		return cfg, ai.ErrLocalOnly
	}
	cfg = projectCustomAITarget(cfg, target.AIConfig)
	cfg.Enabled = true
	cfg.ConfigurationDigest = expected
	return cfg, nil
}

// projectCustomAITarget changes provider metadata without decrypting or exposing
// credentials; accounting and its mailbox read model share this projection.
func projectCustomAITarget(cfg store.AiProviderConfigs, target mailboxsettings.AIConfig) store.AiProviderConfigs {
	cfg.Mode = "byo"
	if target.Provider == ai.ProviderOllama {
		cfg.Mode = "local"
	}
	cfg.Provider, cfg.Endpoint, cfg.Model = target.Provider, target.Endpoint, target.Model
	cfg.EncryptedKey = ""
	cfg.InputMicrosPerMillion, cfg.OutputMicrosPerMillion = 0, 0
	return cfg
}

func mailboxQuotaTarget(ctx context.Context, q *store.Queries, cfg store.AiProviderConfigs, tenant, mailbox int64) (store.AiProviderConfigs, error) {
	row, err := q.PlatformGetMailboxSettings(ctx, store.PlatformGetMailboxSettingsParams{TenantID: tenant, MailboxID: mailbox})
	if errors.Is(err, pgx.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var prefs struct {
		AI json.RawMessage `json:"ai"`
	}
	if err = json.Unmarshal(row.Preferences, &prefs); err != nil {
		return cfg, err
	}
	if len(prefs.AI) == 0 {
		return cfg, nil
	}
	target, err := mailboxsettings.ParseAI(prefs.AI)
	if err != nil {
		return cfg, err
	}
	if !target.UseCustom {
		return cfg, nil
	}
	cfg = projectCustomAITarget(cfg, target)
	if cfg.LocalOnly && target.Provider != ai.ProviderOllama {
		cfg.Enabled = false
	}
	return cfg, nil
}
