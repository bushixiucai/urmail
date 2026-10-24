package pdf

import (
	"bytes"
	"testing"
)

func TestInvoice(t *testing.T) {
	raw, err := Invoice(InvoiceData{
		Number: "INV-1", Tenant: "example.test", Amount: "10.00",
		Currency: "CNY", PeriodStart: "2026-10-01", PeriodEnd: "2026-11-01",
		Status: "paid", IssuedAt: "2026-10-22",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("%PDF-1.")) {
		t.Fatal("missing PDF header")
	}
	if !bytes.Contains(raw, []byte("startxref")) || !bytes.Contains(raw, []byte("/FontFile2")) {
		t.Fatal("PDF is missing expected sections")
	}
}
