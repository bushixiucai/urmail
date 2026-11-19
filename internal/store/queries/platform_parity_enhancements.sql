-- WEBMAIL-PARITY-001: SQL queries for snippets, corporate directory, enhanced contacts

-- name: PlatformListMailboxSnippets :many
-- scope: platform

SELECT * FROM mailbox_snippets
WHERE tenant_id = ?1 AND mailbox_id = ?2
ORDER BY title ASC;

-- name: PlatformGetMailboxSnippet :one
-- scope: platform

SELECT * FROM mailbox_snippets
WHERE tenant_id = ?1 AND mailbox_id = ?2 AND id = ?3;

-- name: PlatformInsertMailboxSnippet :one
-- scope: platform

INSERT INTO mailbox_snippets(tenant_id, mailbox_id, title, content, shortcut)
VALUES(?1, ?2, ?3, ?4, ?5)
RETURNING *;

-- name: PlatformUpdateMailboxSnippet :one
-- scope: platform

UPDATE mailbox_snippets
SET title = ?4, content = ?5, shortcut = ?6, updated_at = (CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id = ?1 AND mailbox_id = ?2 AND id = ?3
RETURNING *;

-- name: PlatformDeleteMailboxSnippet :execrows
-- scope: platform

DELETE FROM mailbox_snippets
WHERE tenant_id = ?1 AND mailbox_id = ?2 AND id = ?3;

-- name: PlatformGetTenantDirectorySettings :one
-- scope: platform

SELECT * FROM tenant_directory_settings
WHERE tenant_id = ?1;

-- name: PlatformSetTenantDirectorySettings :one
-- scope: platform

INSERT INTO tenant_directory_settings(tenant_id, enabled, updated_at)
VALUES(?1, ?2, (CAST(strftime('%s','now') AS INTEGER)*1000000))
ON CONFLICT (tenant_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = (CAST(strftime('%s','now') AS INTEGER)*1000000)
RETURNING *;

-- name: PlatformListTenantDirectoryContacts :many
-- scope: platform

SELECT * FROM tenant_directory_contacts
WHERE tenant_id = ?1
ORDER BY display_name ASC;

-- name: PlatformListPublishedCorporateContacts :many
-- scope: platform

SELECT c.* FROM tenant_directory_contacts c
JOIN tenant_directory_settings s ON s.tenant_id = c.tenant_id
WHERE c.tenant_id = ?1 AND s.enabled = true
ORDER BY c.display_name ASC;

-- name: PlatformSearchPublishedCorporateContacts :many
-- scope: platform

SELECT c.* FROM tenant_directory_contacts c
JOIN tenant_directory_settings s ON s.tenant_id = c.tenant_id
WHERE c.tenant_id = ?1 AND s.enabled = true
  AND (
    c.display_name LIKE '%' || replace(replace(replace(sqlc.arg(query), '', '\'), '%', '%'), '_', '_') || '%' ESCAPE ''
    OR c.emails LIKE '%' || replace(replace(replace(sqlc.arg(query), '', '\'), '%', '%'), '_', '_') || '%' ESCAPE ''
    OR c.department LIKE '%' || replace(replace(replace(sqlc.arg(query), '', '\'), '%', '%'), '_', '_') || '%' ESCAPE ''
    OR c.job_title LIKE '%' || replace(replace(replace(sqlc.arg(query), '', '\'), '%', '%'), '_', '_') || '%' ESCAPE ''
  )
ORDER BY c.display_name ASC
LIMIT 20;

-- name: PlatformInsertTenantDirectoryContact :one
-- scope: platform

INSERT INTO tenant_directory_contacts(
  tenant_id, display_name, given_name, family_name,
  organization, department, job_title, emails, phones, addresses, note
) VALUES(
  ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11
) RETURNING *;

-- name: PlatformUpdateTenantDirectoryContact :one
-- scope: platform

UPDATE tenant_directory_contacts
SET display_name = ?3, given_name = ?4, family_name = ?5,
    organization = ?6, department = ?7, job_title = ?8,
    emails = ?9, phones = ?10, addresses = ?11, note = ?12, updated_at = (CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id = ?1 AND id = ?2
RETURNING *;

-- name: PlatformDeleteTenantDirectoryContact :execrows
-- scope: platform

DELETE FROM tenant_directory_contacts
WHERE tenant_id = ?1 AND id = ?2;

-- scope: platform

INSERT INTO mailbox_contacts(tenant_id, mailbox_id, addressbook_id, display_name, given_name, family_name,
 emails, phones, note, organization, job_title, addresses, birthday, photo, vcard_extra)
VALUES(sqlc.arg(tenant_id), sqlc.arg(mailbox_id), sqlc.arg(addressbook_id), sqlc.arg(display_name),
 sqlc.arg(given_name), sqlc.arg(family_name), sqlc.arg(emails), sqlc.arg(phones), sqlc.arg(note),
 sqlc.arg(organization), sqlc.arg(job_title), sqlc.arg(addresses), sqlc.arg(birthday), sqlc.arg(photo), sqlc.arg(vcard_extra))
RETURNING *;
-- scope: platform

UPDATE mailbox_contacts c
SET display_name=sqlc.arg(display_name), given_name=sqlc.arg(given_name), family_name=sqlc.arg(family_name),
 emails=sqlc.arg(emails), phones=sqlc.arg(phones), note=sqlc.arg(note),
 organization=sqlc.arg(organization), job_title=sqlc.arg(job_title), addresses=sqlc.arg(addresses),
 birthday=sqlc.arg(birthday), photo=sqlc.arg(photo), vcard_extra=sqlc.arg(vcard_extra),
 version=c.version+1, updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.mailbox_id=sqlc.arg(mailbox_id) AND c.id=sqlc.arg(id)
 AND c.version=sqlc.arg(expected_version) AND c.deleted_at IS NULL
RETURNING *;
