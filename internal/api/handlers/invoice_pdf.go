package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/pdf"
	"github.com/bushixiucai/urmail/internal/store"
)

// invoiceDocumentPDF publishes bytes only after a mandatory tenant-scoped READ
// audit has succeeded. A legacy document never consults today's party profiles.
func (h *BillingHandler) invoiceDocumentPDF(c *gin.Context) {
	tenant, ok := middleware.GetTenantID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	user, ok := middleware.GetUserID(c)
	if !ok {
		authError(c, 401, "UNAUTHORIZED")
		return
	}
	role := middleware.GetRole(c)
	if role != "owner" && role != "admin" && role != "billing" {
		authError(c, 403, "FORBIDDEN")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		authError(c, 400, "INVALID_ID")
		return
	}
	q := requestStore(c, h.Store)
	invoice, err := q.GetInvoiceByID(c, store.GetInvoiceByIDParams{TenantID: tenant, ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authError(c, 404, "INVOICE_NOT_FOUND")
		} else {
			authError(c, 500, "DB_ERROR")
		}
		return
	}
	data := pdf.InvoiceData{Number: invoice.Number, Tenant: fmt.Sprintf("tenant-%d", tenant), Amount: pdf.Money(invoice.AmountCents), Currency: invoice.Currency, PeriodStart: documentTime(invoice.PeriodStart), PeriodEnd: documentTime(invoice.PeriodEnd), Status: invoice.Status, IssuedAt: documentTime(invoice.IssuedAt), CreatedAt: invoice.CreatedAt.Time.UTC()}
	if len(invoice.DocumentSnapshot) > 0 && string(invoice.DocumentSnapshot) != "{}" && string(invoice.DocumentSnapshot) != "null" {
		var snapshot billing.DocumentSnapshot
		if json.Unmarshal(invoice.DocumentSnapshot, &snapshot) != nil || snapshot.Version != 1 || snapshot.Kind != "commercial_statement" || snapshot.Amounts.Currency != invoice.Currency || snapshot.Amounts.TotalCents != invoice.AmountCents || snapshot.CapturedAt.IsZero() {
			authError(c, 500, "PDF_FAILED")
			return
		}
		switch snapshot.Provenance {
		case "quote", "observed_at_settlement", "observed_at_issuance":
		default:
			authError(c, 500, "PDF_FAILED")
			return
		}
		data.Snapshot = &snapshot
	}
	// The production request store must support both mandatory audit and refund
	// disclosure. Fake billing readers cannot accidentally bypass this boundary.
	concrete, ok := q.(*store.Queries)
	if !ok {
		authError(c, 503, "DB_ERROR")
		return
	}
	if invoice.SourcePaymentID.Valid {
		data.SourcePaymentID = strconv.FormatInt(invoice.SourcePaymentID.Int64, 10)
		data.RefundedCents, err = concrete.InvoiceCompletedRefundTotal(c, store.InvoiceCompletedRefundTotalParams{TenantID: tenant, PaymentID: invoice.SourcePaymentID})
		if err != nil {
			authError(c, 500, "DB_ERROR")
			return
		}
	}
	raw, err := pdf.Invoice(data)
	if err != nil {
		status := 500
		if errors.Is(err, pdf.ErrBusy) {
			status = 503
		}
		authError(c, status, "PDF_FAILED")
		return
	}
	if err := concrete.RecordTenantAudit(c, store.RecordTenantAuditParams{TenantID: tenant, UserID: user, Action: "READ invoice.pdf", Target: strconv.FormatInt(id, 10), Status: 200}); err != nil {
		middleware.RecordAuditFailure(true)
		authError(c, 500, "DB_ERROR")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"invoice-%d.pdf\"", id))
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/pdf", raw)
}

func documentTime(value pgtype.Timestamptz) string {
	if !value.Valid {
		return "-"
	}
	return value.Time.UTC().Format(time.RFC3339)
}
