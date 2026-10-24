-- name: PlatformInitializeBillingCalendar :one
INSERT INTO billing_calendars(tenant_id,timezone,first_paid_at,anchor_at,source,review_reason,created_by)
VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id) DO NOTHING RETURNING *;

-- name: PlatformReanchorBillingCalendar :one
UPDATE billing_calendars SET anchor_at=$3,version=version+1,source='reactivation',review_reason='Expired paid service resumed with a complete new cycle',updated_at=now()
WHERE tenant_id=$1 AND version=$2 RETURNING *;

-- name: PlatformBillingCalendarReviewExcludingOrder :one
SELECT urmail_billing_history_requires_review(sqlc.arg(tenant_id)::bigint,sqlc.arg(order_id)::bigint)::boolean AS required;

-- name: PlatformListBillingCalendarReviewTenants :many
SELECT t.id FROM tenants t LEFT JOIN billing_calendars c ON c.tenant_id=t.id
WHERE c.tenant_id IS NULL AND urmail_billing_history_requires_review(t.id,0) ORDER BY t.id LIMIT 100;

-- name: PlatformCountBillingCalendarReviewTenants :one
SELECT count(*) FROM tenants t LEFT JOIN billing_calendars c ON c.tenant_id=t.id
WHERE c.tenant_id IS NULL AND urmail_billing_history_requires_review(t.id,0);
