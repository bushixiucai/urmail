package handlers

import (
	"context"
	"database/sql"
	"strings"

	"github.com/bushixiucai/urmail/internal/store"
)

// SQLiteCorporateDirectoryStore serves the mailbox corporate directory read
// surface from the SQLite slice database (b6 structural unlock). Every query
// binds the tenant scope explicitly to its argument — the session's TenantID
// at the handler layer — so isolation holds at the application layer without
// RLS (architecture ruling ①). The SQL mirrors the PG engine statements
// (platform_parity_enhancements.sql.go) with the dialect differences: ?
// placeholders, LOWER(...)=... instead of ILIKE, integer 1 for boolean true.
type SQLiteCorporateDirectoryStore struct {
	DB *sql.DB
}

// PlatformGetTenantDirectorySettings returns the tenant's directory toggle.
func (s *SQLiteCorporateDirectoryStore) PlatformGetTenantDirectorySettings(ctx context.Context, tenantID int64) (store.TenantDirectorySettings, error) {
	const q = `SELECT tenant_id, enabled, updated_at FROM tenant_directory_settings
WHERE tenant_id = ?1`
	var row store.TenantDirectorySettings
	err := s.DB.QueryRowContext(ctx, q, tenantID).Scan(&row.TenantID, &row.Enabled, &row.UpdatedAt)
	return row, err
}

// PlatformListPublishedCorporateContacts lists the tenant's published
// contacts, only while the directory is enabled.
func (s *SQLiteCorporateDirectoryStore) PlatformListPublishedCorporateContacts(ctx context.Context, tenantID int64) ([]store.TenantDirectoryContacts, error) {
	const q = `SELECT c.id, c.tenant_id, c.display_name, c.given_name, c.family_name, c.organization, c.department, c.job_title, c.emails, c.phones, c.addresses, c.note, c.created_at, c.updated_at FROM tenant_directory_contacts c
JOIN tenant_directory_settings s ON s.tenant_id = c.tenant_id
WHERE c.tenant_id = ?1 AND s.enabled = 1
ORDER BY c.display_name ASC`
	return s.scanContacts(s.DB.QueryContext(ctx, q, tenantID))
}

// PlatformSearchPublishedCorporateContacts substring-searches display name,
// emails, department and job title (case-insensitive, %-/ _-escaped,
// bounded like the PG statement).
func (s *SQLiteCorporateDirectoryStore) PlatformSearchPublishedCorporateContacts(ctx context.Context, arg store.PlatformSearchPublishedCorporateContactsParams) ([]store.TenantDirectoryContacts, error) {
	pattern := "%" + escapeDirectoryLike(arg.Query) + "%"
	const q = `SELECT c.id, c.tenant_id, c.display_name, c.given_name, c.family_name, c.organization, c.department, c.job_title, c.emails, c.phones, c.addresses, c.note, c.created_at, c.updated_at FROM tenant_directory_contacts c
JOIN tenant_directory_settings s ON s.tenant_id = c.tenant_id
WHERE c.tenant_id = ?1 AND s.enabled = 1
  AND (
    LOWER(c.display_name) LIKE ?2 ESCAPE '\'
    OR LOWER(c.emails) LIKE ?2 ESCAPE '\'
    OR LOWER(c.department) LIKE ?2 ESCAPE '\'
    OR LOWER(c.job_title) LIKE ?2 ESCAPE '\'
  )
ORDER BY c.display_name ASC
LIMIT 20`
	return s.scanContacts(s.DB.QueryContext(ctx, q, arg.TenantID, pattern))
}

func (s *SQLiteCorporateDirectoryStore) scanContacts(rows *sql.Rows, err error) ([]store.TenantDirectoryContacts, error) {
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []store.TenantDirectoryContacts{}
	for rows.Next() {
		var i store.TenantDirectoryContacts
		if err := rows.Scan(
			&i.ID,
			&i.TenantID,
			&i.DisplayName,
			&i.GivenName,
			&i.FamilyName,
			&i.Organization,
			&i.Department,
			&i.JobTitle,
			&i.Emails,
			&i.Phones,
			&i.Addresses,
			&i.Note,
			&i.CreatedAt,
			&i.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// escapeDirectoryLike escapes the SQL LIKE wildcards so a search term matches
// literally, mirroring the PG statement's replace chain (backslash escape).
func escapeDirectoryLike(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(strings.ToLower(term))
}
