package dovecotauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"
)

// Counts is the row-count fingerprint compared at startup.
type Counts struct {
	Tenants   int64
	Domains   int64
	Mailboxes int64
}

// ErrSnapshotDrift marks a published snapshot whose journal mode or
// sidecar state violates the single-file DELETE contract.
var ErrSnapshotDrift = errors.New("auth snapshot journal drift")

// Reconcile compares the published snapshot with the source: row counts always,
// plus a random sample of mailbox rows when sample > 0. A mismatch marks the
// snapshot dirty; callers run Repair to rebuild it.
//
// The snapshot is opened STRICTLY read-only (mode=ro + query_only): the
// process-global sqlite hook must not flip its journal mode, and any drift
// (WAL mode set, -wal/-shm sidecars present) is reported as dirty so
// Repair -> Rebuild republishes a clean single-file DELETE snapshot
// (cutover-blockers WAL-fix: a WAL flip made the read-only dovecot mount
// fail with SQLITE_CANTOPEN).
func (m *Manager) Reconcile(ctx context.Context, sample int) error {
	src, err := m.sourceCounts(ctx)
	if err != nil {
		return err
	}
	if drift := snapshotJournalDrift(m.path); drift != "" {
		m.markDirty()
		return fmt.Errorf("%w: %s", ErrSnapshotDrift, drift)
	}
	db, err := openSnapshotReadOnly(m.path)
	if err != nil {
		return fmt.Errorf("open auth snapshot: %w", err)
	}
	defer func() { _ = db.Close() }()
	var snap Counts
	for _, probe := range []struct {
		query string
		dst   *int64
	}{
		{`SELECT count(*) FROM tenants`, &snap.Tenants},
		{`SELECT count(*) FROM domains`, &snap.Domains},
		{`SELECT count(*) FROM mailboxes`, &snap.Mailboxes},
	} {
		if err = db.QueryRowContext(ctx, probe.query).Scan(probe.dst); err != nil {
			m.markDirty()
			return fmt.Errorf("read snapshot counts: %w", err)
		}
	}
	if src != snap {
		m.markDirty()
		return fmt.Errorf("auth snapshot diverged from the main store: source=%+v snapshot=%+v", src, snap)
	}
	if sample <= 0 {
		return nil
	}
	rows, err := m.src.Query(ctx, mailboxQuery)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var addresses []string
	for rows.Next() {
		var address, password, status, sieveMode, sieveScript string
		var tenantID, domainID, quota int64
		if err = rows.Scan(&address, &password, &tenantID, &domainID, &status, &quota, &sieveMode, &sieveScript); err != nil {
			return err
		}
		addresses = append(addresses, address)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(addresses) == 0 {
		return nil
	}
	if sample > len(addresses) {
		sample = len(addresses)
	}
	offset := rand.Intn(len(addresses) - sample + 1) // #nosec G404 -- sampling offset, not security material.
	for _, address := range addresses[offset : offset+sample] {
		want, err := m.sourceMailbox(ctx, address)
		if err != nil {
			return err
		}
		var password, status string
		var quota int64
		err = db.QueryRowContext(ctx, `SELECT password_hash, status, quota_bytes FROM mailboxes WHERE address = ?`, address).Scan(&password, &status, &quota)
		if errors.Is(err, sql.ErrNoRows) || password != want.password || status != want.status || quota != want.quota {
			m.markDirty()
			return fmt.Errorf("auth snapshot row %s diverged from the main store", address)
		}
		if err != nil {
			m.markDirty()
			return fmt.Errorf("read snapshot row %s: %w", address, err)
		}
	}
	return nil
}

// Repair rebuilds the snapshot when Reconcile reports divergence.
func (m *Manager) Repair(ctx context.Context) (Snapshot, error) { return m.Rebuild(ctx) }

type mailboxRow struct {
	password string
	status   string
	quota    int64
}

func (m *Manager) sourceMailbox(ctx context.Context, address string) (mailboxRow, error) {
	rows, err := m.src.Query(ctx, mailboxQuery+` WHERE m.address = ?`, address)
	if err != nil {
		return mailboxRow{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return mailboxRow{}, err
		}
		return mailboxRow{}, fmt.Errorf("mailbox %s missing from the main store", address)
	}
	var row mailboxRow
	var wantAddress, sieveMode, sieveScript string
	var tenantID, domainID int64
	if err = rows.Scan(&wantAddress, &row.password, &tenantID, &domainID, &row.status, &row.quota, &sieveMode, &sieveScript); err != nil {
		return mailboxRow{}, err
	}
	return row, nil
}

func (m *Manager) sourceCounts(ctx context.Context) (Counts, error) {
	var out Counts
	for _, probe := range []struct {
		query string
		dst   *int64
	}{
		{`SELECT count(*) FROM tenants`, &out.Tenants},
		{`SELECT count(*) FROM domains`, &out.Domains},
		{`SELECT count(*) FROM mailboxes`, &out.Mailboxes},
	} {
		rows, err := m.src.Query(ctx, probe.query)
		if err != nil {
			return Counts{}, err
		}
		if !rows.Next() {
			_ = rows.Close()
			return Counts{}, fmt.Errorf("count query returned no row")
		}
		if err = rows.Scan(probe.dst); err != nil {
			_ = rows.Close()
			return Counts{}, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// RequireFreshAfterRestore is the single entry the restore sequence calls: the
// snapshot must be rebuilt from the restored store before auth is released,
// otherwise password changes made after the restore point (but before the file
// rollback) would keep an old snapshot live with new credentials.
func RequireFreshAfterRestore(ctx context.Context, m *Manager) (Snapshot, error) {
	if m == nil {
		return Snapshot{}, errors.New("auth snapshot manager is required after restore")
	}
	snap, err := m.Rebuild(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("rebuild auth snapshot after restore: %w", err)
	}
	if snap.Mailboxes < 0 {
		return Snapshot{}, errors.New("auth snapshot rebuild returned an invalid mailbox count")
	}
	return snap, nil
}

// MaxStalenessThreshold is the alerting threshold for snapshot age.
const MaxStalenessThreshold = 60 * time.Second

// snapshotJournalDrift returns a human-readable drift description when the
// published snapshot violates the single-file DELETE contract (journal mode
// flipped to WAL, or -wal/-shm sidecars present), or "" when clean.
func snapshotJournalDrift(path string) string {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return "cannot open for journal inspection: " + err.Error()
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err := db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return "cannot read journal mode: " + err.Error()
	}
	if mode != "delete" {
		return "journal mode is " + mode + ", want delete"
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return suffix + " sidecar present"
		}
	}
	return ""
}

// openSnapshotReadOnly opens the published snapshot strictly read-only:
// mode=ro forbids writes, query_only forbids pragma writes, so neither the
// process-global sqlite hook nor anything else can flip the journal mode.
func openSnapshotReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
