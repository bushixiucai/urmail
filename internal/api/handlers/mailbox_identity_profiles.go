package handlers

import (
	"encoding/json"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/mailboxsettings"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type mailboxIdentityProfile struct {
	DisplayName   string `json:"display_name"`
	SignatureText string `json:"signature_text"`
	SignatureHTML string `json:"signature_html"`
}

func (h *MailboxNativeHandler) authorizedIdentity(c *gin.Context, sess webmail.Session, raw string) (string, string, bool) {
	address := normalizeFromCandidate(raw)
	if address == "" {
		if strings.TrimSpace(raw) != "" {
			authError(c, 400, "INVALID_PARAMS")
			return "", "", false
		}
		address = sess.Address
	}
	if address != sess.Address {
		allowed := false
		if h.SelfSettings != nil {
			var err error
			allowed, err = h.SelfSettings.SenderIdentityAllowed(c, sess.TenantID, sess.Address, address)
			if err != nil {
				authError(c, 503, "MAIL_POLICY_UNAVAILABLE")
				return "", "", false
			}
		}
		if !allowed {
			authError(c, 403, "MAILBOX_FROM_NOT_AUTHORIZED")
			return "", "", false
		}
	}
	name := ""
	if h.SelfSettings != nil {
		settings, err := h.SelfSettings.Settings(c, sess.TenantID, sess.MailboxID)
		if err != nil {
			authError(c, 503, "DB_ERROR")
			return "", "", false
		}
		if address == sess.Address {
			name = settings.DisplayName
		}
		var profiles map[string]mailboxIdentityProfile
		if json.Unmarshal(settings.IdentityProfiles, &profiles) == nil {
			if profile, ok := profiles[address]; ok && address != sess.Address {
				name = profile.DisplayName
			}
		}
	}
	if _, err := mailboxsettings.DisplayName(name); err != nil || strings.ContainsAny(name, "\r\n") {
		authError(c, 400, "MAILBOX_SETTINGS_DISPLAY_NAME_INVALID")
		return "", "", false
	}
	header := address
	if name != "" {
		header = (&mail.Address{Name: name, Address: address}).String()
	}
	return address, header, true
}
func (h *MailboxNativeHandler) normalizeIdentityProfiles(c *gin.Context, sess webmail.Session, raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var profiles map[string]mailboxIdentityProfile
	if len(raw) > 65536 || json.Unmarshal(raw, &profiles) != nil || profiles == nil || len(profiles) > 20 {
		authError(c, 400, "MAILBOX_SETTINGS_SIGNATURE_INVALID")
		return nil, false
	}
	normalized := make(map[string]mailboxIdentityProfile, len(profiles))
	for address, profile := range profiles {
		canonical := normalizeFromCandidate(address)
		if canonical == "" || canonical != address {
			authError(c, 400, "INVALID_PARAMS")
			return nil, false
		}
		if canonical != sess.Address {
			allowed, err := h.SelfSettings.SenderIdentityAllowed(c, sess.TenantID, sess.Address, canonical)
			if err != nil {
				authError(c, 503, "MAIL_POLICY_UNAVAILABLE")
				return nil, false
			}
			if !allowed {
				authError(c, 403, "MAILBOX_FROM_NOT_AUTHORIZED")
				return nil, false
			}
		}
		name, e1 := mailboxsettings.DisplayName(profile.DisplayName)
		text, e2 := mailboxsettings.Signature(profile.SignatureText, mailboxsettings.MaxSignatureText)
		html, e3 := mailboxsettings.Signature(profile.SignatureHTML, mailboxsettings.MaxSignatureHTML)
		if e1 != nil || e2 != nil || e3 != nil {
			authError(c, 400, "MAILBOX_SETTINGS_SIGNATURE_INVALID")
			return nil, false
		}
		html, _ = webmail.SanitizeHTML(html)
		normalized[canonical] = mailboxIdentityProfile{DisplayName: name, SignatureText: text, SignatureHTML: html}
	}
	encoded, _ := json.Marshal(normalized)
	if len(encoded) > 65536 {
		authError(c, 400, "MAILBOX_SETTINGS_SIGNATURE_INVALID")
		return nil, false
	}
	return encoded, true
}
func (h *MailboxNativeHandler) validatePresentationPreferences(c *gin.Context, sess webmail.Session, raw json.RawMessage) bool {
	var values map[string]any
	_ = json.Unmarshal(raw, &values)
	if value, ok := values["quote_original"]; ok {
		if _, valid := value.(bool); !valid {
			authError(c, 400, "MAILBOX_SETTINGS_PREFERENCES_INVALID")
			return false
		}
	}

	if value, ok := values["timezone"]; ok {
		zone, isString := value.(string)
		if !isString || zone == "" {
			authError(c, 400, "MAILBOX_SETTINGS_PREFERENCES_INVALID")
			return false
		}
		if _, err := time.LoadLocation(zone); err != nil {
			authError(c, 400, "MAILBOX_SETTINGS_PREFERENCES_INVALID")
			return false
		}
	}
	if value, ok := values["date_format"]; ok {
		switch value {
		case "locale", "iso", "day_first", "month_first":
		default:
			authError(c, 400, "MAILBOX_SETTINGS_PREFERENCES_INVALID")
			return false
		}
	}
	if value, ok := values["reply_identity"]; ok {
		if value != "recipient" && value != "default" {
			authError(c, 400, "MAILBOX_SETTINGS_PREFERENCES_INVALID")
			return false
		}
	}
	if value, ok := values["default_from"]; ok {
		address, isString := value.(string)
		if !isString {
			authError(c, 400, "MAILBOX_FROM_NOT_AUTHORIZED")
			return false
		}
		if address != "" && address != sess.Address {
			allowed, err := h.SelfSettings.SenderIdentityAllowed(c, sess.TenantID, sess.Address, address)
			if err != nil || !allowed {
				authError(c, 403, "MAILBOX_FROM_NOT_AUTHORIZED")
				return false
			}
		}
	}
	return true
}
