-- Mailbox-owned contacts and calendars. Every read and write names both
-- tenant_id and mailbox_id. Message bodies are not stored here.

-- name: PlatformEnsureAddressbook :one
-- scope: platform

INSERT INTO mailbox_addressbooks(tenant_id, mailbox_id, name)
VALUES(sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(name))
ON CONFLICT (tenant_id, mailbox_id, lower(name)) WHERE deleted_at IS NULL DO NOTHING
RETURNING *;

-- name: PlatformGetAddressbookByName :one
-- scope: platform

SELECT * FROM mailbox_addressbooks
WHERE tenant_id=?1 AND mailbox_id=?2 AND name=?3 AND deleted_at IS NULL;

-- name: PlatformListContacts :many
-- scope: platform

SELECT * FROM mailbox_contacts
WHERE tenant_id=?1 AND mailbox_id=?2 AND deleted_at IS NULL
ORDER BY lower(display_name), id
LIMIT sqlc.arg(page_limit);

-- name: PlatformGetContact :one
-- scope: platform

SELECT * FROM mailbox_contacts
WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND deleted_at IS NULL;

-- name: PlatformInsertContact :one
-- scope: platform

INSERT INTO mailbox_contacts(tenant_id, mailbox_id, addressbook_id, display_name, given_name, family_name, emails, phones, note)
VALUES(sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(addressbook_id), sqlc.arg(display_name), sqlc.arg(given_name), sqlc.arg(family_name), sqlc.arg(emails), sqlc.arg(phones), sqlc.arg(note))
RETURNING *;
-- name: PlatformUpdateContact :one
-- scope: platform

UPDATE mailbox_contacts SET display_name=sqlc.arg(display_name), given_name=sqlc.arg(given_name), family_name=sqlc.arg(family_name),
 emails=sqlc.arg(emails), phones=sqlc.arg(phones), note=sqlc.arg(note),
 version=version+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND version=sqlc.arg(expected_version) AND deleted_at IS NULL
RETURNING *;
-- name: PlatformDeleteContact :one
-- scope: platform

-- 成员清理（原 removed CTE）拆为顺序查询 PlatformDeleteContactMembers，与本体同事务依序执行
UPDATE mailbox_contacts SET deleted_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), version=version+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND version=?4 AND deleted_at IS NULL
RETURNING *;
-- name: PlatformSearchContacts :many
-- scope: platform

