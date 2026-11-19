-- Platform-private credentials and durable side-effect journals.
-- name: PlatformGetDNSCredential :one
-- scope: platform

SELECT * FROM dns_credentials WHERE tenant_id=?1 AND domain_id=?2 AND enabled AND revoked_at IS NULL;
-- name: PlatformDNSDomain :one
-- scope: platform

SELECT id,tenant_id,fqdn,mode,status,verified_at,provisioning_state,dns_explicit,dkim_selector,dkim_private_key,mx_host FROM domains WHERE tenant_id=?1 AND id=?2;
-- name: PlatformClaimDNSPlan :one
-- scope: platform

UPDATE dns_change_plans p SET status=CASE WHEN status='rollback_pending' THEN status ELSE 'applying' END,lease_owner=?1,lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000),fence=fence+1,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE p.id=(SELECT id FROM dns_change_plans WHERE status IN('confirmed','rollback_pending') AND next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND (lease_until IS NULL OR lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000)) ORDER BY created_at,id LIMIT 1)
  AND p.status IN('confirmed','rollback_pending')
  AND (p.lease_until IS NULL OR p.lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000)) RETURNING p.*;
-- name: PlatformExpireDNSClaims :execrows
-- scope: platform

UPDATE dns_change_plans SET status='uncertain',fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='DNS_WORKER_EXPIRED',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE status IN('applying','rollback_pending') AND lease_until<(CAST(strftime('%s','now') AS INTEGER)*1000000);
-- name: PlatformFenceDNSPlan :one
-- scope: platform

SELECT * FROM dns_change_plans WHERE id=?1 AND fence=?2 AND lease_owner=?3 AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status IN('applying','rollback_pending');
-- name: PlatformAdvanceDNSPlan :execrows
-- scope: platform

UPDATE dns_change_plans SET status=?4,last_error_code=?5,lease_owner=NULL,lease_until=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE id=?1 AND fence=?2 AND lease_owner=?3 AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status IN('applying','rollback_pending');
-- name: PlatformListDNSActions :many
-- scope: platform

SELECT * FROM dns_record_actions WHERE plan_id=?1 ORDER BY ordinal;
-- name: PlatformCreateDNSAction :exec
-- scope: platform

INSERT INTO dns_record_actions(tenant_id,plan_id,ordinal,kind,before_record,after_record) VALUES(?1,?2,?3,?4,?5,?6) ON CONFLICT(plan_id,ordinal) DO NOTHING;
-- name: PlatformSubmitDNSAction :execrows
-- scope: platform

UPDATE dns_record_actions a SET status='submitted',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) FROM dns_change_plans p WHERE p.id=a.plan_id AND a.id=?1 AND p.fence=?2 AND p.lease_owner=?3 AND p.status='applying' AND p.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND a.status='pending';
-- name: PlatformCompleteDNSAction :execrows
-- scope: platform

UPDATE dns_record_actions a SET status=?4,provider_record_id=?5,owned=?6,last_error_code=?7,observed_record=sqlc.narg(observed_record),updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) FROM dns_change_plans p WHERE p.id=a.plan_id AND a.id=?1 AND p.fence=?2 AND p.lease_owner=?3 AND p.status IN('applying','rollback_pending') AND p.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND a.status='submitted';
-- name: PlatformApplyManagedDNS :exec
-- scope: platform

INSERT INTO managed_dns_zones(tenant_id,domain_id,records,plan_id) VALUES(?1,?2,?3,?4) ON CONFLICT(domain_id) DO UPDATE SET records=EXCLUDED.records,plan_id=EXCLUDED.plan_id,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000);
-- name: PlatformSetExplicitDNS :exec
-- scope: platform

UPDATE domains SET dns_explicit=true,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id=?1 AND id=?2 AND mode='managed';
-- name: PlatformListManagedDNS :many
-- scope: platform

