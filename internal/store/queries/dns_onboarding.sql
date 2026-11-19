-- name: ListDNSCredentials :many
-- scope: tenant

SELECT id,tenant_id,domain_id,provider,zone_name,zone_id,version,enabled,verified_at,revoked_at,updated_at FROM dns_credentials WHERE tenant_id=?1 AND domain_id=?2;
-- name: SaveDNSCredential :one
-- scope: platform

SELECT urmail_save_dns_credential(sqlc.arg(tenant_id),sqlc.arg(domain_id),sqlc.arg(provider),sqlc.arg(zone_name),sqlc.arg(zone_id),sqlc.arg(encrypted_secret)) AS id;
-- name: RevokeDNSCredential :exec
-- scope: platform

SELECT urmail_revoke_dns_credential(sqlc.arg(tenant_id),sqlc.arg(domain_id));
-- name: CreateDNSPlan :one
-- scope: platform

SELECT urmail_create_dns_plan(sqlc.arg(id),sqlc.arg(tenant_id),sqlc.arg(domain_id),sqlc.narg(credential_id),sqlc.narg(credential_version),sqlc.arg(kind),sqlc.arg(domain_digest),sqlc.arg(source_digest),sqlc.arg(before_records),sqlc.arg(desired_records),sqlc.arg(changes)) AS id;
-- name: ConfirmDNSPlan :exec
-- scope: platform

SELECT urmail_confirm_dns_plan(sqlc.arg(tenant_id),sqlc.arg(id),sqlc.arg(source_digest));
-- name: CancelDNSPlan :exec
-- scope: platform

SELECT urmail_cancel_dns_plan(sqlc.arg(tenant_id),sqlc.arg(id),sqlc.arg(reason));
-- name: ListDNSPlans :many
-- scope: tenant

SELECT * FROM dns_change_plans WHERE tenant_id=?1 AND domain_id=?2 ORDER BY created_at DESC,id LIMIT 20;
-- name: GetDNSPlan :one
-- scope: tenant

SELECT * FROM dns_change_plans WHERE tenant_id=?1 AND id=?2;
-- name: ListDNSActions :many
-- scope: tenant

SELECT * FROM dns_record_actions WHERE tenant_id=?1 AND plan_id=?2 ORDER BY ordinal;
-- name: RequestOnboardingRetry :exec
-- scope: platform

SELECT urmail_request_onboarding(sqlc.arg(tenant_id),sqlc.arg(domain_id),sqlc.arg(reason));
-- name: GetDomainProvisioning :one
-- scope: tenant

SELECT provisioning_state,dns_explicit FROM domains WHERE tenant_id=?1 AND id=?2;
-- name: RequestDNSRollback :exec
-- scope: platform

SELECT urmail_rollback_dns_plan(sqlc.arg(tenant_id),sqlc.arg(id),sqlc.arg(reason));
-- name: ReviewDNSPlan :exec
-- scope: platform

SELECT urmail_review_dns_plan(sqlc.arg(tenant_id),sqlc.arg(id),sqlc.arg(reason));
-- name: AbandonDNSPlan :exec
-- scope: platform

SELECT urmail_abandon_dns_plan(sqlc.arg(tenant_id),sqlc.arg(id),sqlc.arg(reason));
