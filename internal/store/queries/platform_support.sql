-- name: PlatformListSupportTickets :many
-- scope: platform

SELECT * FROM tickets WHERE id < @before_id ORDER BY id DESC LIMIT 51;

-- name: PlatformGetSupportTicket :one
-- scope: platform

SELECT * FROM tickets WHERE tenant_id=?1 AND id=?2;

-- name: PlatformListSupportMessages :many
-- scope: platform

SELECT * FROM ticket_messages WHERE tenant_id=?1 AND ticket_id=?2 AND id > @after_id ORDER BY id LIMIT 101;

-- name: PlatformReplySupportTicket :one
-- scope: platform

INSERT INTO ticket_messages(tenant_id,ticket_id,author_id,body,is_staff) VALUES(?1,?2,?3,?4,true) RETURNING *;

-- name: PlatformChangeSupportTicket :one
-- scope: platform

UPDATE tickets t SET status=?3,assigned_to=?4,updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000) WHERE t.tenant_id=?1 AND t.id=?2 AND (?4 IS NULL OR EXISTS(SELECT 1 FROM platform_admins p JOIN users u ON u.id=p.user_id WHERE p.user_id=?4 AND u.status='active')) RETURNING *;

-- name: PlatformListAnnouncements :many
-- scope: platform

SELECT * FROM announcements ORDER BY id DESC LIMIT 100;

-- name: PlatformCreateAnnouncement :one
-- scope: platform

INSERT INTO announcements(brand_id,title,body,state,published_at,ends_at) VALUES(?1,?2,?3,?4,?5,?6) RETURNING *;

-- name: PlatformUpdateAnnouncement :one
-- scope: platform

UPDATE announcements SET brand_id=?2,title=?3,body=?4,state=?5,published_at=?6,ends_at=?7 WHERE id=?1 RETURNING *;
