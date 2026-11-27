-- name: PlatformInsertAIDraft :one
INSERT INTO mailbox_ai_drafts(tenant_id,mailbox_id,subject,body,version)
VALUES($1,$2,$3,$4,1) RETURNING id,version;

-- name: PlatformUpdateAIDraft :one
UPDATE mailbox_ai_drafts SET subject=$4, body=$5, version=version+1, updated_at=now()
WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND version=$6
RETURNING version;

-- name: PlatformGetAIDraft :one
SELECT id, subject, body, version FROM mailbox_ai_drafts
WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3;