SELECT z.domain_id,z.tenant_id,d.fqdn,z.records,z.updated_at FROM managed_dns_zones z JOIN domains d ON d.id=z.domain_id AND d.tenant_id=z.tenant_id WHERE d.mode='managed' AND d.status='active' AND d.dns_explicit;
-- name: PlatformClaimOnboarding :one
-- scope: platform

UPDATE onboarding_tasks t SET status='running',leased_by=?1,leased_until=(CAST(strftime('%s','now') AS INTEGER)*1000000),fence=fence+1,attempts=attempts+1,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE t.id=(SELECT task.id FROM onboarding_tasks task JOIN domains d ON d.id=task.domain_id AND d.tenant_id=task.tenant_id
WHERE task.status IN('pending','running') AND task.next_attempt_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000) AND (task.leased_until IS NULL OR task.leased_until<(CAST(strftime('%s','now') AS INTEGER)*1000000)) AND task.attempts<task.max_attempts AND d.status='active' AND d.verified_at<=(CAST(strftime('%s','now') AS INTEGER)*1000000)
AND NOT EXISTS(SELECT 1 FROM onboarding_tasks held WHERE held.tenant_id=task.tenant_id AND held.domain_id=task.domain_id AND held.status='blocked' AND (coalesce(held.last_error_code,'')||coalesce(held.last_error,'')) LIKE '%RESTORE_RECONCILIATION_REQUIRED%' AND NOT EXISTS(SELECT 1 FROM dns_change_plans reviewed WHERE reviewed.tenant_id=held.tenant_id AND reviewed.domain_id=held.domain_id AND reviewed.status='completed' AND reviewed.updated_at>held.updated_at))
ORDER BY task.id LIMIT 1)
  AND t.status IN('pending','running')
  AND (t.leased_until IS NULL OR t.leased_until<(CAST(strftime('%s','now') AS INTEGER)*1000000)) RETURNING t.*;
-- name: PlatformFenceOnboarding :one
-- scope: platform

SELECT * FROM onboarding_tasks WHERE id=?1 AND fence=?2 AND leased_by=?3 AND leased_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status='running';
-- name: PlatformAdvanceOnboarding :execrows
-- scope: platform

UPDATE onboarding_tasks SET attempts=CASE WHEN step<>?4 THEN 0 ELSE attempts END,step=?4,status=?5,last_error_code=?6,desired_generation=?7,next_attempt_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)+make_interval(secs=>sqlc.arg(backoff_seconds)),leased_by=NULL,leased_until=NULL,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),completed_at=CASE WHEN ?5='completed' THEN (CAST(strftime('%s','now') AS INTEGER)*1000000) ELSE completed_at END WHERE id=?1 AND fence=?2 AND leased_by=?3 AND leased_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status='running';
-- name: PlatformPrepareOnboardingTenant :exec
-- scope: platform

UPDATE tenants SET status='active' WHERE tenants.id=?1 AND tenants.status='pending';
-- name: PlatformMarkDomainReady :execrows
-- scope: platform

UPDATE domains d SET provisioning_state='ready',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) FROM onboarding_tasks task WHERE d.tenant_id=?1 AND d.id=?2 AND task.tenant_id=d.tenant_id AND task.domain_id=d.id AND task.id=?3 AND task.fence=?4 AND task.leased_by=?5 AND task.leased_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND task.status='running' AND task.step='protocol_readiness' AND d.status='active';
-- name: PlatformProvisioningCertificateAllowed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM domains d WHERE d.fqdn=?1 AND d.status='active' AND d.custom_web_enabled AND (d.provisioning_state='ready' OR EXISTS(SELECT 1 FROM onboarding_tasks t WHERE t.domain_id=d.id AND t.tenant_id=d.tenant_id AND t.status IN('pending','running','completed') AND t.step IN('certificate_issue','mailbox_provision','configuration_apply','protocol_readiness','ready')))) AS allowed;

-- name: PlatformCustomWebEnabled :one
-- scope: platform

