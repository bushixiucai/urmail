-- Mailbox-owned contacts and calendars. Every read and write names both
-- tenant_id and mailbox_id. Message bodies are not stored here.

-- name: PlatformEnsureAddressbook :one
INSERT INTO mailbox_addressbooks(tenant_id, mailbox_id, name)
VALUES(sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(name))
ON CONFLICT (tenant_id, mailbox_id, lower(name)) WHERE deleted_at IS NULL DO NOTHING
RETURNING *;

-- name: PlatformGetAddressbookByName :one
SELECT * FROM mailbox_addressbooks
WHERE tenant_id=$1 AND mailbox_id=$2 AND name=$3 AND deleted_at IS NULL;

-- name: PlatformListContacts :many
SELECT * FROM mailbox_contacts
WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL
ORDER BY lower(display_name), id
LIMIT sqlc.arg(page_limit);

-- name: PlatformGetContact :one
SELECT * FROM mailbox_contacts
WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND deleted_at IS NULL;

-- name: PlatformInsertContact :one
WITH next AS (
 INSERT INTO mailbox_contacts(
  tenant_id, mailbox_id, addressbook_id, display_name, given_name, family_name, emails, phones, note
 ) VALUES(
  sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(addressbook_id), sqlc.arg(display_name),
  sqlc.arg(given_name), sqlc.arg(family_name), sqlc.arg(emails), sqlc.arg(phones), sqlc.arg(note)
 ) RETURNING *
), logged AS (
 INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
 SELECT next.tenant_id, next.mailbox_id, 'contact', next.id, next.version, false FROM next
)
SELECT * FROM next;

-- name: PlatformUpdateContact :one
WITH next AS (
 UPDATE mailbox_contacts AS c
 SET display_name=sqlc.arg(display_name), given_name=sqlc.arg(given_name), family_name=sqlc.arg(family_name),
  emails=sqlc.arg(emails), phones=sqlc.arg(phones), note=sqlc.arg(note),
  version=c.version+1, updated_at=now()
 WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.mailbox_id=sqlc.arg(mailbox_id) AND c.id=sqlc.arg(id)
  AND c.version=sqlc.arg(expected_version) AND c.deleted_at IS NULL
 RETURNING *
), logged AS (
 INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
 SELECT next.tenant_id, next.mailbox_id, 'contact', next.id, next.version, false FROM next
)
SELECT * FROM next;

-- name: PlatformDeleteContact :one
WITH next AS (
 UPDATE mailbox_contacts AS c
 SET deleted_at=now(), version=c.version+1, updated_at=now()
 WHERE c.tenant_id=$1 AND c.mailbox_id=$2 AND c.id=$3 AND c.version=$4 AND c.deleted_at IS NULL
 RETURNING *
), logged AS (
 INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
 SELECT next.tenant_id, next.mailbox_id, 'contact', next.id, next.version, true FROM next
), removed AS (
 DELETE FROM mailbox_contact_group_members m USING next
 WHERE m.tenant_id=next.tenant_id AND m.mailbox_id=next.mailbox_id AND m.contact_id=next.id
)
SELECT * FROM next;

