// Package dovecotauth owns the read-only SQLite snapshot dovecot authenticates
// against (design §5, v3.2). The snapshot is derived from the main store and is
// replaced atomically; dovecot never reads the main database.
package dovecotauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the sqlite driver used by the snapshot file
)

// DefaultPath is the snapshot dovecot's db.conf points at. It lives in its
// OWN parent directory (cutover-blockers Blocker 2): nesting it inside the
// urmaild archive volume made the atomic os.Rename publish fail with EBUSY
// (cross-mountpoint) and tore the inode out from under dovecot's open
// handle. The deployment mounts the same standalone parent directory into
// both urmaild (rw, publisher) and dovecot (ro, consumer); the compose
// override lives with DevOps.
const DefaultPath = "/srv/urmail/data/dovecot-auth/dovecot-auth.db"

// DirectoryMode keeps the snapshot directory traversable and the file
// readable by the dovecot auth worker without granting write access: the
// containerized dovecot uid has no membership in the writer group, so the
// world-read bit on the directory is the contract that makes the standalone
// mount work (cutover-blockers Blocker 2). Write access stays
// publisher-only.
const DirectoryMode os.FileMode = 0o755

// FileMode matches the same readability contract as DirectoryMode: the
// containerized dovecot auth worker reads the snapshot directly, so the
// file is world-readable (still not writable — only the publisher writes).
const FileMode os.FileMode = 0o644

// SchemaVersion is stored in PRAGMA user_version of the snapshot.
const SchemaVersion = 1

// The two predicates below are the SINGLE SOURCE of the column contract
// between this snapshot schema and the dovecot SQL dovecot renders
// (internal/mail/dovecot consumes them verbatim). They were duplicated by
// hand once and drifted — the rendered queries referenced columns the
// snapshot never had, and every IMAP authentication failed on load
// (cutover-blockers Blocker 1). Any schema change here MUST go through
// these constants, and TestAuthSnapshotServesRenderedQueries executes the
// rendered SQL against a real snapshot build to keep the two honest.
const (
	// TenantActivePredicate matches a tenant row that is currently active,
	// joined from a mailbox row (alias m).
	TenantActivePredicate = "EXISTS(SELECT 1 FROM tenants t WHERE t.tenant_id = m.tenant_id AND t.status = 'active')"
	// DomainReadyPredicate matches the mailbox's domain in its mail-ready
	// state (snapshot column mail_ready collapses the PostgreSQL
	// urmail_domain_mail_ready function semantics for the read-only copy).
	DomainReadyPredicate = "EXISTS(SELECT 1 FROM domains d WHERE d.domain_id = m.domain_id AND d.tenant_id = m.tenant_id AND d.status = 'active' AND d.mail_ready = 1)"
	// ReadyJoin composes both predicates for the rendered queries.
	ReadyJoin = TenantActivePredicate + " AND " + DomainReadyPredicate
)

// schemaSQL is the dovecot-facing surface: one row per mailbox plus the two
// status tables the rendered queries consult with EXISTS subqueries.
const schemaSQL = `
CREATE TABLE tenants (
  tenant_id INTEGER PRIMARY KEY,
  status TEXT NOT NULL
);
CREATE TABLE domains (
  tenant_id INTEGER NOT NULL,
  domain_id INTEGER NOT NULL,
  status TEXT NOT NULL,
  mail_ready INTEGER NOT NULL,
  PRIMARY KEY (tenant_id, domain_id)
);
CREATE TABLE mailboxes (
  address TEXT PRIMARY KEY,
  password_hash TEXT NOT NULL,
  tenant_id INTEGER NOT NULL,
  domain_id INTEGER NOT NULL,
  status TEXT NOT NULL,
  quota_bytes INTEGER NOT NULL,
  sieve_mode TEXT NOT NULL,
  sieve_script TEXT NOT NULL
);
`

