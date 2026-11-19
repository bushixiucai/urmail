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

-- scope: platform

INSERT INTO mailbox_contacts(tenant_id, mailbox_id, addressbook_id, display_name, given_name, family_name, emails, phones, note)
VALUES(sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(addressbook_id), sqlc.arg(display_name), sqlc.arg(given_name), sqlc.arg(family_name), sqlc.arg(emails), sqlc.arg(phones), sqlc.arg(note))
RETURNING *;
-- scope: platform

UPDATE mailbox_contacts c
SET display_name=sqlc.arg(display_name), given_name=sqlc.arg(given_name), family_name=sqlc.arg(family_name),
 emails=sqlc.arg(emails), phones=sqlc.arg(phones), note=sqlc.arg(note),
 version=c.version+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.mailbox_id=sqlc.arg(mailbox_id) AND c.id=sqlc.arg(id)
 AND c.version=sqlc.arg(expected_version) AND c.deleted_at IS NULL
RETURNING *;
-- scope: platform

-- 成员清理（原 removed CTE）拆为顺序查询 PlatformDeleteContactMembers，与本体同事务依序执行
UPDATE mailbox_contacts c
SET deleted_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), version=c.version+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE c.tenant_id=?1 AND c.mailbox_id=?2 AND c.id=?3 AND c.version=?4 AND c.deleted_at IS NULL
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

-- scope: platform

INSERT INTO mailbox_events(tenant_id, mailbox_id, calendar_id, uid, summary, description, location,
 starts_at, ends_at, all_day, timezone, rrule, status, attendees, recurrence_overrides, reminders)
VALUES(sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(calendar_id), sqlc.arg(uid),
 sqlc.arg(summary), sqlc.arg(description), sqlc.arg(location),
 sqlc.arg(starts_at), sqlc.arg(ends_at), sqlc.arg(all_day), sqlc.arg(timezone),
 sqlc.arg(rrule), sqlc.arg(status), sqlc.arg(attendees), COALESCE(sqlc.narg(recurrence_overrides),'[]'), COALESCE(sqlc.narg(reminders),'[]'))
RETURNING *;
-- scope: platform

UPDATE mailbox_events e
SET summary=sqlc.arg(summary), description=sqlc.arg(description), location=sqlc.arg(location),
 starts_at=sqlc.arg(starts_at), ends_at=sqlc.arg(ends_at), all_day=sqlc.arg(all_day),
 timezone=sqlc.arg(timezone), rrule=sqlc.arg(rrule), status=sqlc.arg(status), attendees=sqlc.arg(attendees),
 recurrence_overrides=COALESCE(sqlc.narg(recurrence_overrides), e.recurrence_overrides),
 reminders=COALESCE(sqlc.narg(reminders), e.reminders),
 sequence=e.sequence+1, version=e.version+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE e.tenant_id=sqlc.arg(tenant_id) AND e.mailbox_id=sqlc.arg(mailbox_id) AND e.id=sqlc.arg(id)
 AND e.version=sqlc.arg(expected_version) AND e.deleted_at IS NULL
RETURNING *;
-- name: PlatformDeleteEvent :one
-- scope: platform

WITH next AS (
 UPDATE mailbox_events AS e
 SET deleted_at=(CAST(strftime('%s','now') AS INTEGER)*1000000), status='cancelled', version=e.version+1, sequence=e.sequence+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
 WHERE e.tenant_id=?1 AND e.mailbox_id=?2 AND e.id=?3 AND e.version=?4 AND e.deleted_at IS NULL
 RETURNING *
), logged AS (
 INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
 SELECT next.tenant_id, next.mailbox_id, 'event', next.id, next.version, true FROM next
)
SELECT * FROM next;

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
UPDATE mailbox_jobs j
SET status='leased', attempts=j.attempts+1, claim_token=j.claim_token+1,
 lease_until=(CAST(strftime('%s','now') AS INTEGER)*1000000), updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE j.id=(SELECT id FROM candidate) AND j.status='pending'
RETURNING j.*;

-- name: PlatformInsertContactGroupMember :one
-- scope: platform

WITH inserted AS (
INSERT INTO mailbox_contact_group_members(tenant_id, mailbox_id, group_id, contact_id)
SELECT g.tenant_id, g.mailbox_id, g.id, c.id
FROM mailbox_contact_groups g
JOIN mailbox_contacts c ON c.tenant_id=g.tenant_id AND c.mailbox_id=g.mailbox_id AND c.addressbook_id=g.addressbook_id
WHERE g.tenant_id=?1 AND g.mailbox_id=?2 AND g.id=?3 AND c.id=?4 AND g.deleted_at IS NULL AND c.deleted_at IS NULL
RETURNING tenant_id
)
SELECT count(*) FROM inserted;

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
