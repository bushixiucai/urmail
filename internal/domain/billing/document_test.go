package billing

import (
	"testing"
	"time"
)

func TestDocumentTaxInclusiveHalfUpAndUnknown(t *testing.T) {
	seller := DocumentProfile{Party: DocumentParty{Name: "Seller"}, Tax: DocumentTaxPolicy{Configured: true, RateBasisPoints: 600, IncludedInTotal: true}}
	s, err := BuildDocumentSnapshot(seller, DocumentProfile{}, 10600, "CNY", "quote", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if *s.Amounts.NetCents != 10000 || *s.Amounts.TaxCents != 600 {
		t.Fatalf("incorrect tax split: %+v", s.Amounts)
	}
	seller.Tax.RateBasisPoints = 10000
	s, err = BuildDocumentSnapshot(seller, DocumentProfile{}, 1, "USD", "quote", time.Now())
	if err != nil || *s.Amounts.NetCents != 1 || *s.Amounts.TaxCents != 0 {
		t.Fatalf("half-up failed: %+v %v", s, err)
	}
	seller.Tax.Configured = false
	s, err = BuildDocumentSnapshot(seller, DocumentProfile{}, 0, "USD", "quote", time.Now())
	if err != nil || s.Amounts.NetCents != nil || s.Amounts.TaxConfigured {
		t.Fatalf("missing tax treated as zero: %+v %v", s, err)
	}
	seller.Tax.Configured = true
	seller.Tax.IncludedInTotal = false
	if _, err := BuildDocumentSnapshot(seller, DocumentProfile{}, 100, "USD", "quote", time.Now()); err == nil {
		t.Fatal("tax-exclusive policy silently changed total")
	}
}
