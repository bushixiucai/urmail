-- name: CreateSupportTicket :one
INSERT INTO tickets(tenant_id,opened_by,subject,priority) VALUES($1,$2,$3,$4) RETURNING *;

-- name: ListSupportTickets :many
SELECT * FROM tickets WHERE tenant_id=$1 AND opened_by=$2 AND id < @before_id::bigint ORDER BY id DESC LIMIT 51;

-- name: GetSupportTicket :one
SELECT * FROM tickets WHERE tenant_id=$1 AND opened_by=$2 AND id=$3;

-- name: AddSupportMessage :one
INSERT INTO ticket_messages(tenant_id,ticket_id,author_id,body,is_staff) VALUES($1,$2,$3,$4,false) RETURNING *;

-- name: ListSupportMessages :many
SELECT * FROM ticket_messages WHERE tenant_id=$1 AND ticket_id=$2 AND id > @after_id::bigint ORDER BY id LIMIT 101;

-- name: ChangeSupportTicketStatus :one
UPDATE tickets SET status=$4 WHERE tenant_id=$1 AND opened_by=$2 AND id=$3 AND status<>$4 RETURNING *;

-- name: AddSupportAttachment :one
INSERT INTO ticket_attachments(tenant_id,message_id,filename,content_type,content) VALUES($1,$2,$3,$4,$5) RETURNING id,tenant_id,message_id,filename,content_type,created_at;

-- name: ListSupportAttachments :many
SELECT a.id,a.message_id,a.filename,a.content_type,a.created_at FROM ticket_attachments a JOIN ticket_messages m ON m.tenant_id=a.tenant_id AND m.id=a.message_id WHERE a.tenant_id=$1 AND m.ticket_id=$2 ORDER BY a.id LIMIT 500;

-- name: GetSupportAttachment :one
SELECT * FROM ticket_attachments WHERE tenant_id=$1 AND id=$2;

-- name: ListVisibleAnnouncements :many
SELECT a.* FROM announcements a JOIN tenants t ON t.id = @tenant_id::bigint WHERE (a.brand_id IS NULL OR a.brand_id=t.brand_id) AND a.state='published' AND a.published_at<=now() AND (a.ends_at IS NULL OR a.ends_at>now()) ORDER BY a.published_at DESC,a.id DESC LIMIT 100;
