-- Invoices (tenant-scoped). M3 checklist gap: CRUD for the invoices table.
-- order_id keeps every invoice traceable to the order that produced it, so a
-- bill stays reproducible after a later plan price edit (M5 review 3.31).

-- name: CreateInvoice :one
INSERT INTO invoices (
    tenant_id, order_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at, order_id;

-- name: GetInvoiceByID :one
SELECT id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at, order_id
FROM invoices
WHERE tenant_id = $1 AND id = $2;

-- name: GetInvoiceByNumber :one
SELECT id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at, order_id
FROM invoices
WHERE tenant_id = $1 AND number = $2;

-- name: ListInvoicesByTenant :many
SELECT id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at, order_id
FROM invoices
WHERE tenant_id = $1
ORDER BY created_at DESC, id DESC;

-- name: ListInvoicesByOrder :many
SELECT id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at, order_id
FROM invoices
WHERE tenant_id = $1 AND order_id = $2
ORDER BY created_at DESC, id DESC;

-- name: MarkInvoicePaid :one
UPDATE invoices
SET status = 'paid', issued_at = COALESCE(issued_at, now())
WHERE tenant_id = $1 AND id = $2 AND status IN ('draft', 'issued')
RETURNING id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at, order_id;

-- name: VoidInvoice :one
UPDATE invoices
SET status = 'void'
WHERE tenant_id = $1 AND id = $2 AND status IN ('draft', 'issued')
RETURNING id, tenant_id, number, period_start, period_end, amount_cents, currency, status, pdf_path, issued_at, created_at, order_id;

-- name: SetInvoicePDFPath :exec
UPDATE invoices SET pdf_path = $3 WHERE tenant_id = $1 AND id = $2;