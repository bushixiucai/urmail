-- name: GetMailboxEASPingConfig :one
SELECT ping_heartbeat, ping_folders FROM mailbox_eas_devices
WHERE tenant_id=$1 AND mailbox_id=$2 AND device_id=$3;

-- name: SetMailboxEASPingConfig :execrows
UPDATE mailbox_eas_devices SET ping_heartbeat=$4, ping_folders=$5
WHERE tenant_id=$1 AND mailbox_id=$2 AND device_id=$3;

-- name: MailboxEASPingCollectionChanged :one
SELECT EXISTS(SELECT 1 FROM mailbox_changes c
WHERE c.tenant_id=$1 AND c.mailbox_id=$2 AND c.id>$5 AND (
 (c.resource_type=$3 AND c.resource_id=$4)
 OR ($3='addressbook' AND c.resource_type='contact' AND c.resource_id IN (
  SELECT p.id FROM mailbox_contacts p WHERE p.tenant_id=$1 AND p.mailbox_id=$2 AND p.addressbook_id=$4))
 OR ($3='calendar' AND c.resource_type='event' AND c.resource_id IN (
  SELECT e.id FROM mailbox_events e WHERE e.tenant_id=$1 AND e.mailbox_id=$2 AND e.calendar_id=$4))
));
