-- name: GetDomainTransportSecurity :one
-- scope: tenant

SELECT * FROM domain_transport_security WHERE tenant_id=?1 AND domain_id=?2;

-- name: PublishDomainTransportSecurity :one
-- scope: tenant

INSERT INTO domain_transport_security(tenant_id,domain_id,mtasts_policy_id,mtasts_mode,tls_rpt_rua,enabled,policy_mx,max_age,published_at,testing_since,cache_safe_after)
SELECT sqlc.arg('tenant_id'),sqlc.arg('domain_id'),sqlc.arg('policy_id'),sqlc.arg('mode'),sqlc.arg('rua'),true,sqlc.arg('mx'),sqlc.arg('max_age'),(CAST(strftime('%s','now') AS INTEGER)*1000000),(CAST(strftime('%s','now') AS INTEGER)*1000000),(CAST(strftime('%s','now') AS INTEGER)*1000000)+make_interval(secs=>sqlc.arg('cache_seconds'))
WHERE (sqlc.arg('expected_version')>0 OR NOT EXISTS(SELECT 1 FROM domain_transport_security WHERE tenant_id=sqlc.arg('tenant_id') AND domain_id=sqlc.arg('domain_id')))
ON CONFLICT(tenant_id,domain_id) DO UPDATE SET
 mtasts_policy_id=excluded.mtasts_policy_id,mtasts_mode=excluded.mtasts_mode,tls_rpt_rua=excluded.tls_rpt_rua,
 enabled=true,policy_mx=excluded.policy_mx,max_age=excluded.max_age,published_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),
 testing_since=CASE WHEN domain_transport_security.policy_mx=excluded.policy_mx AND domain_transport_security.mtasts_mode='testing' AND excluded.mtasts_mode='enforce' THEN domain_transport_security.testing_since ELSE (CAST(strftime('%s','now') AS INTEGER)*1000000) END,
 cache_safe_after=max(domain_transport_security.cache_safe_after,excluded.cache_safe_after),observation_digest=NULL,observed_at=NULL,observation_status='unverified',observation_detail='{}',version=domain_transport_security.version+1,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE domain_transport_security.version=sqlc.arg('expected_version') RETURNING *;

-- name: ObserveDomainTransportSecurity :execrows
-- scope: tenant

UPDATE domain_transport_security SET observation_digest=?3,observed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),observation_status=?4,observation_detail=?5
WHERE tenant_id=?1 AND domain_id=?2 AND version=?6;

-- name: DisableDomainTransportSecurity :execrows
-- scope: tenant

UPDATE domain_transport_security SET enabled=false,version=version+1,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND domain_id=?2 AND version=?3 AND mtasts_mode='none' AND cache_safe_after<=(CAST(strftime('%s','now') AS INTEGER)*1000000);

-- name: ListDomainTLSReports :many
-- scope: tenant

SELECT id,report_digest,period_start,period_end,successful,failed,failures,received_at,source_verified
FROM domain_tls_reports WHERE tenant_id=?1 AND domain_id=?2 ORDER BY received_at DESC,id DESC LIMIT 100;
