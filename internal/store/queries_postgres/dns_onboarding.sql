-- name: ListDNSCredentials :many
SELECT id,tenant_id,domain_id,provider,zone_name,zone_id,version,enabled,verified_at,revoked_at,updated_at FROM dns_credentials WHERE tenant_id=$1 AND domain_id=$2;
-- name: SaveDNSCredential :one
SELECT urmail_save_dns_credential(sqlc.arg(tenant_id)::bigint,sqlc.arg(domain_id)::bigint,sqlc.arg(provider)::text,sqlc.arg(zone_name)::text,sqlc.arg(zone_id)::text,sqlc.arg(encrypted_secret)::text)::bigint AS id;
-- name: RevokeDNSCredential :exec
SELECT urmail_revoke_dns_credential(sqlc.arg(tenant_id)::bigint,sqlc.arg(domain_id)::bigint);
-- name: CreateDNSPlan :one
SELECT urmail_create_dns_plan(sqlc.arg(id)::uuid,sqlc.arg(tenant_id)::bigint,sqlc.arg(domain_id)::bigint,sqlc.narg(credential_id)::bigint,sqlc.narg(credential_version)::bigint,sqlc.arg(kind)::text,sqlc.arg(domain_digest)::text,sqlc.arg(source_digest)::text,sqlc.arg(before_records)::jsonb,sqlc.arg(desired_records)::jsonb,sqlc.arg(changes)::jsonb)::uuid AS id;
-- name: ConfirmDNSPlan :exec
SELECT urmail_confirm_dns_plan(sqlc.arg(tenant_id)::bigint,sqlc.arg(id)::uuid,sqlc.arg(source_digest)::text);
-- name: CancelDNSPlan :exec
SELECT urmail_cancel_dns_plan(sqlc.arg(tenant_id)::bigint,sqlc.arg(id)::uuid,sqlc.arg(reason)::text);
-- name: ListDNSPlans :many
SELECT * FROM dns_change_plans WHERE tenant_id=$1 AND domain_id=$2 ORDER BY created_at DESC,id LIMIT 20;
-- name: GetDNSPlan :one
SELECT * FROM dns_change_plans WHERE tenant_id=$1 AND id=$2;
-- name: ListDNSActions :many
SELECT * FROM dns_record_actions WHERE tenant_id=$1 AND plan_id=$2 ORDER BY ordinal;
-- name: RequestOnboardingRetry :exec
SELECT urmail_request_onboarding(sqlc.arg(tenant_id)::bigint,sqlc.arg(domain_id)::bigint,sqlc.arg(reason)::text);
-- name: GetDomainProvisioning :one
SELECT provisioning_state,dns_explicit FROM domains WHERE tenant_id=$1 AND id=$2;
-- name: RequestDNSRollback :exec
SELECT urmail_rollback_dns_plan(sqlc.arg(tenant_id)::bigint,sqlc.arg(id)::uuid,sqlc.arg(reason)::text);
-- name: ReviewDNSPlan :exec
SELECT urmail_review_dns_plan(sqlc.arg(tenant_id)::bigint,sqlc.arg(id)::uuid,sqlc.arg(reason)::text);
-- name: AbandonDNSPlan :exec
SELECT urmail_abandon_dns_plan(sqlc.arg(tenant_id)::bigint,sqlc.arg(id)::uuid,sqlc.arg(reason)::text);
