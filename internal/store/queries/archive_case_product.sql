-- name: ArchiveCasesCreate :one
-- scope: platform

SELECT urmail_create_archive_case(@tenant_id,@actor_id,@title,@purpose,@expires_at,@request_key) AS id;

-- name: ArchiveCasesList :many
-- scope: tenant

SELECT * FROM archive_investigations WHERE tenant_id=?1 ORDER BY id DESC LIMIT 100;

-- name: ArchiveCasesGet :one
-- scope: tenant

SELECT * FROM archive_investigations WHERE tenant_id=?1 AND id=?2;

-- name: ArchiveCasesLock :one
-- scope: tenant

SELECT * FROM archive_investigations WHERE tenant_id=?1 AND id=?2;

-- name: ArchiveCasesSnapshot :one
-- scope: platform

SELECT urmail_archive_case_snapshot(?1,?2,?3) AS snapshot;

-- name: ArchiveCasesSnapshotDigest :one
-- scope: platform

SELECT encode(sha256(convert_to(urmail_archive_case_snapshot(?1,?2,?3),'UTF8')),'hex') AS digest;

-- name: ArchiveCasesMembers :many
-- scope: tenant

SELECT m.user_id,u.email,m.removed_at FROM archive_investigation_members m JOIN users u ON u.id=m.user_id
WHERE m.tenant_id=?1 AND m.investigation_id=?2 ORDER BY m.user_id;

-- name: ArchiveCasesEligibleMembers :many
-- scope: tenant

SELECT r.user_id,u.email FROM user_tenant_roles r JOIN users u ON u.id=r.user_id
WHERE r.tenant_id=?1 AND EXISTS(SELECT 1 FROM compliance_grants cg JOIN user_tenant_roles ur ON ur.tenant_id=cg.tenant_id AND ur.user_id=cg.user_id WHERE cg.tenant_id=r.tenant_id AND cg.user_id='full_content' AND cg.scope=r.user_id AND NOT cg.revoked AND cg.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND cg.owner_confirmed_at IS NOT NULL AND cg.granted_by IS NOT NULL AND cg.granted_by<>'full_content' AND length(trim(cg.reason))>0 AND ur.role IN('owner','admin')) ORDER BY u.email;

-- name: ArchiveCasesGrantEligible :one
-- scope: platform

SELECT EXISTS(SELECT 1 FROM compliance_grants cg JOIN user_tenant_roles ur ON ur.tenant_id=cg.tenant_id AND ur.user_id=cg.user_id WHERE cg.tenant_id=?1 AND cg.user_id=?3 AND cg.scope=?2 AND NOT cg.revoked AND cg.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND cg.owner_confirmed_at IS NOT NULL AND cg.granted_by IS NOT NULL AND cg.granted_by<>?3 AND length(trim(cg.reason))>0 AND ur.role IN('owner','admin')) AS allowed;

-- name: ArchiveCasesAddMember :execrows
-- scope: tenant

INSERT INTO archive_investigation_members(tenant_id,investigation_id,user_id)
SELECT @tenant_id,i.id,@user_id FROM archive_investigations i
WHERE i.tenant_id= @tenant_id AND i.id= @case_id AND i.created_by= @actor_id AND i.status='open' AND i.version= @version
AND EXISTS(SELECT 1 FROM compliance_grants cg JOIN user_tenant_roles ur ON ur.tenant_id=cg.tenant_id AND ur.user_id=cg.user_id WHERE cg.tenant_id=@tenant_id AND cg.user_id='full_content' AND cg.scope=@user_id AND NOT cg.revoked AND cg.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000) AND cg.owner_confirmed_at IS NOT NULL AND cg.granted_by IS NOT NULL AND cg.granted_by<>'full_content' AND length(trim(cg.reason))>0 AND ur.role IN('owner','admin'))
ON CONFLICT(investigation_id,user_id) DO UPDATE SET removed_at=NULL;

-- name: ArchiveCasesRemoveMember :execrows
-- scope: tenant

UPDATE archive_investigation_members SET removed_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
 WHERE tenant_id=?1 AND investigation_id=?2 AND user_id=?3 AND removed_at IS NULL AND EXISTS(SELECT 1 FROM archive_investigations i WHERE i.tenant_id=tenant_id AND i.id=investigation_id AND i.created_by=?4 AND i.created_by<>user_id AND i.status='open' AND i.version=?5);-- name: ArchiveCasesSources :many
-- scope: tenant

SELECT e.id,e.mailbox_id,e.path,e.subject,e.size_bytes,e.raw_sha256,e.deleted_at,e.legal_hold
FROM archive_investigation_sources s JOIN archive_entries e ON e.tenant_id=s.tenant_id AND e.id=s.archive_entry_id
WHERE s.tenant_id=?1 AND s.investigation_id=?2 ORDER BY e.id;

