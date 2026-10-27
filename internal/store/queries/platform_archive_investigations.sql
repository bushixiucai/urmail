-- name: PlatformCreateInvestigation :one
INSERT INTO archive_investigations(tenant_id,title,purpose,created_by,expires_at)
VALUES($1,$2,$3,$4,$5) RETURNING id;

-- name: PlatformGrantInvestigation :exec
INSERT INTO archive_investigation_members(investigation_id,tenant_id,user_id)
VALUES($1,$2,$3)
ON CONFLICT(investigation_id,user_id) DO UPDATE SET removed_at=NULL;

-- name: PlatformRevokeInvestigation :exec
UPDATE archive_investigation_members SET removed_at=now()
WHERE tenant_id=$1 AND investigation_id=$2 AND user_id=$3 AND removed_at IS NULL;

-- name: PlatformAddInvestigationSource :exec
INSERT INTO archive_investigation_sources(investigation_id,tenant_id,archive_entry_id,added_by)
SELECT $1,$2,e.id,$4 FROM archive_entries e
WHERE e.tenant_id=$2 AND e.id=$3
ON CONFLICT(investigation_id,archive_entry_id) DO NOTHING;

-- name: PlatformListInvestigationSources :many
SELECT s.archive_entry_id FROM archive_investigation_sources s
JOIN archive_investigation_members m ON m.tenant_id=s.tenant_id AND m.investigation_id=s.investigation_id
JOIN archive_investigations i ON i.tenant_id=s.tenant_id AND i.id=s.investigation_id
WHERE s.tenant_id=$1 AND s.investigation_id=$2 AND m.user_id=$3 AND m.removed_at IS NULL
 AND i.status='open' AND i.expires_at>now()
ORDER BY s.archive_entry_id;
