package handlers

// SQLite runtime candidate slice 1 (V2 contract §5): mailbox auth/session +
// self settings backed by the single authoritative SQLite file opened via
// internal/sqlitedb. The adapters below implement the existing handler
// contracts (MailboxNativeAuth, MailboxNativeSessions,
// MailboxSessionLookup, MailboxSelfSettingsStore) directly on *sql.DB —
// they do NOT fake a pgx DBTX. No method outside the ten allow-candidate
// queries is reachable here: everything else keeps its PG path, and the
// runtime engine selector (api.Options) decides which store is wired.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/bushixiucai/urmail/internal/timeutil"
	"github.com/bushixiucai/urmail/internal/webmail"
)

const (
	// mailboxSessionTTLDefault is 30 minutes in micros; mailboxSessionTTLRemember
	// is 30 days. Same constants the PG adapter passes as pgtype.Interval.
	mailboxSessionTTLDefault  = int64(30 * 60 * 1_000_000)
	mailboxSessionTTLRemember = int64(30 * 24 * 60 * 60 * 1_000_000)
)

// SQLiteMailboxStore adapts the ten slice-1 queries to a *sql.DB connection
// opened by sqlitedb.Open (real file, WAL, per-connection PRAGMA contract,
// BEGIN IMMEDIATE transactions). One struct serves the login, session and
// self-settings contracts; zero-value detection keeps nil wiring honest.
type SQLiteMailboxStore struct {
	DB *sql.DB
}

// lookupResult mirrors PlatformMailboxAuthLookup's row.
type lookupResult struct {
	TenantID     int64
	MailboxID    int64
	Address      string
	PasswordHash string
}

const lookupSQL = `SELECT m.tenant_id, m.id AS mailbox_id, m.address, m.password_hash
FROM mailboxes m
JOIN tenants t ON t.id = m.tenant_id
JOIN domains d ON d.tenant_id = m.tenant_id AND d.id = m.domain_id
WHERE m.address = ?1
  AND m.status = 'active' AND t.status = 'active' AND d.status = 'active'`

// Lookup resolves an active mailbox by address (native login step 1).
func (s SQLiteMailboxStore) Lookup(ctx context.Context, address string) (int64, int64, string, string, error) {
	if s.DB == nil {
		return 0, 0, "", "", sql.ErrNoRows
	}
	var row lookupResult
	err := s.DB.QueryRowContext(ctx, lookupSQL, address).
		Scan(&row.TenantID, &row.MailboxID, &row.Address, &row.PasswordHash)
	if err != nil {
		return 0, 0, "", "", err
	}
	return row.TenantID, row.MailboxID, row.Address, row.PasswordHash, nil
}

const createVerifiedSessionSQL = `INSERT INTO mailbox_sessions(token_hash,tenant_id,mailbox_id,password_version,expires_at,origin_host)
SELECT ?1,m.tenant_id,m.id,md5(m.password_hash),(CAST(strftime('%s','now') AS INTEGER)*1000000)+?2,?3
FROM mailboxes m JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE m.address=?4 AND m.password_hash=?5
AND m.status='active' AND t.status='active' AND d.status='active'
RETURNING token_hash,tenant_id,mailbox_id,password_version,expires_at,revoked_at,created_at,origin_host`

// Create inserts a mailbox_sessions row; ErrNoRows-style mapping keeps the
// handler's existing 401 semantics when the verified insert finds no row.
func (s SQLiteMailboxStore) Create(ctx context.Context, tokenHash, address, expectedHash string, remember bool) (webmail.Session, error) {
	if s.DB == nil {
		return webmail.Session{}, sql.ErrNoRows
	}
	ttl := mailboxSessionTTLDefault
	if remember {
		ttl = mailboxSessionTTLRemember
	}
	var (
		outTokenHash    string
		tenantID        int64
		mailboxID       int64
		passwordVersion string
		expiresAt       int64
		revokedAt       sql.NullInt64
		createdAt       int64
		originHost      string
	)
	err := s.DB.QueryRowContext(ctx, createVerifiedSessionSQL,
		tokenHash, ttl, webmail.OriginHost(ctx), address, expectedHash,
	).Scan(&outTokenHash, &tenantID, &mailboxID, &passwordVersion, &expiresAt, &revokedAt, &createdAt, &originHost)
	if err != nil {
		return webmail.Session{}, err
	}
	return webmail.Session{
		OriginHost:      originHost,
		TokenHash:       outTokenHash,
		TenantID:        tenantID,
		MailboxID:       mailboxID,
		PasswordVersion: passwordVersion,
		Address:         address,
		ExpiresAt:       timeutil.FromMicro(expiresAt),
	}, nil
}

