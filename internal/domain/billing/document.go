package billing

import (
	"errors"
	"math/big"
	"strings"
	"time"
)

// DocumentParty is the identity printed on a commercial billing statement.
type DocumentParty struct {
	Name        string `json:"name"`
	TaxID       string `json:"tax_id,omitempty"`
	Address     string `json:"address,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Email       string `json:"email,omitempty"`
	BankName    string `json:"bank_name,omitempty"`
	BankAccount string `json:"bank_account,omitempty"`
}

// DocumentTaxPolicy splits an explicitly configured tax-inclusive selling price.
// It is a disclosure policy, not a formal tax-invoice provider integration.
type DocumentTaxPolicy struct {
	Configured      bool  `json:"configured"`
	RateBasisPoints int64 `json:"rate_basis_points"`
	IncludedInTotal bool  `json:"included_in_total"`
}

// DocumentProfile configures identities used only by future quote snapshots.
type DocumentProfile struct {
	Party  DocumentParty     `json:"party"`
	Tax    DocumentTaxPolicy `json:"tax"`
	Locale string            `json:"locale"`
}

// DocumentAmounts uses integer cents; unconfigured tax is unknown rather than zero.
type DocumentAmounts struct {
	Currency        string `json:"currency"`
	TotalCents      int64  `json:"total_cents"`
	NetCents        *int64 `json:"net_cents,omitempty"`
	TaxCents        *int64 `json:"tax_cents,omitempty"`
	RateBasisPoints *int64 `json:"rate_basis_points,omitempty"`
	TaxConfigured   bool   `json:"tax_configured"`
	IncludedInTotal bool   `json:"included_in_total"`
}

// DocumentSnapshot freezes the parties and monetary disclosure at one observation.
type DocumentSnapshot struct {
	Version      int             `json:"version"`
	Kind         string          `json:"kind"`
	Provenance   string          `json:"provenance"`
	CapturedAt   time.Time       `json:"captured_at"`
	SellerSource string          `json:"seller_source"`
	BuyerSource  string          `json:"buyer_source"`
	Seller       DocumentParty   `json:"seller"`
	Buyer        DocumentParty   `json:"buyer"`
	Amounts      DocumentAmounts `json:"amounts"`
	Locale       string          `json:"locale"`
}

// ErrDocumentInvalid rejects malformed or unsupported commercial document data.
var ErrDocumentInvalid = errors.New("invalid commercial document snapshot")

// ValidateDocumentProfile bounds untrusted identity fields without interpreting
// user data as markup, PDF instructions, a URL, or a formal tax authorization.
func ValidateDocumentProfile(profile DocumentProfile) error {
	p := profile.Party
	if len(strings.TrimSpace(p.Name)) == 0 || len(p.Name) > 600 || len(p.Address) > 2000 || len(p.TaxID) > 200 || len(p.Phone) > 200 || len(p.Email) > 320 || len(p.BankName) > 600 || len(p.BankAccount) > 200 {
		return ErrDocumentInvalid
	}
	for _, text := range []string{p.Name, p.Address, p.TaxID, p.Phone, p.Email, p.BankName, p.BankAccount} {
		if strings.ContainsRune(text, 0) {
			return ErrDocumentInvalid
		}
	}
	if profile.Locale != "" && profile.Locale != "zh-CN" && profile.Locale != "en" {
		return ErrDocumentInvalid
	}
	if profile.Tax.Configured && (!profile.Tax.IncludedInTotal || profile.Tax.RateBasisPoints < 0 || profile.Tax.RateBasisPoints > 10000) {
		return ErrDocumentInvalid
	}
	return nil
}

// BuildDocumentSnapshot rounds the tax-inclusive net amount once (half-up),
// derives tax by subtraction and never increases or converts the paid total.
func BuildDocumentSnapshot(seller, buyer DocumentProfile, total int64, currency, provenance string, now time.Time) (DocumentSnapshot, error) {
	if total < 0 || total > 9007199254740991 || (currency != "CNY" && currency != "USD") {
		return DocumentSnapshot{}, ErrDocumentInvalid
	}
	for _, profile := range []DocumentProfile{seller, buyer} {
		if profile.Party.Name != "" {
			if err := ValidateDocumentProfile(profile); err != nil {
				return DocumentSnapshot{}, err
			}
		}
	}
	snapshot := DocumentSnapshot{Version: 1, Kind: "commercial_statement", Provenance: provenance, CapturedAt: now.UTC(), Seller: seller.Party, Buyer: buyer.Party, Amounts: DocumentAmounts{Currency: currency, TotalCents: total}, Locale: seller.Locale}
	if snapshot.Locale == "" {
		snapshot.Locale = "zh-CN"
	}
	if seller.Tax.Configured {
		if !seller.Tax.IncludedInTotal || seller.Tax.RateBasisPoints < 0 || seller.Tax.RateBasisPoints > 10000 {
			return DocumentSnapshot{}, ErrDocumentInvalid
		}
		denominator := big.NewInt(10000 + seller.Tax.RateBasisPoints)
		numerator := new(big.Int).Mul(big.NewInt(total), big.NewInt(10000))
		numerator.Mul(numerator, big.NewInt(2))
		numerator.Add(numerator, denominator)
		numerator.Quo(numerator, new(big.Int).Mul(denominator, big.NewInt(2)))
		if !numerator.IsInt64() {
			return DocumentSnapshot{}, ErrDocumentInvalid
		}
		net := numerator.Int64()
		tax := total - net
		rate := seller.Tax.RateBasisPoints
		snapshot.Amounts.NetCents = &net
		snapshot.Amounts.TaxCents = &tax
		snapshot.Amounts.RateBasisPoints = &rate
		snapshot.Amounts.TaxConfigured = true
		snapshot.Amounts.IncludedInTotal = true
	}
	return snapshot, nil
}