-- name: PlatformSearchContacts :many
SELECT * FROM mailbox_contacts
WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL
 AND length(sqlc.arg(query)::text) BETWEEN 1 AND 100
 AND (
  display_name ILIKE '%' || replace(replace(replace(sqlc.arg(query)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
  OR emails::text ILIKE '%' || replace(replace(replace(sqlc.arg(query)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' ESCAPE '\'
 )
ORDER BY lower(display_name), id
LIMIT 20;

-- name: PlatformEnsureCalendar :one
INSERT INTO mailbox_calendars(tenant_id, mailbox_id, name, timezone)
VALUES($1, $2, $3, $4)
ON CONFLICT (tenant_id, mailbox_id, lower(name)) WHERE deleted_at IS NULL DO NOTHING
RETURNING *;

-- name: PlatformGetCalendarByName :one
SELECT * FROM mailbox_calendars
WHERE tenant_id=$1 AND mailbox_id=$2 AND name=$3 AND deleted_at IS NULL;

-- name: PlatformListEvents :many
SELECT * FROM mailbox_events
WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL
 AND (rrule<>'' OR (starts_at<sqlc.arg(range_end) AND ends_at>sqlc.arg(range_start)))
ORDER BY starts_at, id
LIMIT sqlc.arg(page_limit);

-- name: PlatformGetEvent :one
SELECT * FROM mailbox_events
WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 AND deleted_at IS NULL;

-- name: PlatformInsertEvent :one
WITH next AS (
 INSERT INTO mailbox_events(
  tenant_id, mailbox_id, calendar_id, uid, summary, description, location,
  starts_at, ends_at, all_day, timezone, rrule, status, attendees, recurrence_overrides,reminders
 ) VALUES(
  sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(calendar_id), sqlc.arg(uid),
  sqlc.arg(summary), sqlc.arg(description), sqlc.arg(location),
  sqlc.arg(starts_at), sqlc.arg(ends_at), sqlc.arg(all_day), sqlc.arg(timezone),
  sqlc.arg(rrule), sqlc.arg(status), sqlc.arg(attendees), COALESCE(sqlc.narg(recurrence_overrides)::jsonb,'[]'::jsonb),COALESCE(sqlc.narg(reminders)::jsonb,'[]'::jsonb)
 ) RETURNING *
), logged AS (
 INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
 SELECT next.tenant_id, next.mailbox_id, 'event', next.id, next.version, false FROM next
)
SELECT * FROM next;

-- name: PlatformUpdateEvent :one
WITH next AS (
 UPDATE mailbox_events AS e
 SET summary=sqlc.arg(summary), description=sqlc.arg(description), location=sqlc.arg(location),
  starts_at=sqlc.arg(starts_at), ends_at=sqlc.arg(ends_at), all_day=sqlc.arg(all_day),
  timezone=sqlc.arg(timezone), rrule=sqlc.arg(rrule), status=sqlc.arg(status), attendees=sqlc.arg(attendees),
  recurrence_overrides=COALESCE(sqlc.narg(recurrence_overrides)::jsonb,e.recurrence_overrides),
 reminders=COALESCE(sqlc.narg(reminders)::jsonb,e.reminders),
  sequence=e.sequence+1, version=e.version+1, updated_at=now()
 WHERE e.tenant_id=sqlc.arg(tenant_id) AND e.mailbox_id=sqlc.arg(mailbox_id) AND e.id=sqlc.arg(id)
  AND e.version=sqlc.arg(expected_version) AND e.deleted_at IS NULL
 RETURNING *
), logged AS (
 INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
 SELECT next.tenant_id, next.mailbox_id, 'event', next.id, next.version, false FROM next
)
SELECT * FROM next;

-- name: PlatformDeleteEvent :one
WITH next AS (
 UPDATE mailbox_events AS e
 SET deleted_at=now(), status='cancelled', version=e.version+1, sequence=e.sequence+1, updated_at=now()
 WHERE e.tenant_id=$1 AND e.mailbox_id=$2 AND e.id=$3 AND e.version=$4 AND e.deleted_at IS NULL
 RETURNING *
), logged AS (
 INSERT INTO mailbox_changes(tenant_id, mailbox_id, resource_type, resource_id, version, deleted)
 SELECT next.tenant_id, next.mailbox_id, 'event', next.id, next.version, true FROM next
)
SELECT * FROM next;

-- name: PlatformListChanges :many
SELECT * FROM mailbox_changes
WHERE tenant_id=$1 AND mailbox_id=$2 AND id > $3
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- name: PlatformClaimMailboxJob :one
WITH candidate AS (
 SELECT id FROM mailbox_jobs
 WHERE status='pending' AND kind<>'calendar_reminder' AND (lease_until IS NULL OR lease_until <= now())
 ORDER BY id
 FOR UPDATE SKIP LOCKED
 LIMIT 1
)
UPDATE mailbox_jobs j
SET status='leased', attempts=j.attempts+1, claim_token=j.claim_token+1,
 lease_until=now()+interval '2 minutes', updated_at=now()
FROM candidate
WHERE j.id=candidate.id
RETURNING j.*;

-- name: PlatformInsertContactGroupMember :one
WITH inserted AS (
INSERT INTO mailbox_contact_group_members(tenant_id, mailbox_id, group_id, contact_id)
SELECT g.tenant_id, g.mailbox_id, g.id, c.id
FROM mailbox_contact_groups g
JOIN mailbox_contacts c ON c.tenant_id=g.tenant_id AND c.mailbox_id=g.mailbox_id AND c.addressbook_id=g.addressbook_id
WHERE g.tenant_id=$1 AND g.mailbox_id=$2 AND g.id=$3 AND c.id=$4 AND g.deleted_at IS NULL AND c.deleted_at IS NULL
RETURNING tenant_id
)
SELECT count(*) FROM inserted;

-- name: PlatformInsertContactGroup :one
INSERT INTO mailbox_contact_groups(tenant_id, mailbox_id, addressbook_id, name)
SELECT b.tenant_id, b.mailbox_id, b.id, $4
FROM mailbox_addressbooks b
WHERE b.tenant_id=$1 AND b.mailbox_id=$2 AND b.id=$3 AND b.deleted_at IS NULL
RETURNING *;

-- name: PlatformDeleteContactGroup :one
SELECT urmail_delete_contact_group($1,$2,$3);

-- name: PlatformListContactGroups :many
SELECT * FROM mailbox_contact_groups
WHERE tenant_id=$1 AND mailbox_id=$2 AND deleted_at IS NULL
ORDER BY lower(name), id;

-- name: PlatformGetEventByUID :one
SELECT * FROM mailbox_events
WHERE tenant_id=$1 AND mailbox_id=$2 AND uid=$3 AND deleted_at IS NULL;

-- name: PlatformGetEventAny :one
SELECT * FROM mailbox_events
WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3;

-- name: PlatformListCalendarSyncEvents :many
SELECT * FROM mailbox_events
WHERE tenant_id=$1 AND mailbox_id=$2 AND calendar_id=$3 AND deleted_at IS NULL
ORDER BY id LIMIT sqlc.arg(page_limit);