// Source supplies the rows a rebuild copies. Both the PostgreSQL and the
// single-engine SQLite store expose the same column names, so the builder
// issues one dialect-neutral SELECT set.
type Source interface {
	Query(ctx context.Context, query string, args ...any) (Rows, error)
}

// Rows is the minimal row iterator the builder needs.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

const tenantQuery = `SELECT id, status FROM tenants`

// mailReadyExpr mirrors urmail_domain_mail_ready: a domain is delivery-ready
// when it is active, verified and has been provisioned.
const mailReadyExpr = `CASE WHEN d.status = 'active' AND d.verified_at IS NOT NULL AND d.provisioning_state = 'ready' THEN 1 ELSE 0 END`

const mailboxQuery = `SELECT m.address, m.password_hash, m.tenant_id, m.domain_id, m.status, m.quota_bytes, COALESCE(m.sieve_mode,''), COALESCE(m.sieve_script,'') FROM mailboxes m`

// Snapshot is one built generation plus the moments that bound its staleness.
type Snapshot struct {
	Path      string
	Version   int
	Mailboxes int
	BuiltAt   time.Time
}

// Manager keeps one snapshot current and tracks whether the last compensation
// failed. A dirty manager reports degraded readiness until a retry succeeds or
// a full rebuild repairs the file.
type Manager struct {
	path string
	src  Source

	mu           sync.Mutex
	dirty        atomic.Bool
	lastOK       atomic.Int64 // unix nanos of the last successful snapshot write
	pending      []pendingChange
	retryRunning bool

	retryDelay time.Duration
	maxRetry   time.Duration

	now func() time.Time
}

// NewManager binds the snapshot path to its source.
func NewManager(path string, src Source) *Manager {
	m := &Manager{path: path, src: src, retryDelay: 250 * time.Millisecond, maxRetry: 30 * time.Second, now: time.Now}
	SetMetricsManager(m)
	return m
}

// Path reports the snapshot file the renderer must point dovecot at.
func (m *Manager) Path() string { return m.path }

// Dirty reports whether the snapshot lags the main store.
func (m *Manager) Dirty() bool { return m.dirty.Load() }

// Staleness reports how old the newest successful snapshot is.
func (m *Manager) Staleness() time.Duration {
	last := m.lastOK.Load()
	if last == 0 {
		return time.Duration(1<<62 - 1)
	}
	return m.now().Sub(time.Unix(0, last))
}

// markOK records a successful snapshot write and clears the dirty flag.
func (m *Manager) markOK() {
	m.lastOK.Store(m.now().UnixNano())
	m.dirty.Store(false)
}

// markDirty makes the lag observable; readiness reports degraded until a later
// write succeeds.
func (m *Manager) markDirty() { m.dirty.Store(true) }

// Rebuild writes a complete generation to a temporary file, removes the WAL
// sidecars, and atomically renames it over the live snapshot.
func (m *Manager) Rebuild(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rebuildLocked(ctx)
}