-- name: ArchiveCasesAddSource :execrows
-- scope: tenant

INSERT INTO archive_investigation_sources(tenant_id,investigation_id,archive_entry_id,added_by)
SELECT @tenant_id,i.id,e.id,@actor_id FROM archive_investigations i JOIN archive_entries e ON e.tenant_id=i.tenant_id
WHERE i.tenant_id= @tenant_id AND i.id= @case_id AND i.status='open' AND i.version= @version AND e.id= @entry_id
AND e.deleted_at IS NULL AND e.mailbox_id IS NOT NULL AND e.raw_sha256 ~ '^[0-9a-f]{64}$'
ON CONFLICT(investigation_id,archive_entry_id) DO NOTHING;

-- name: ArchiveCasesRemoveSource :execrows
-- scope: tenant

DELETE FROM archive_investigation_sources s USING archive_investigations i
WHERE s.tenant_id=?1 AND s.investigation_id=?2 AND s.archive_entry_id=?3
AND i.tenant_id=s.tenant_id AND i.id=s.investigation_id AND i.status='open' AND i.version=?4;

-- name: ArchiveCasesClose :one
-- scope: tenant

UPDATE archive_investigations SET status='closed' WHERE tenant_id=?1 AND id=?2 AND created_by=?3 AND status='open' AND version=?4 RETURNING *;

-- name: ArchiveCasesApprovals :many
-- scope: tenant

SELECT id,tenant_id,investigation_id,requester_user_id,action,source_digest,state,approver_user_id,decided_at,expires_at,case_version,source_snapshot,decision_reason
FROM archive_approval_requests WHERE tenant_id=?1 AND investigation_id=?2 AND case_version>0 ORDER BY id DESC LIMIT 100;

-- name: ArchiveCasesApproval :one
-- scope: tenant

SELECT id,tenant_id,investigation_id,requester_user_id,action,source_digest,state,approver_user_id,decided_at,expires_at,case_version,source_snapshot,decision_reason
FROM archive_approval_requests WHERE tenant_id=?1 AND investigation_id=?2 AND id=?3 AND case_version>0;

-- scope: tenant

-- name: ArchiveCasesGetPendingApproval :one
-- scope: tenant

SELECT r.id FROM archive_approval_requests r
WHERE r.tenant_id=@tenant_id AND r.investigation_id=@case_id
 AND r.requester_user_id=@actor_id AND r.source_digest=@digest AND r.state='pending'
 AND r.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)
ORDER BY r.id LIMIT 1;

-- name: ArchiveCasesInsertApproval :one
-- scope: tenant

INSERT INTO archive_approval_requests(tenant_id, investigation_id, requester_user_id, action, source_digest, expires_at, case_version, source_snapshot)
SELECT @tenant_id, i.id, @actor_id, 'export', @digest,
 min(i.expires_at, (CAST(strftime('%s','now') AS INTEGER)*1000000)), i.version, sqlc.arg(source_snapshot)
FROM archive_investigations i
WHERE i.tenant_id=@tenant_id AND i.id=@case_id AND i.status='open' AND i.version=@version
RETURNING id;
-- name: ArchiveCasesDecideApproval :one
-- scope: tenant

UPDATE archive_approval_requests SET state= @decision,approver_user_id= @actor_id,decided_at=(CAST(strftime('%s','now') AS INTEGER)*1000000),decision_reason= @reason
WHERE tenant_id= @tenant_id AND investigation_id= @case_id AND id= @approval_id AND state='pending' AND expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)
AND requester_user_id<> @actor_id AND source_digest= @digest RETURNING id;

-- name: ArchiveCasesWithdrawApproval :one
-- scope: tenant

UPDATE archive_approval_requests SET state='withdrawn',decision_reason=?5,decided_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND investigation_id=?2 AND id=?3 AND requester_user_id=?4 AND state='pending' RETURNING id;

-- name: ArchiveCasesExportVisible :one
-- scope: platform

SELECT urmail_archive_export_visible(?1,?2,?3) AS visible;

-- name: ArchiveCasesSaveEvidence :one
-- scope: tenant

INSERT INTO archive_evidence_exports(tenant_id,approval_id,created_by,manifest,manifest_sha256)
VALUES(?1,?2,?3,?4,?5) ON CONFLICT(tenant_id,approval_id) DO NOTHING RETURNING *;

-- name: ArchiveCasesGetEvidence :one
-- scope: tenant

SELECT * FROM archive_evidence_exports WHERE tenant_id=?1 AND approval_id=?2;
