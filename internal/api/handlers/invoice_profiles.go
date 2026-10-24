package handlers

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

// InvoiceProfileHandler manages future statement identities, never historical PDFs.
type InvoiceProfileHandler struct{ Store, Platform *store.Queries }

type invoicePartyPatch struct {
	Name        *string `json:"name"`
	TaxID       *string `json:"tax_id"`
	Address     *string `json:"address"`
	Phone       *string `json:"phone"`
	Email       *string `json:"email"`
	BankName    *string `json:"bank_name"`
	BankAccount *string `json:"bank_account"`
}
type invoiceProfilePatch struct {
	Party  invoicePartyPatch          `json:"party"`
	Tax    *billing.DocumentTaxPolicy `json:"tax"`
	Locale *string                    `json:"locale"`
}

func maskedBillingIdentity(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-4:])
}

func loadDocumentProfile(c *gin.Context, q *store.Queries, scope string, id int64) (billing.DocumentProfile, bool, error) {
	var profile billing.DocumentProfile
	raw, err := q.PlatformGetDocumentProfile(c, store.PlatformGetDocumentProfileParams{Scope: scope, ScopeID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return profile, false, nil
	}
	if err != nil {
		return profile, false, err
	}
	if json.Unmarshal(raw, &profile) != nil {
		return profile, false, billing.ErrDocumentInvalid
	}
	return profile, true, nil
}

func (h *InvoiceProfileHandler) profile(c *gin.Context, platform, write bool) {
	scope, id, q := "platform", int64(0), requestStore(c, h.Platform)
	if !platform {
		var ok bool
		id, ok = middleware.GetTenantID(c)
		if !ok {
			authError(c, 401, "UNAUTHORIZED")
			return
		}
		role := middleware.GetRole(c)
		if role != "owner" && role != "admin" && role != "billing" {
			authError(c, 403, "FORBIDDEN")
			return
		}
		scope = "tenant"
		q = requestStore(c, h.Store)
	}
	if q == nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	profile, exists, err := loadDocumentProfile(c, q, scope, id)
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	if !write {
		profile.Party.TaxID = maskedBillingIdentity(profile.Party.TaxID)
		profile.Party.BankAccount = maskedBillingIdentity(profile.Party.BankAccount)
		profile.Party.Phone = maskedBillingIdentity(profile.Party.Phone)
		profile.Party.Email = maskedBillingIdentity(profile.Party.Email)
		c.JSON(200, gin.H{"profile": profile, "configured": exists})
		return
	}
	var patch invoiceProfilePatch
	if c.Request.ContentLength > 16384 || c.ShouldBindJSON(&patch) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	apply := func(target *string, value *string) {
		if value != nil {
			*target = strings.TrimSpace(*value)
		}
	}
	apply(&profile.Party.Name, patch.Party.Name)
	apply(&profile.Party.TaxID, patch.Party.TaxID)
	apply(&profile.Party.Address, patch.Party.Address)
	apply(&profile.Party.Phone, patch.Party.Phone)
	apply(&profile.Party.Email, patch.Party.Email)
	apply(&profile.Party.BankName, patch.Party.BankName)
	apply(&profile.Party.BankAccount, patch.Party.BankAccount)
	if patch.Locale != nil {
		profile.Locale = *patch.Locale
	}
	if profile.Locale == "" {
		profile.Locale = "zh-CN"
	}
	if patch.Tax != nil {
		if !platform && patch.Tax.Configured {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		if platform {
			profile.Tax = *patch.Tax
		}
	}
	if !platform {
		profile.Tax = billing.DocumentTaxPolicy{}
	}
	if billing.ValidateDocumentProfile(profile) != nil {
		authError(c, 400, "BILLING_PROFILE_INVALID")
		return
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		authError(c, 400, "BILLING_PROFILE_INVALID")
		return
	}
	if platform {
		err = q.PlatformSetDocumentProfile(c, store.PlatformSetDocumentProfileParams{Scope: scope, ScopeID: id, Value: raw})
	} else {
		err = q.UpsertInvoiceBuyerProfile(c, store.UpsertInvoiceBuyerProfileParams{TenantID: id, Value: raw})
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	c.Status(204)
}

// GetSeller returns a redacted platform identity profile.
func (h *InvoiceProfileHandler) GetSeller(c *gin.Context) { h.profile(c, true, false) }

// PutSeller changes only identities/tax disclosures captured by future quotes.
func (h *InvoiceProfileHandler) PutSeller(c *gin.Context) { h.profile(c, true, true) }

// GetBuyer returns the requesting tenant's redacted billing identity.
func (h *InvoiceProfileHandler) GetBuyer(c *gin.Context) { h.profile(c, false, false) }

// PutBuyer applies only explicitly supplied fields; omitted private fields persist.
func (h *InvoiceProfileHandler) PutBuyer(c *gin.Context) { h.profile(c, false, true) }
