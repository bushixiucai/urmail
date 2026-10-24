-- name: PlatformFreezeInvoiceDocumentSnapshot :execrows
UPDATE invoices SET document_snapshot=$3 WHERE tenant_id=$1 AND id=$2 AND document_snapshot='{}'::jsonb;

-- name: PlatformGetDocumentProfile :one
SELECT value FROM settings WHERE scope=$1 AND scope_id=$2 AND key='billing_document_profile';

-- name: PlatformSetDocumentProfile :exec
INSERT INTO settings(scope,scope_id,key,value) VALUES($1,$2,'billing_document_profile',$3)
ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();


-- name: UpsertInvoiceBuyerProfile :exec
INSERT INTO settings(scope,scope_id,key,value) VALUES('tenant',sqlc.arg(tenant_id),'billing_document_profile',sqlc.arg(value))
ON CONFLICT(scope,scope_id,key) DO UPDATE SET value=EXCLUDED.value,updated_at=now();
