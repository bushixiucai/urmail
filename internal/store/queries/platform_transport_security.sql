-- name: PlatformListTransportSecurity :many
-- scope: platform

SELECT t.id, t.tenant_id, t.domain_id, t.mtasts_policy_id, t.mtasts_mode, t.tls_rpt_rua, t.created_at, t.updated_at, t.enabled, t.policy_mx, t.max_age, coalesce(t.published_at, 0) AS published_at, coalesce(t.testing_since, 0) AS testing_since, t.cache_safe_after, t.observation_digest, coalesce(t.observed_at, 0) AS observed_at, t.observation_status, t.observation_detail, t.version, d.fqdn FROM domain_transport_security t JOIN domains d ON d.id=t.domain_id AND d.tenant_id=t.tenant_id
WHERE t.enabled AND d.status='active' AND d.verified_at IS NOT NULL ORDER BY t.tenant_id,t.domain_id;

-- name: PlatformPublicTransportSecurity :one
-- scope: platform

SELECT t.id, t.tenant_id, t.domain_id, t.mtasts_policy_id, t.mtasts_mode, t.tls_rpt_rua, t.created_at, t.updated_at, t.enabled, t.policy_mx, t.max_age, coalesce(t.published_at, 0) AS published_at, coalesce(t.testing_since, 0) AS testing_since, t.cache_safe_after, t.observation_digest, coalesce(t.observed_at, 0) AS observed_at, t.observation_status, t.observation_detail, t.version, d.fqdn FROM domain_transport_security t JOIN domains d ON d.id=t.domain_id AND d.tenant_id=t.tenant_id
WHERE d.fqdn=?1 AND t.enabled AND d.status='active' AND d.verified_at IS NOT NULL;

-- name: PlatformTransportCertificateAllowed :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM domain_transport_security t JOIN domains d ON d.id=t.domain_id AND d.tenant_id=t.tenant_id
WHERE 'mta-sts.'||d.fqdn=?1 AND t.enabled AND d.status='active' AND d.verified_at IS NOT NULL) AS allowed;

-- name: PlatformCountRecentTLSReports :one
-- scope: platform

SELECT count(*) FROM domain_tls_reports WHERE tenant_id=?1 AND domain_id=?2 AND received_at>=(CAST(strftime('%s','now') AS INTEGER)*1000000)-86400000000;

-- name: PlatformRecordTLSReport :execrows
-- scope: platform

INSERT INTO domain_tls_reports(tenant_id,domain_id,report_digest,reporter_digest,report_id_digest,period_start,period_end,successful,failed,failures)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10) ON CONFLICT DO NOTHING;

-- name: PlatformFindTLSReportFingerprint :one
-- scope: platform

SELECT report_digest FROM domain_tls_reports
WHERE tenant_id=?1 AND domain_id=?2 AND reporter_digest=?3 AND report_id_digest=?4 AND period_start=?5 AND period_end=?6;
