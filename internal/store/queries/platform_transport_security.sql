-- name: PlatformListTransportSecurity :many
SELECT t.*,d.fqdn FROM domain_transport_security t JOIN domains d ON d.id=t.domain_id AND d.tenant_id=t.tenant_id
WHERE t.enabled AND d.status='active' AND d.verified_at IS NOT NULL ORDER BY t.tenant_id,t.domain_id;

-- name: PlatformPublicTransportSecurity :one
SELECT t.*,d.fqdn FROM domain_transport_security t JOIN domains d ON d.id=t.domain_id AND d.tenant_id=t.tenant_id
WHERE d.fqdn=$1 AND t.enabled AND d.status='active' AND d.verified_at IS NOT NULL;

-- name: PlatformTransportCertificateAllowed :one
SELECT EXISTS(SELECT 1 FROM domain_transport_security t JOIN domains d ON d.id=t.domain_id AND d.tenant_id=t.tenant_id
WHERE 'mta-sts.'||d.fqdn=$1 AND t.enabled AND d.status='active' AND d.verified_at IS NOT NULL)::boolean AS allowed;

-- name: PlatformCountRecentTLSReports :one
SELECT count(*)::bigint FROM domain_tls_reports WHERE tenant_id=$1 AND domain_id=$2 AND received_at>=now()-interval '1 day';

-- name: PlatformRecordTLSReport :execrows
INSERT INTO domain_tls_reports(tenant_id,domain_id,report_digest,reporter_digest,report_id_digest,period_start,period_end,successful,failed,failures)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING;

-- name: PlatformFindTLSReportFingerprint :one
SELECT report_digest FROM domain_tls_reports
WHERE tenant_id=$1 AND domain_id=$2 AND reporter_digest=$3 AND report_id_digest=$4 AND period_start=$5 AND period_end=$6;
