package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/i18n"
	"github.com/bushixiucai/urmail/internal/store"
)

// SecretBox decrypts the private notification channel configuration.
type SecretBox interface {
	EncryptString(string) (string, error)
	DecryptString(string) (string, error)
}

// Worker delivers fenced notification jobs without retrying ambiguous submissions.
type Worker struct {
	Pool                          *db.Pool
	Secrets                       SecretBox
	Messages                      *i18n.Store
	FallbackLocale                string
	BaseURL                       string
	Enabled, DevMode, Quarantined bool
	Send                          Sender
}

// Step processes at most one durable notification job.
func (w *Worker) Step(ctx context.Context) (bool, error) {
	if !w.Enabled || w.DevMode || w.Quarantined || w.Secrets == nil || w.Messages == nil {
		return false, nil
	}
	q := store.New(w.Pool)
	value, err := q.PlatformGetNotificationChannel(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var encrypted string
	if json.Unmarshal(value, &encrypted) != nil || !strings.HasPrefix(encrypted, "enc:v1:") {
		return false, errors.New("notification configuration unavailable")
	}
	plain, err := w.Secrets.DecryptString(encrypted)
	if err != nil {
		return false, errors.New("notification configuration unavailable")
	}
	var cfg SMTPConfig
	if json.Unmarshal([]byte(plain), &cfg) != nil || cfg.Validate() != nil {
		return false, errors.New("notification configuration invalid")
	}
	if !cfg.Enabled {
		return false, nil
	}
	origin, err := url.Parse(w.BaseURL)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil {
		return false, errors.New("notification origin invalid")
	}
	if _, err = q.PlatformQuarantinePoisonEmailNotifications(ctx); err != nil {
		return false, err
	}
	item, err := q.PlatformClaimEmailNotification(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var params map[string]any
	if json.Unmarshal(item.Params, &params) != nil {
		return true, errors.New("notification parameters invalid")
	}
	if item.Kind == "contact_verification" {
		if !item.SecretPayload.Valid {
			return true, errors.New("verification credential missing")
		}
		token, decryptErr := w.Secrets.DecryptString(item.SecretPayload.String)
		if decryptErr != nil {
			return true, errors.New("verification credential unavailable")
		}
		params["url"] = strings.TrimRight(w.BaseURL, "/") + "/register/verify#token=" + url.QueryEscape(token)
	} else if !item.RecipientVerified {
		return true, errors.New("notification contact is unverified")
	}
	catalog, err := w.notificationCatalog(ctx, q, item)
	if err != nil {
		return true, err
	}
	body := catalog.Lookup(item.MessageKey, params)
	if body == item.MessageKey {
		return true, errors.New("notification template unavailable")
	}
	send := w.Send
	if send == nil {
		send = SendSMTP
	}
	err = send(ctx, cfg, Message{ID: item.ID, To: item.Recipient.String, Subject: catalog.Lookup("notification.subject", nil), Body: body})
	// The finish statement owns the retry decision: exponential backoff while
	// pending and the poisoned 'failed' cap once attempts are exhausted. A
	// confirmed-ambiguous submission stays 'uncertain' and is never replayed.
	state := "delivered"
	if errors.Is(err, ErrSubmissionUnknown) {
		state = "uncertain"
	} else if err != nil {
		state = "pending"
	}
	if _, saveErr := q.PlatformFinishEmailNotification(ctx, store.PlatformFinishEmailNotificationParams{ID: item.ID, Fence: item.Fence, Column3: state, Column4: sanitizeDeliveryError(err)}); saveErr != nil {
		notificationDeliveries.WithLabelValues("email", item.Kind, "unfinished").Inc()
		return true, saveErr
	}
	notificationDeliveries.WithLabelValues("email", item.Kind, outcome(state)).Inc()
	return true, err
}

// outcome maps a terminal state machine result onto a delivery metric.
func outcome(state string) string {
	switch state {
	case "delivered":
		return "sent"
	case "uncertain":
		return "uncertain"
	default:
		return "retrying"
	}
}

// sanitizeDeliveryError keeps provider diagnostics one bounded, single-line
// field: no newlines and no unbounded relay banners in durable storage.
func sanitizeDeliveryError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", " "), "\n", " ")
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// notificationCatalog uses existing user and inherited tenant preferences. Both
// templates come from one catalog, including when a missing translation falls back.
func (w *Worker) notificationCatalog(ctx context.Context, q *store.Queries, item store.NotificationOutbox) (*i18n.Catalog, error) {
	locale := ""
	usable := func(value string) bool {
		return value != "" && w.Messages.Get(value).Locale == value
	}
	if item.UserID.Valid {
		user, err := q.GetUserByID(ctx, item.UserID.Int64)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil && usable(user.Locale) {
			locale = user.Locale
		}
	}
	if locale == "" {
		var brandID, tenantID int64
		if item.TenantID.Valid {
			tenant, err := q.GetTenantByID(ctx, item.TenantID.Int64)
			if err != nil {
				return nil, err
			}
			brandID, tenantID = tenant.BrandID, tenant.ID
		}
		entries, err := q.ListSettingsByScopes(ctx, store.ListSettingsByScopesParams{ScopeID: brandID, ScopeID_2: tenantID})
		if err != nil {
			return nil, err
		}
		preferences := make(map[string]string)
		for _, entry := range entries {
			if entry.Key != "locale" {
				continue
			}
			var value string
			if json.Unmarshal(entry.Value, &value) == nil && usable(value) {
				preferences[entry.Scope] = value
			}
		}
		for _, scope := range []string{"tenant", "brand", "platform"} {
			if preferences[scope] != "" {
				locale = preferences[scope]
				break
			}
		}
	}
	if !usable(locale) {
		locale = w.FallbackLocale
	}
	catalog := w.Messages.Get(locale)
	_, hasBody := catalog.Message(item.MessageKey)
	_, hasSubject := catalog.Message("notification.subject")
	if !hasBody || !hasSubject {
		catalog = w.Messages.Get(w.FallbackLocale)
		_, hasBody = catalog.Message(item.MessageKey)
		_, hasSubject = catalog.Message("notification.subject")
		if !hasBody || !hasSubject {
			catalog = w.Messages.Get(i18n.DefaultLocale)
		}
	}
	return catalog, nil
}

// Run polls pending notifications until the context is canceled.
func (w *Worker) Run(ctx context.Context, logger *zap.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if _, err := w.Step(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("notification delivery requires attention")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