const getMailboxSessionSQL = `SELECT s.token_hash,s.tenant_id,s.mailbox_id,s.expires_at,m.address,s.password_version,s.origin_host FROM mailbox_sessions s
JOIN mailboxes m ON m.tenant_id=s.tenant_id AND m.id=s.mailbox_id
JOIN tenants t ON t.id=m.tenant_id JOIN domains d ON d.tenant_id=m.tenant_id AND d.id=m.domain_id
WHERE s.token_hash=?1 AND s.revoked_at IS NULL AND s.expires_at>(CAST(strftime('%s','now') AS INTEGER)*1000000)
AND s.password_version=md5(m.password_hash) AND m.status='active' AND t.status='active' AND d.status='active'`

// Get loads a still-valid mailbox session (bearer middleware lookup).
func (s SQLiteMailboxStore) Get(ctx context.Context, tokenHash string) (webmail.Session, error) {
	if s.DB == nil {
		return webmail.Session{}, sql.ErrNoRows
	}
	var (
		outTokenHash    string
		tenantID        int64
		mailboxID       int64
		expiresAt       int64
		address         string
		passwordVersion string
		originHost      string
	)
	err := s.DB.QueryRowContext(ctx, getMailboxSessionSQL, tokenHash).
		Scan(&outTokenHash, &tenantID, &mailboxID, &expiresAt, &address, &passwordVersion, &originHost)
	if err != nil {
		return webmail.Session{}, err
	}
	return webmail.Session{
		OriginHost:      originHost,
		TokenHash:       outTokenHash,
		TenantID:        tenantID,
		MailboxID:       mailboxID,
		PasswordVersion: passwordVersion,
		Address:         address,
		ExpiresAt:       timeutil.FromMicro(expiresAt),
	}, nil
}

// GetMailboxSession satisfies middleware.MailboxSessionLookup.
func (s SQLiteMailboxStore) GetMailboxSession(ctx context.Context, tokenHash string) (webmail.Session, error) {
	return s.Get(ctx, tokenHash)
}

const revokeMailboxSessionSQL = `UPDATE mailbox_sessions SET revoked_at=COALESCE(revoked_at,(CAST(strftime('%s','now') AS INTEGER)*1000000)) WHERE token_hash=?1`

// Revoke marks the session revoked (idempotent, same COALESCE as PG).
func (s SQLiteMailboxStore) Revoke(ctx context.Context, tokenHash string) error {
	if s.DB == nil {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, revokeMailboxSessionSQL, tokenHash)
	return err
}

// --- self settings ---

const getMailboxSettingsSQL = `SELECT display_name,signature_text,signature_html,preferences,identity_profiles,version,updated_at
FROM mailbox_settings WHERE tenant_id=?1 AND mailbox_id=?2`

// Settings returns the stored snapshot, or an empty version-0 snapshot when
// the mailbox has never saved settings.
func (s SQLiteMailboxStore) Settings(ctx context.Context, tenantID, mailboxID int64) (MailboxSettings, error) {
	if s.DB == nil {
		return MailboxSettings{}, sql.ErrNoRows
	}
	var (
		displayName      string
		signatureText    string
		signatureHTML    string
		preferences      []byte
		identityProfiles []byte
		version          int32
		updatedAt        int64
	)
	err := s.DB.QueryRowContext(ctx, getMailboxSettingsSQL, tenantID, mailboxID).
		Scan(&displayName, &signatureText, &signatureHTML, &preferences, &identityProfiles, &version, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MailboxSettings{Preferences: json.RawMessage(`{}`)}, nil
	}
	if err != nil {
		return MailboxSettings{}, err
	}
	return MailboxSettings{
		DisplayName:      displayName,
		SignatureText:    signatureText,
		SignatureHTML:    signatureHTML,
		Preferences:      json.RawMessage(preferences),
		IdentityProfiles: json.RawMessage(identityProfiles),
		Version:          int64(version),
		UpdatedAt:        timeutil.FromMicro(updatedAt),
	}, nil
}

const updateMailboxSettingsSQL = `UPDATE mailbox_settings
SET display_name=?1,
    signature_text=?2,
    signature_html=?3,
    preferences=?4,
    identity_profiles=COALESCE(?5,identity_profiles),
    version=version+1,
    updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000)
WHERE tenant_id=?6 AND mailbox_id=?7
  AND version=?8`

const insertMailboxSettingsSQL = `INSERT INTO mailbox_settings(tenant_id,mailbox_id,display_name,signature_text,signature_html,preferences,identity_profiles)
VALUES(?1,?2,?3,?4,?5,?6,COALESCE(?7,'{}'))
ON CONFLICT(mailbox_id) DO NOTHING`