SELECT custom_web_enabled FROM domains WHERE fqdn=?1 AND status='active';
-- name: PlatformRenewDNSLease :execrows
-- scope: platform

UPDATE dns_change_plans SET lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE id=?1 AND fence=?2 AND lease_owner=?3 AND lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status IN('applying','rollback_pending');
-- name: PlatformRenewOnboardingLease :execrows
-- scope: platform

UPDATE onboarding_tasks SET leased_until=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE id=?1 AND fence=?2 AND leased_by=?3 AND leased_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND status='running';
-- name: PlatformSubmitDNSRollback :execrows
-- scope: platform

UPDATE dns_record_actions a SET status='submitted',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) FROM dns_change_plans p WHERE p.id=a.plan_id AND a.id=?1 AND p.fence=?2 AND p.lease_owner=?3 AND p.status='rollback_pending' AND p.lease_until>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND a.status='applied' AND (a.owned OR a.kind='update');
-- name: PlatformReviewDNSPlan :execrows
-- scope: platform

UPDATE dns_change_plans SET status='completed',fence=fence+1,lease_owner=NULL,lease_until=NULL,last_error_code='READ_ONLY_OBSERVATION_CONFIRMED',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE tenant_id=?1 AND id=?2 AND status='uncertain';
-- name: PlatformCurrentManagedDNS :one
-- scope: platform

SELECT * FROM managed_dns_zones WHERE tenant_id=?1 AND domain_id=?2;
-- name: PlatformExpireOnboarding :execrows
-- scope: platform

UPDATE onboarding_tasks SET status='failed',fence=fence+1,leased_by=NULL,leased_until=NULL,last_error_code='ONBOARDING_RETRIES_EXHAUSTED',updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE status IN('pending','running') AND attempts>=max_attempts AND (leased_until IS NULL OR leased_until<(CAST(strftime('%s','now') AS INTEGER)*1000000));
-- name: PlatformRefreshDNSOnboardingAlerts :exec
-- scope: platform

WITH sources AS(
 SELECT 'dns:'||id AS key,tenant_id,'dns_requires_review' AS kind FROM dns_change_plans WHERE status='uncertain' OR (status IN('confirmed','applying','rollback_pending') AND created_at<(CAST(strftime('%s','now') AS INTEGER)*1000000)-900000000)
 UNION ALL SELECT 'onboarding:'||id,tenant_id,'onboarding_requires_review' FROM onboarding_tasks WHERE status IN('failed','blocked') OR (status IN('pending','running') AND created_at<(CAST(strftime('%s','now') AS INTEGER)*1000000)-900000000)
), refreshed AS(
 INSERT INTO operational_alerts(alert_key,tenant_id,kind,state) SELECT key,tenant_id,kind,'firing' FROM sources
 ON CONFLICT(alert_key) DO UPDATE SET state='firing',last_seen_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),resolved_at=NULL RETURNING alert_key
)
UPDATE operational_alerts a SET state='resolved',resolved_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),last_seen_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE a.kind IN('dns_requires_review','onboarding_requires_review') AND a.state='firing' AND NOT EXISTS(SELECT 1 FROM sources s WHERE s.key=a.alert_key);
-- name: PlatformEnqueueDNSOnboardingAlerts :exec
-- scope: platform

INSERT INTO notification_outbox(tenant_id,kind,message_key,params,dedupe_key,channel,state)
SELECT a.tenant_id,a.kind,'notification.'||a.kind,json_object('workflow',CASE WHEN a.kind='dns_requires_review' THEN 'dns' ELSE 'onboarding' END),a.alert_key||':'||to_char((CAST(strftime('%s','now') AS INTEGER)*1000000),'YYYY-MM-DD-HH24'),'in_app','delivered' FROM operational_alerts a WHERE a.kind IN('dns_requires_review','onboarding_requires_review') AND a.state='firing' ON CONFLICT(dedupe_key) DO NOTHING;