SELECT * FROM mailbox_contacts
WHERE tenant_id=?1 AND mailbox_id=?2 AND deleted_at IS NULL
 AND length(sqlc.arg(query)) BETWEEN 1 AND 100
 AND (
  display_name LIKE '%' || replace(replace(replace(sqlc.arg(query), '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  OR emails LIKE '%' || replace(replace(replace(sqlc.arg(query), '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
 )
ORDER BY lower(display_name), id
LIMIT 20;

-- name: PlatformEnsureCalendar :one
-- scope: platform

INSERT INTO mailbox_calendars(tenant_id, mailbox_id, name, timezone)
VALUES(?1, ?2, ?3, ?4)
ON CONFLICT (tenant_id, mailbox_id, lower(name)) WHERE deleted_at IS NULL DO NOTHING
RETURNING *;

-- name: PlatformGetCalendarByName :one
-- scope: platform

SELECT * FROM mailbox_calendars
WHERE tenant_id=?1 AND mailbox_id=?2 AND name=?3 AND deleted_at IS NULL;

-- name: PlatformListEvents :many
-- scope: platform

SELECT * FROM mailbox_events
WHERE tenant_id=?1 AND mailbox_id=?2 AND deleted_at IS NULL
 AND (rrule<>'' OR (starts_at<sqlc.arg(range_end) AND ends_at>sqlc.arg(range_start)))
ORDER BY starts_at, id
LIMIT sqlc.arg(page_limit);

-- name: PlatformGetEvent :one
-- scope: platform

SELECT * FROM mailbox_events
WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3 AND deleted_at IS NULL;

-- name: PlatformInsertEvent :one
-- scope: platform

INSERT INTO mailbox_events(tenant_id, mailbox_id, calendar_id, uid, summary, description, location,
 starts_at, ends_at, all_day, timezone, rrule, status, attendees, recurrence_overrides, reminders)
VALUES(sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(calendar_id), sqlc.arg(uid),
 sqlc.arg(summary), sqlc.arg(description), sqlc.arg(location),
 sqlc.arg(starts_at), sqlc.arg(ends_at), sqlc.arg(all_day), sqlc.arg(timezone),
 sqlc.arg(rrule), sqlc.arg(status), sqlc.arg(attendees), COALESCE(sqlc.narg(recurrence_overrides),'[]'), COALESCE(sqlc.narg(reminders),'[]'))
RETURNING *;
-- name: PlatformUpdateEvent :one
-- scope: platform

UPDATE mailbox_events SET summary=sqlc.arg(summary), description=sqlc.arg(description), location=sqlc.arg(location),
 starts_at=sqlc.arg(starts_at), ends_at=sqlc.arg(ends_at), all_day=sqlc.arg(all_day),
 timezone=sqlc.arg(timezone), rrule=sqlc.arg(rrule), status=sqlc.arg(status), attendees=sqlc.arg(attendees),
 recurrence_overrides=COALESCE(sqlc.narg(recurrence_overrides), recurrence_overrides),
 reminders=COALESCE(sqlc.narg(reminders), reminders),
 sequence=sequence+1, version=version+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=sqlc.arg(tenant_id) AND mailbox_id=sqlc.arg(mailbox_id) AND id=sqlc.arg(id)
 AND version=sqlc.arg(expected_version) AND deleted_at IS NULL
RETURNING *;
-- name: PlatformDeleteEvent :one
-- scope: platform

UPDATE mailbox_events e
SET deleted_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), status='cancelled', version=e.version+1, sequence=e.sequence+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE e.tenant_id=?1 AND e.mailbox_id=?2 AND e.id=?3 AND e.version=?4 AND e.deleted_at IS NULL
RETURNING e.tenant_id, e.mailbox_id, e.calendar_id, e.id, e.uid, e.summary, e.description, e.location,
 e.starts_at, e.ends_at, e.all_day, e.timezone, e.rrule, e.status, e.attendees, e.recurrence_overrides,
 e.reminders, e.sequence, e.version, e.deleted_at, e.updated_at;
-- name: PlatformListChanges :many
-- scope: platform

SELECT * FROM mailbox_changes
WHERE tenant_id=?1 AND mailbox_id=?2 AND id > ?3
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- name: PlatformClaimMailboxJob :one
-- scope: platform

WITH candidate AS (
 SELECT id FROM mailbox_jobs
 WHERE status='pending' AND kind<>'calendar_reminder' AND (lease_until IS NULL OR lease_until <= (CAST(strftime('%s','now') AS INTEGER)*1000000))
 ORDER BY id
 LIMIT 1
)
UPDATE mailbox_jobs SET status='leased', attempts=attempts+1, claim_token=claim_token+1,
 lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000), updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE id=(SELECT id FROM candidate) AND status='pending'
RETURNING *;

-- name: PlatformInsertContactGroupMember :one
-- scope: platform

INSERT INTO mailbox_contact_group_members(tenant_id, mailbox_id, group_id, contact_id)
SELECT g.tenant_id, g.mailbox_id, g.id, c.id
FROM mailbox_contact_groups g
JOIN mailbox_contacts c ON c.tenant_id=g.tenant_id AND c.mailbox_id=g.mailbox_id AND c.addressbook_id=g.addressbook_id
WHERE g.tenant_id=?1 AND g.mailbox_id=?2 AND g.id=?3 AND c.id=?4 AND g.deleted_at IS NULL AND c.deleted_at IS NULL;
-- name: PlatformInsertContactGroup :one
-- scope: platform

INSERT INTO mailbox_contact_groups(tenant_id, mailbox_id, addressbook_id, name)
SELECT b.tenant_id, b.mailbox_id, b.id, ?4
FROM mailbox_addressbooks b
WHERE b.tenant_id=?1 AND b.mailbox_id=?2 AND b.id=?3 AND b.deleted_at IS NULL
RETURNING *;

-- name: PlatformDeleteContactGroup :one
-- scope: platform

SELECT urmail_delete_contact_group(?1,?2,?3);

-- name: PlatformListContactGroups :many
-- scope: platform

SELECT * FROM mailbox_contact_groups
WHERE tenant_id=?1 AND mailbox_id=?2 AND deleted_at IS NULL
ORDER BY lower(name), id;

-- name: PlatformGetEventByUID :one
-- scope: platform

SELECT * FROM mailbox_events
WHERE tenant_id=?1 AND mailbox_id=?2 AND uid=?3 AND deleted_at IS NULL;

-- name: PlatformGetEventAny :one
-- scope: platform

SELECT * FROM mailbox_events
WHERE tenant_id=?1 AND mailbox_id=?2 AND id=?3;

-- name: PlatformListCalendarSyncEvents :many
-- scope: platform

SELECT * FROM mailbox_events
WHERE tenant_id=?1 AND mailbox_id=?2 AND calendar_id=?3 AND deleted_at IS NULL
ORDER BY id LIMIT sqlc.arg(page_limit);

-- name: PlatformDeleteContactMembers :execrows
-- scope: platform

DELETE FROM mailbox_contact_group_members
WHERE tenant_id=?1 AND mailbox_id=?2 AND contact_id=?3;