// SaveSettings applies the optimistic-concurrency write inside one
// BEGIN IMMEDIATE transaction: update first, insert only when no row exists.
func (s SQLiteMailboxStore) SaveSettings(ctx context.Context, save MailboxSettingsSave) (int64, error) {
	if s.DB == nil {
		return 0, sql.ErrNoRows
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, updateMailboxSettingsSQL,
		save.DisplayName, save.SignatureText, save.SignatureHTML, save.Preferences, nullIfEmpty(save.IdentityProfiles),
		save.TenantID, save.MailboxID, save.ExpectedVersion,
	)
	if err != nil {
		return 0, err
	}
	if updated, updErr := res.RowsAffected(); updErr == nil && updated == 1 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return save.ExpectedVersion + 1, nil
	}
	var existing int64
	getErr := tx.QueryRowContext(ctx, `SELECT version FROM mailbox_settings WHERE tenant_id=?1 AND mailbox_id=?2`, save.TenantID, save.MailboxID).Scan(&existing)
	if errors.Is(getErr, sql.ErrNoRows) {
		if save.ExpectedVersion != 0 {
			return 0, ErrSettingsConflict
		}
		res, insErr := tx.ExecContext(ctx, insertMailboxSettingsSQL,
			save.TenantID, save.MailboxID, save.DisplayName, save.SignatureText, save.SignatureHTML, save.Preferences, nullIfEmpty(save.IdentityProfiles))
		if insErr != nil {
			return 0, insErr
		}
		inserted, insErr := res.RowsAffected()
		if insErr != nil {
			return 0, insErr
		}
		if inserted != 1 {
			return 0, ErrSettingsConflict
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 1, nil
	}
	if getErr != nil {
		return 0, getErr
	}
	return 0, ErrSettingsConflict
}

const getMailboxQuotaSQL = `SELECT m.bytes_used,m.quota_bytes FROM mailboxes m
WHERE m.tenant_id=?1 AND m.id=?2 AND m.status='active'`

// Quota returns server-authoritative usage for the mailbox.
func (s SQLiteMailboxStore) Quota(ctx context.Context, tenantID, mailboxID int64) (int64, int64, error) {
	if s.DB == nil {
		return 0, 0, sql.ErrNoRows
	}
	var used, total int64
	if err := s.DB.QueryRowContext(ctx, getMailboxQuotaSQL, tenantID, mailboxID).Scan(&used, &total); err != nil {
		return 0, 0, err
	}
	return used, total, nil
}

const listMailboxSenderIdentitiesSQL = `SELECT DISTINCT a.local_part,d.fqdn FROM mailbox_aliases a
JOIN domains d ON d.id=a.domain_id AND d.tenant_id=a.tenant_id
WHERE a.tenant_id=?1 AND a.active
  AND d.status='active' AND d.provisioning_state='ready'
  AND EXISTS(SELECT 1 FROM json_each(a.sender_logins) je WHERE je.value = ?2)
ORDER BY a.local_part,d.fqdn`

// SenderIdentities lists the alias addresses this mailbox may send From.
func (s SQLiteMailboxStore) SenderIdentities(ctx context.Context, tenantID int64, sender string) ([]string, error) {
	if s.DB == nil {
		return nil, sql.ErrNoRows
	}
	rows, err := s.DB.QueryContext(ctx, listMailboxSenderIdentitiesSQL, tenantID, sender)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var localPart, fqdn string
		if err := rows.Scan(&localPart, &fqdn); err != nil {
			return nil, err
		}
		out = append(out, localPart+"@"+fqdn)
	}
	return out, rows.Err()
}

const senderIdentityAllowedSQL = `SELECT 1 FROM mailbox_aliases a
JOIN domains d ON d.id=a.domain_id AND d.tenant_id=a.tenant_id
WHERE a.tenant_id=?1 AND a.active
  AND d.status='active' AND d.provisioning_state='ready'
  AND EXISTS(SELECT 1 FROM json_each(a.sender_logins) je WHERE je.value = ?2)
  AND ?3=a.local_part||'@'||d.fqdn
LIMIT 1`

// SenderIdentityAllowed rechecks one From candidate against the same grants.
func (s SQLiteMailboxStore) SenderIdentityAllowed(ctx context.Context, tenantID int64, sender, from string) (bool, error) {
	if s.DB == nil {
		return false, sql.ErrNoRows
	}
	var one int64
	err := s.DB.QueryRowContext(ctx, senderIdentityAllowedSQL, tenantID, sender, from).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return one == 1, nil
}

// nullIfEmpty keeps identity_profiles NULL when the caller omitted it, so the
// COALESCE defaults apply exactly like the PG adapter's narg.
func nullIfEmpty(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