func (m *Manager) rebuildLocked(ctx context.Context) (Snapshot, error) {
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, DirectoryMode); err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot directory: %w", err)
	}
	if err := os.Chmod(dir, DirectoryMode); err != nil {
		return Snapshot{}, fmt.Errorf("chmod snapshot directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".dovecot-auth-*.tmp")
	if err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath); _ = os.Remove(tmpPath + "-wal"); _ = os.Remove(tmpPath + "-shm") }
	if err = tmp.Close(); err != nil {
		cleanup()
		return Snapshot{}, err
	}

	db, err := sql.Open("sqlite", "file:"+tmpPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		cleanup()
		return Snapshot{}, fmt.Errorf("open snapshot temp: %w", err)
	}
	count, buildErr := m.build(ctx, db)
	// The snapshot never stays in WAL: checkpoint and switch the journal mode so
	// the published file is a single self-contained file.
	if buildErr == nil {
		if _, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			buildErr = fmt.Errorf("checkpoint snapshot: %w", err)
		}
	}
	if buildErr == nil {
		if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
			buildErr = fmt.Errorf("switch snapshot journal mode: %w", err)
		}
	}
	if buildErr == nil {
		_, buildErr = db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion))
	}
	if closeErr := db.Close(); buildErr == nil && closeErr != nil {
		buildErr = closeErr
	}
	if buildErr != nil {
		cleanup()
		return Snapshot{}, buildErr
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Remove(tmpPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanup()
			return Snapshot{}, fmt.Errorf("remove snapshot sidecar: %w", err)
		}
	}
	if err = os.Chmod(tmpPath, FileMode); err != nil {
		cleanup()
		return Snapshot{}, fmt.Errorf("chmod snapshot: %w", err)
	}
	// Remove stale sidecars of the PREVIOUS published file first: a drifted
	// (WAL) snapshot leaves -wal/-shm next to the path, and leaving them
	// next to the fresh single-file snapshot would re-attach them on the
	// next open (QA wal-fix acceptance b).
	for _, suffix := range []string{m.path + "-wal", m.path + "-shm"} {
		if err = os.Remove(suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanup()
			return Snapshot{}, fmt.Errorf("remove stale sidecar: %w", err)
		}
	}
	if err = os.Rename(tmpPath, m.path); err != nil {
		cleanup()
		return Snapshot{}, fmt.Errorf("publish snapshot: %w", err)
	}
	snap := Snapshot{Path: m.path, Version: SchemaVersion, Mailboxes: count, BuiltAt: m.now().UTC()}
	m.markOK()
	return snap, nil
}

// build copies the source rows into the temp database inside one transaction.
func (m *Manager) build(ctx context.Context, db *sql.DB) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, schemaSQL); err != nil {
		return 0, fmt.Errorf("create snapshot schema: %w", err)
	}
	tenants, err := m.src.Query(ctx, tenantQuery)
	if err != nil {
		return 0, fmt.Errorf("read tenants: %w", err)
	}
	for tenants.Next() {
		var id int64
		var status string
		if err = tenants.Scan(&id, &status); err != nil {
			_ = tenants.Close()
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO tenants(tenant_id,status) VALUES(?,?)`, id, status); err != nil {
			_ = tenants.Close()
			return 0, err
		}
	}
	if err = tenants.Err(); err != nil {
		_ = tenants.Close()
		return 0, err
	}
	_ = tenants.Close()

	domains, err := m.src.Query(ctx, `SELECT tenant_id, id, status, `+mailReadyExpr+` FROM domains d`)
	if err != nil {
		return 0, fmt.Errorf("read domains: %w", err)
	}
	for domains.Next() {
		var tenantID, domainID, mailReady int64
		var status string
		if err = domains.Scan(&tenantID, &domainID, &status, &mailReady); err != nil {
			_ = domains.Close()
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO domains(tenant_id,domain_id,status,mail_ready) VALUES(?,?,?,?)`, tenantID, domainID, status, mailReady); err != nil {
			_ = domains.Close()
			return 0, err
		}
	}
	if err = domains.Err(); err != nil {
		_ = domains.Close()
		return 0, err
	}
	_ = domains.Close()

	boxes, err := m.src.Query(ctx, mailboxQuery)
	if err != nil {
		return 0, fmt.Errorf("read mailboxes: %w", err)
	}
	count := 0
	for boxes.Next() {
		var address, password, status, sieveMode, sieveScript string
		var tenantID, domainID, quota int64
		if err = boxes.Scan(&address, &password, &tenantID, &domainID, &status, &quota, &sieveMode, &sieveScript); err != nil {
			_ = boxes.Close()
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO mailboxes(address,password_hash,tenant_id,domain_id,status,quota_bytes,sieve_mode,sieve_script) VALUES(?,?,?,?,?,?,?,?)`,
			address, password, tenantID, domainID, status, quota, sieveMode, sieveScript); err != nil {
			_ = boxes.Close()
			return 0, err
		}
		count++
	}
	if err = boxes.Err(); err != nil {
		_ = boxes.Close()
		return 0, err
	}
	_ = boxes.Close()
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
