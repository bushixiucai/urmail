-- Invoices (tenant-scoped). M3 checklist gap: CRUD for the invoices table.

-- name: CreateInvoice :one
INSERT INTO invoices (
    tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at;

-- name: GetInvoiceByID :one
SELECT id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at
FROM invoices
WHERE tenant_id = $1 AND id = $2;

-- name: GetInvoiceByNumber :one
SELECT id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at
FROM invoices
WHERE tenant_id = $1 AND number = $2;

-- name: ListInvoicesByTenant :many
SELECT id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at
FROM invoices
WHERE tenant_id = $1
ORDER BY created_at DESC, id DESC;

-- name: MarkInvoicePaid :one
UPDATE invoices
SET status = 'paid', issued_at = COALESCE(issued_at, now())
WHERE tenant_id = $1 AND id = $2 AND status IN ('draft', 'issued')
RETURNING id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at;

-- name: VoidInvoice :one
UPDATE invoices
SET status = 'void'
WHERE tenant_id = $1 AND id = $2 AND status IN ('draft', 'issued')
RETURNING id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at;
