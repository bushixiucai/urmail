-- name: GetBillingCalendar :one
SELECT * FROM billing_calendars WHERE tenant_id=$1;

-- name: GetBillingBusinessTimezone :one
SELECT urmail_business_timezone($1::bigint)::text AS timezone;

-- name: BillingCalendarReviewRequired :one
SELECT urmail_billing_history_requires_review($1::bigint,0)::boolean AS required;
