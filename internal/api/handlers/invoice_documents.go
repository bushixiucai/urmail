package handlers

import (
	"context"

	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/store"
)

// CaptureInvoiceDocumentSnapshot shares the same immutable invoice identity
// capture for checkout and authorized recurring settlement.
func CaptureInvoiceDocumentSnapshot(ctx context.Context, q *store.Queries, tenantID, amountCents int64, currency string, stage ...string) ([]byte, error) {
	return billingrenew.CaptureInvoiceDocumentSnapshot(ctx, q, tenantID, amountCents, currency, stage...)
}
