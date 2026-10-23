// Package billing validates subscription, order, and invoice rules.
package billing

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Invoice statuses as constrained by invoices_status_check.
const (
	InvoiceStatusDraft  = "draft"
	InvoiceStatusIssued = "issued"
	InvoiceStatusPaid   = "paid"
	InvoiceStatusVoid   = "void"
)

// Invoice validation errors.
var (
	ErrInvalidInvoiceStatus = errors.New("invalid invoice status transition")
	ErrInvalidInvoice       = errors.New("invalid invoice")
)

// Invoice is the billing domain's own representation of an invoice row
// (the domain package does not import internal/store).
type Invoice struct {
	ID          int64
	TenantID    int64
	Number      string
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	AmountCents int64
	Currency    string
	Status      string
	PDFPath     *string
	IssuedAt    *time.Time
	CreatedAt   time.Time
}

// CreateInvoiceParams carries the fields needed to insert a new invoice.
type CreateInvoiceParams struct {
	TenantID    int64
	Number      string
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	AmountCents int64
	Currency    string
	PDFPath     *string
}

// CanTransitionInvoiceStatus checks whether the invoice status transition is
// allowed: draft/issued may become paid or void; terminal states stay put.
func CanTransitionInvoiceStatus(current, next string) bool {
	switch current {
	case InvoiceStatusDraft, InvoiceStatusIssued:
		return next == InvoiceStatusPaid || next == InvoiceStatusIssued || next == InvoiceStatusVoid
	default:
		return false
	}
}

// ValidateNewInvoice checks the invariants of a freshly created invoice: a
// positive amount, a currency, and a period (when present) that ends after it
// starts.
func ValidateNewInvoice(p CreateInvoiceParams) error {
	if p.TenantID <= 0 {
		return fmt.Errorf("%w: tenant id must be positive", ErrInvalidInvoice)
	}
	if p.AmountCents < 0 {
		return fmt.Errorf("%w: amount must not be negative", ErrInvalidInvoice)
	}
	if p.Currency == "" {
		return fmt.Errorf("%w: currency is required", ErrInvalidInvoice)
	}
	if p.Number == "" {
		return fmt.Errorf("%w: number is required", ErrInvalidInvoice)
	}
	if p.PeriodStart != nil && p.PeriodEnd != nil && p.PeriodEnd.Before(*p.PeriodStart) {
		return fmt.Errorf("%w: period_end before period_start", ErrInvalidInvoice)
	}
	return nil
}

// GenerateInvoiceNumber produces a unique invoice number:
// INV-YYYYMMDD-<tenantID>-<4 random hex chars>. Randomness keeps the number
// unique even when a tenant has several invoices per day.
func GenerateInvoiceNumber(tenantID int64, now time.Time) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is catastrophic; fall back to time entropy
		// rather than panicking.
		return fmt.Sprintf("INV-%s-%06d-%d", now.Format("20060102"), tenantID, now.UnixNano())
	}
	return fmt.Sprintf("INV-%s-%06d-%s", now.Format("20060102"), tenantID, hex.EncodeToString(buf[:]))
}

// MarkInvoicePaid validates that the transition to paid is legal.
func (inv *Invoice) MarkInvoicePaid() error {
	if !CanTransitionInvoiceStatus(inv.Status, InvoiceStatusPaid) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidInvoiceStatus, inv.Status, InvoiceStatusPaid)
	}
	inv.Status = InvoiceStatusPaid
	now := time.Now()
	if inv.IssuedAt == nil {
		inv.IssuedAt = &now
	}
	return nil
}
