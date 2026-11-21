-- name: ArchiveCasesCreate :one
SELECT urmail_create_archive_case(@tenant_id::bigint,@actor_id::bigint,@title::text,@purpose::text,@expires_at::timestamptz,@request_key::text)::bigint AS id;

-- name: ArchiveCasesList :many
SELECT * FROM archive_investigations WHERE tenant_id=$1 ORDER BY id DESC LIMIT 100;

-- name: ArchiveCasesGet :one
SELECT * FROM archive_investigations WHERE tenant_id=$1 AND id=$2;

-- name: ArchiveCasesLock :one
SELECT * FROM archive_investigations WHERE tenant_id=$1 AND id=$2 FOR UPDATE;

-- name: ArchiveCasesSnapshot :one
SELECT urmail_archive_case_snapshot($1,$2,$3)::jsonb AS snapshot;

-- name: ArchiveCasesSnapshotDigest :one
SELECT encode(sha256(convert_to(urmail_archive_case_snapshot($1,$2,$3)::text,'UTF8')),'hex')::text AS digest;

-- name: ArchiveCasesMembers :many
SELECT m.user_id,u.email,m.removed_at FROM archive_investigation_members m JOIN users u ON u.id=m.user_id
WHERE m.tenant_id=$1 AND m.investigation_id=$2 ORDER BY m.user_id;

-- name: ArchiveCasesEligibleMembers :many
SELECT r.user_id,u.email FROM user_tenant_roles r JOIN users u ON u.id=r.user_id
WHERE r.tenant_id=$1 AND urmail_archive_case_grant(r.tenant_id,r.user_id,'full_content') ORDER BY u.email;

-- name: ArchiveCasesGrantEligible :one
SELECT urmail_archive_case_grant($1,$2,$3)::boolean AS allowed;

-- name: ArchiveCasesAddMember :execrows
INSERT INTO archive_investigation_members(tenant_id,investigation_id,user_id)
SELECT @tenant_id::bigint,i.id,@user_id::bigint FROM archive_investigations i
WHERE i.tenant_id= @tenant_id AND i.id= @case_id AND i.created_by= @actor_id AND i.status='open' AND i.version= @version
AND urmail_archive_case_grant(@tenant_id,@user_id,'full_content')
ON CONFLICT(investigation_id,user_id) DO UPDATE SET removed_at=NULL;

-- name: ArchiveCasesRemoveMember :execrows
UPDATE archive_investigation_members m SET removed_at=now()
FROM archive_investigations i WHERE m.tenant_id=$1 AND m.investigation_id=$2 AND m.user_id=$3 AND m.removed_at IS NULL
AND i.tenant_id=m.tenant_id AND i.id=m.investigation_id AND i.created_by=$4 AND i.created_by<>m.user_id AND i.status='open' AND i.version=$5;

-- name: ArchiveCasesSources :many
SELECT e.id,e.mailbox_id,e.path,e.subject,e.size_bytes,e.raw_sha256,e.deleted_at,e.legal_hold
FROM archive_investigation_sources s JOIN archive_entries e ON e.tenant_id=s.tenant_id AND e.id=s.archive_entry_id
WHERE s.tenant_id=$1 AND s.investigation_id=$2 ORDER BY e.id;

-- name: ArchiveCasesAddSource :execrows
INSERT INTO archive_investigation_sources(tenant_id,investigation_id,archive_entry_id,added_by)
SELECT @tenant_id::bigint,i.id,e.id,@actor_id::bigint FROM archive_investigations i JOIN archive_entries e ON e.tenant_id=i.tenant_id
WHERE i.tenant_id= @tenant_id AND i.id= @case_id AND i.status='open' AND i.version= @version AND e.id= @entry_id
AND e.deleted_at IS NULL AND e.mailbox_id IS NOT NULL AND e.raw_sha256 ~ '^[0-9a-f]{64}$'
ON CONFLICT(investigation_id,archive_entry_id) DO NOTHING;

-- name: ArchiveCasesRemoveSource :execrows
DELETE FROM archive_investigation_sources s USING archive_investigations i
WHERE s.tenant_id=$1 AND s.investigation_id=$2 AND s.archive_entry_id=$3
AND i.tenant_id=s.tenant_id AND i.id=s.investigation_id AND i.status='open' AND i.version=$4;

-- name: ArchiveCasesClose :one
UPDATE archive_investigations SET status='closed' WHERE tenant_id=$1 AND id=$2 AND created_by=$3 AND status='open' AND version=$4 RETURNING *;

-- name: ArchiveCasesApprovals :many
SELECT id,tenant_id,investigation_id,requester_user_id,action,source_digest,state,approver_user_id,decided_at,expires_at,case_version,source_snapshot,decision_reason
FROM archive_approval_requests WHERE tenant_id=$1 AND investigation_id=$2 AND case_version>0 ORDER BY id DESC LIMIT 100;

-- name: ArchiveCasesApproval :one
SELECT id,tenant_id,investigation_id,requester_user_id,action,source_digest,state,approver_user_id,decided_at,expires_at,case_version,source_snapshot,decision_reason
FROM archive_approval_requests WHERE tenant_id=$1 AND investigation_id=$2 AND id=$3 AND case_version>0 FOR UPDATE;

-- name: ArchiveCasesRequestApproval :one
WITH existing AS (
 SELECT r.id FROM archive_approval_requests r WHERE r.tenant_id= @tenant_id AND r.investigation_id= @case_id
 AND r.requester_user_id= @actor_id AND r.source_digest= @digest AND r.state='pending' AND r.expires_at>now()
 ORDER BY r.id LIMIT 1
), inserted AS (
 INSERT INTO archive_approval_requests(tenant_id,investigation_id,requester_user_id,action,source_digest,expires_at,case_version,source_snapshot)
 SELECT @tenant_id::bigint,i.id,@actor_id::bigint,'export',@digest::text,least(i.expires_at,now()+interval '24 hours'),i.version,urmail_archive_case_snapshot(i.tenant_id,i.id,@actor_id)
 FROM archive_investigations i WHERE i.tenant_id= @tenant_id AND i.id= @case_id AND i.status='open' AND i.version= @version AND NOT EXISTS(SELECT 1 FROM existing)
 RETURNING id
) SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1;

-- name: ArchiveCasesDecideApproval :one
UPDATE archive_approval_requests SET state= @decision::text,approver_user_id= @actor_id::bigint,decided_at=now(),decision_reason= @reason::text
WHERE tenant_id= @tenant_id AND investigation_id= @case_id AND id= @approval_id AND state='pending' AND expires_at>now()
AND requester_user_id<> @actor_id AND source_digest= @digest RETURNING id;

-- name: ArchiveCasesWithdrawApproval :one
UPDATE archive_approval_requests SET state='withdrawn',decision_reason=$5,decided_at=now()
WHERE tenant_id=$1 AND investigation_id=$2 AND id=$3 AND requester_user_id=$4 AND state='pending' RETURNING id;

-- name: ArchiveCasesExportVisible :one
SELECT urmail_archive_export_visible($1,$2,$3)::boolean AS visible;

-- name: ArchiveCasesSaveEvidence :one
INSERT INTO archive_evidence_exports(tenant_id,approval_id,created_by,manifest,manifest_sha256)
VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,approval_id) DO NOTHING RETURNING *;

-- name: ArchiveCasesGetEvidence :one
SELECT * FROM archive_evidence_exports WHERE tenant_id=$1 AND approval_id=$2;
