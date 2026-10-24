package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/payment"
)

type paymentMerchantInput struct {
	Provider       string                         `json:"provider"`
	Environment    string                         `json:"environment"`
	AccountID      string                         `json:"account_id"`
	Name           string                         `json:"name"`
	Enabled        bool                           `json:"enabled"`
	Currencies     []string                       `json:"currencies"`
	Entrypoint     string                         `json:"entrypoint"`
	CheckoutLimits map[string]payment.AmountLimit `json:"checkout_limits"`
	Secrets        *payment.MerchantSecrets       `json:"credentials"`
}
type paymentConfigProof struct {
	PayloadHash, Scope, PreviousCipher string
	PreviousVersion                    pgtype.Timestamptz
	Existing                           bool
}

const paymentConfigProofKey = "urmail.payment_config_proof"

func paymentConfigBodyHash(c *gin.Context) string {
	raw, _ := c.Get(gin.BodyBytesKey)
	body, _ := raw.([]byte)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func failPaymentPreflight(c *gin.Context, status int, code string) {
	c.Abort()
	authError(c, status, code)
}

// VerifyConfiguration performs only read-only provider identity checks before
// the audited business transaction, binding evidence to this exact request body.
func (h *PaymentGatewayHandler) VerifyConfiguration(c *gin.Context) {
	if h.Secrets == nil || h.Registry == nil || h.Platform == nil {
		failPaymentPreflight(c, 503, "PAYMENT_PROVIDER_DISABLED")
		return
	}
	var input paymentMerchantInput
	if c.ShouldBindBodyWith(&input, binding.JSON) != nil {
		failPaymentPreflight(c, 400, "INVALID_PARAMS")
		return
	}
	proof := paymentConfigProof{PayloadHash: paymentConfigBodyHash(c)}
	credentials := payment.MerchantSecrets{}
	if input.Secrets != nil {
		credentials = *input.Secrets
	}
	if c.Param("merchant_id") != "" {
		id, ok := marketingID(c, "merchant_id")
		if !ok {
			c.Abort()
			return
		}
		row, err := h.Platform.PlatformGetPaymentMerchant(c.Request.Context(), id)
		if err != nil {
			c.Abort()
			supportError(c, err)
			return
		}
		if row.Provider != input.Provider || row.Environment != input.Environment || row.AccountID != input.AccountID {
			failPaymentPreflight(c, 409, "PAYMENT_IDENTITY_IMMUTABLE")
			return
		}
		proof.Existing = true
		proof.PreviousCipher = row.EncryptedConfig
		proof.PreviousVersion = row.UpdatedAt
		if input.Secrets == nil {
			raw, err := h.Secrets.DecryptString(row.EncryptedConfig)
			if err != nil || json.Unmarshal([]byte(raw), &credentials) != nil {
				failPaymentPreflight(c, 503, "PAYMENT_PROVIDER_DISABLED")
				return
			}
		}
	}
	merchant := payment.Merchant{Provider: input.Provider, Environment: input.Environment, AccountID: input.AccountID, Name: input.Name, Enabled: input.Enabled, Currencies: input.Currencies, Entrypoint: input.Entrypoint, CheckoutLimits: input.CheckoutLimits, Secrets: credentials}
	if merchant.ValidateCheckoutLimits() != nil {
		failPaymentPreflight(c, 400, "PAYMENT_CONFIG_INVALID")
		return
	}
	proof.Scope = merchant.Scope()
	if input.Enabled {
		provider, err := h.Registry.Build(merchant)
		if err != nil || payment.VerifyProviderIdentity(c.Request.Context(), merchant, provider) != nil {
			failPaymentPreflight(c, 400, "PAYMENT_CONFIG_INVALID")
			return
		}
	}
	c.Set(paymentConfigProofKey, proof)
	c.Next()
}
