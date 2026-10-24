-- name: PlatformListSupportTickets :many
SELECT * FROM tickets WHERE id < @before_id::bigint ORDER BY id DESC LIMIT 51;

-- name: PlatformGetSupportTicket :one
SELECT * FROM tickets WHERE tenant_id=$1 AND id=$2 FOR UPDATE;

-- name: PlatformListSupportMessages :many
SELECT * FROM ticket_messages WHERE tenant_id=$1 AND ticket_id=$2 AND id > @after_id::bigint ORDER BY id LIMIT 101;

-- name: PlatformReplySupportTicket :one
INSERT INTO ticket_messages(tenant_id,ticket_id,author_id,body,is_staff) VALUES($1,$2,$3,$4,true) RETURNING *;

-- name: PlatformChangeSupportTicket :one
UPDATE tickets t SET status=$3,assigned_to=$4,updated_at=now() WHERE t.tenant_id=$1 AND t.id=$2 AND ($4::bigint IS NULL OR EXISTS(SELECT 1 FROM platform_admins p JOIN users u ON u.id=p.user_id WHERE p.user_id=$4 AND u.status='active')) RETURNING *;

-- name: PlatformListAnnouncements :many
SELECT * FROM announcements ORDER BY id DESC LIMIT 100;

-- name: PlatformCreateAnnouncement :one
INSERT INTO announcements(brand_id,title,body,state,published_at,ends_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING *;

-- name: PlatformUpdateAnnouncement :one
UPDATE announcements SET brand_id=$2,title=$3,body=$4,state=$5,published_at=$6,ends_at=$7 WHERE id=$1 RETURNING *;
