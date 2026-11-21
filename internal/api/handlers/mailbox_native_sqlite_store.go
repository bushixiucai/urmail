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
	"sort"
	"strings"

	"github.com/bushixiucai/urmail/internal/timeutil"
	"github.com/bushixiucai/urmail/internal/webmail"
)

var (
	// ErrMailboxNotFound reports that the session's (tenant_id, mailbox_id) is
	// missing or no longer active in SQLite.
	ErrMailboxNotFound = errors.New("mailbox not found")
	// ErrFolderNotFound reports that the requested mailbox folder does not exist.
	ErrFolderNotFound = errors.New("mailbox folder not found")
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

// --- brand host boundary (slice 1) ---
//
// The SQLite schema carries the full brand_hosts lifecycle (status,
// verified/cert/activated timestamps), so the boundary semantics are the
// same statements the PG adapter runs; only the binding differs.

const brandHostLifecycleSQL = `SELECT hostname, brand_id, status, failure_reason, verified_at, cert_issued_at, activated_at, created_at, updated_at FROM brand_hosts WHERE hostname = ?1`

// BrandHostLifecycle reports found=false for hosts that are not brand hosts.
func (s SQLiteMailboxStore) BrandHostLifecycle(ctx context.Context, host string) (int64, string, bool, error) {
	if s.DB == nil {
		return 0, "", false, sql.ErrNoRows
	}
	var (
		hostname      string
		brandID       int64
		status        string
		failureReason sql.NullString
		verifiedAt    sql.NullInt64
		certIssuedAt  sql.NullInt64
		activatedAt   sql.NullInt64
		createdAt     int64
		updatedAt     int64
	)
	err := s.DB.QueryRowContext(ctx, brandHostLifecycleSQL, host).
		Scan(&hostname, &brandID, &status, &failureReason, &verifiedAt, &certIssuedAt, &activatedAt, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return brandID, status, true, nil
}

const brandHostCertEligibleSQL = `SELECT EXISTS(
  SELECT 1 FROM brand_hosts h JOIN brands b ON b.id=h.brand_id AND b.status='active'
  WHERE h.hostname = ?1 AND h.status IN ('verified','cert-issued','active')
)`

// BrandHostCertEligible mirrors PlatformBrandHostCertEligible.
func (s SQLiteMailboxStore) BrandHostCertEligible(ctx context.Context, host string) (bool, error) {
	if s.DB == nil {
		return false, sql.ErrNoRows
	}
	var eligible int64
	if err := s.DB.QueryRowContext(ctx, brandHostCertEligibleSQL, host).Scan(&eligible); err != nil {
		return false, err
	}
	return eligible == 1, nil
}

const tenantBrandMatchesSQL = `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=?1 AND brand_id=?2 AND status IN ('active','pending'))`

// BrandTenantMatches carries the brand boundary through session creation
// and bearer authentication; errors fail closed.
func (s SQLiteMailboxStore) BrandTenantMatches(ctx context.Context, tenant, brand int64) bool {
	if s.DB == nil {
		return false
	}
	var matches int64
	if err := s.DB.QueryRowContext(ctx, tenantBrandMatchesSQL, tenant, brand).Scan(&matches); err != nil {
		return false
	}
	return matches == 1
}

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

// --- read-only webmail folders & messages (SQLite slice-1/2 inbox unlock) ---

const verifyActiveMailboxSQL = `SELECT m.id
FROM mailboxes m
JOIN tenants t ON t.id = m.tenant_id
JOIN domains d ON d.tenant_id = m.tenant_id AND d.id = m.domain_id
WHERE m.tenant_id = ?1 AND m.id = ?2
  AND m.status = 'active' AND t.status = 'active' AND d.status = 'active'`

func (s SQLiteMailboxStore) verifyActiveMailbox(ctx context.Context, tenantID, mailboxID int64) error {
	if s.DB == nil {
		return sql.ErrNoRows
	}
	if tenantID <= 0 || mailboxID <= 0 {
		return ErrMailboxNotFound
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, verifyActiveMailboxSQL, tenantID, mailboxID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMailboxNotFound
	}
	return err
}

func (s SQLiteMailboxStore) sqliteTableExists(ctx context.Context, name string) (bool, error) {
	var found int
	err := s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?1 LIMIT 1`, name,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return found == 1, nil
}

func defaultSQLiteFolders() []webmail.Folder {
	specs := []struct {
		name  string
		flags []string
	}{
		{name: "INBOX", flags: []string{`\HasNoChildren`}},
		{name: "Sent", flags: []string{`\HasNoChildren`, `\Sent`}},
		{name: "Drafts", flags: []string{`\HasNoChildren`, `\Drafts`}},
		{name: "Trash", flags: []string{`\HasNoChildren`, `\Trash`}},
		{name: "Junk", flags: []string{`\HasNoChildren`, `\Junk`}},
	}
	out := make([]webmail.Folder, 0, len(specs))
	for _, spec := range specs {
		out = append(out, webmail.Folder{
			Name:      spec.name,
			NameEnc:   webmail.EncodeFolderName(spec.name),
			Delimiter: "/",
			Flags:     spec.flags,
		})
	}
	return out
}

// ListFolders returns folders scoped strictly to (tenantID, mailboxID). When no
// optional mailbox_folders rows exist for an active empty mailbox, it returns
// the standard default IMAP folder set so the webmail inbox renders cleanly.
func (s SQLiteMailboxStore) ListFolders(ctx context.Context, tenantID, mailboxID int64) ([]webmail.Folder, error) {
	if err := s.verifyActiveMailbox(ctx, tenantID, mailboxID); err != nil {
		return nil, err
	}
	hasTable, err := s.sqliteTableExists(ctx, "mailbox_folders")
	if err != nil {
		return nil, err
	}
	if hasTable {
		rows, err := s.DB.QueryContext(ctx,
			`SELECT name, delimiter, flags_json FROM mailbox_folders WHERE tenant_id = ?1 AND mailbox_id = ?2 ORDER BY id ASC`,
			tenantID, mailboxID,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := make([]webmail.Folder, 0)
		for rows.Next() {
			var name, delim, flagsJSON string
			if err := rows.Scan(&name, &delim, &flagsJSON); err != nil {
				return nil, err
			}
			if delim == "" {
				delim = "/"
			}
			var flags []string
			if flagsJSON != "" {
				_ = json.Unmarshal([]byte(flagsJSON), &flags)
			}
			if flags == nil {
				flags = []string{}
			}
			out = append(out, webmail.Folder{
				Name:      name,
				NameEnc:   webmail.EncodeFolderName(name),
				Delimiter: delim,
				Flags:     flags,
			})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return defaultSQLiteFolders(), nil
}

// ListMessages returns read-only message summaries for one folder scoped
// strictly to (tenantID, mailboxID). When no mailbox_messages table exists,
// it returns an empty page (200 OK) for any valid folder of the active mailbox.
func (s SQLiteMailboxStore) ListMessages(ctx context.Context, tenantID, mailboxID int64, folder string, opts webmail.ListOpts) (webmail.ListResult, error) {
	folders, err := s.ListFolders(ctx, tenantID, mailboxID)
	if err != nil {
		return webmail.ListResult{}, err
	}
	canonicalFolder := ""
	for _, f := range folders {
		if f.Name == folder || (strings.EqualFold(f.Name, "INBOX") && strings.EqualFold(folder, "INBOX")) {
			canonicalFolder = f.Name
			break
		}
	}
	if canonicalFolder == "" {
		return webmail.ListResult{}, ErrFolderNotFound
	}

	opts = webmail.ClampListOpts(opts)
	empty := webmail.ListResult{
		UIDValidity:  1,
		Messages:     []webmail.MessageSummary{},
		Total:        0,
		Sort:         opts.SortBy,
		ServerSorted: true,
	}

	hasTable, err := s.sqliteTableExists(ctx, "mailbox_messages")
	if err != nil {
		return webmail.ListResult{}, err
	}
	if !hasTable {
		return empty, nil
	}

	rows, err := s.DB.QueryContext(ctx,
		`SELECT uid_validity, uid, subject, from_name, from_email, date_micros, flags_json, size_bytes, has_attachments, preview, message_id, in_reply_to
		 FROM mailbox_messages
		 WHERE tenant_id = ?1 AND mailbox_id = ?2 AND folder = ?3
		 ORDER BY uid DESC`,
		tenantID, mailboxID, canonicalFolder,
	)
	if err != nil {
		return webmail.ListResult{}, err
	}
	defer rows.Close()

	var (
		uidValidity uint32 = 1
		all         []webmail.MessageSummary
	)
	for rows.Next() {
		var (
			rowValidity    uint32
			uid            uint32
			subject        string
			fromName       string
			fromEmail      string
			dateMicros     int64
			flagsJSON      string
			sizeBytes      int64
			hasAttachments bool
			preview        string
			messageID      string
			inReplyTo      string
		)
		if err := rows.Scan(
			&rowValidity, &uid, &subject, &fromName, &fromEmail,
			&dateMicros, &flagsJSON, &sizeBytes, &hasAttachments,
			&preview, &messageID, &inReplyTo,
		); err != nil {
			return webmail.ListResult{}, err
		}
		if rowValidity > 0 {
			uidValidity = rowValidity
		}
		var flags []string
		if flagsJSON != "" {
			_ = json.Unmarshal([]byte(flagsJSON), &flags)
		}
		if flags == nil {
			flags = []string{}
		}
		var from []webmail.Address
		if fromEmail != "" || fromName != "" {
			from = []webmail.Address{{Name: fromName, Email: fromEmail}}
		} else {
			from = []webmail.Address{}
		}
		summary := webmail.MessageSummary{
			UIDValidity:    uidValidity,
			HasAttachments: hasAttachments,
			UID:            uid,
			Subject:        subject,
			From:           from,
			Flags:          flags,
			Size:           sizeBytes,
			MessageID:      messageID,
			InReplyTo:      inReplyTo,
		}
		if dateMicros > 0 {
			t := timeutil.FromMicro(dateMicros)
			summary.Date = &t
		}
		if opts.Preview {
			summary.Preview = preview
		}
		if sqliteMessageMatchesQuery(summary, preview, opts.Query) {
			all = append(all, summary)
		}
	}
	if err := rows.Err(); err != nil {
		return webmail.ListResult{}, err
	}

	sortSQLiteMessages(all, opts.SortBy, opts.SortDesc)
	total := len(all)
	start := opts.Offset
	if opts.CursorUID > 0 {
		for idx, msg := range all {
			if msg.UID == opts.CursorUID {
				start = idx + 1
				break
			}
		}
	}
	if start > total {
		start = total
	}
	end := start + opts.Limit
	if end > total {
		end = total
	}
	page := make([]webmail.MessageSummary, end-start)
	copy(page, all[start:end])

	var nextCursor uint32
	if end < total && len(page) > 0 {
		nextCursor = page[len(page)-1].UID
	}
	return webmail.ListResult{
		UIDValidity:  uidValidity,
		Messages:     page,
		Total:        total,
		NextCursor:   nextCursor,
		Sort:         opts.SortBy,
		ServerSorted: true,
	}, nil
}

func sqliteMessageMatchesQuery(m webmail.MessageSummary, preview, rawQuery string) bool {
	q := strings.TrimSpace(strings.ToLower(rawQuery))
	if q == "" {
		return true
	}
	if q == "is:unseen" {
		for _, f := range m.Flags {
			if strings.EqualFold(f, `\Seen`) {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(q, "subject:") {
		needle := strings.TrimSpace(strings.TrimPrefix(q, "subject:"))
		return strings.Contains(strings.ToLower(m.Subject), needle)
	}
	if strings.HasPrefix(q, "from:") {
		needle := strings.TrimSpace(strings.TrimPrefix(q, "from:"))
		for _, addr := range m.From {
			if strings.Contains(strings.ToLower(addr.Email), needle) || strings.Contains(strings.ToLower(addr.Name), needle) {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(q, "text:") {
		q = strings.TrimSpace(strings.TrimPrefix(q, "text:"))
	}
	if strings.Contains(strings.ToLower(m.Subject), q) || strings.Contains(strings.ToLower(preview), q) {
		return true
	}
	for _, addr := range m.From {
		if strings.Contains(strings.ToLower(addr.Email), q) || strings.Contains(strings.ToLower(addr.Name), q) {
			return true
		}
	}
	return false
}

func sortSQLiteMessages(msgs []webmail.MessageSummary, sortBy string, desc bool) {
	if len(msgs) <= 1 {
		return
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		a, b := msgs[i], msgs[j]
		var cmp int
		switch sortBy {
		case "subject":
			cmp = strings.Compare(strings.ToLower(a.Subject), strings.ToLower(b.Subject))
		case "from":
			af, bf := "", ""
			if len(a.From) > 0 {
				af = strings.ToLower(a.From[0].Email)
			}
			if len(b.From) > 0 {
				bf = strings.ToLower(b.From[0].Email)
			}
			cmp = strings.Compare(af, bf)
		case "size":
			switch {
			case a.Size < b.Size:
				cmp = -1
			case a.Size > b.Size:
				cmp = 1
			}
		case "date":
			var at, bt int64
			if a.Date != nil {
				at = a.Date.UnixNano()
			}
			if b.Date != nil {
				bt = b.Date.UnixNano()
			}
			switch {
			case at < bt:
				cmp = -1
			case at > bt:
				cmp = 1
			}
		default:
			switch {
			case a.UID < b.UID:
				cmp = -1
			case a.UID > b.UID:
				cmp = 1
			}
			return cmp > 0
		}
		if cmp == 0 {
			return a.UID > b.UID
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}
