package dovecotauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Change names one row whose snapshot copy must be refreshed after the main
// transaction commits. Address covers password updates, suspensions and
// deletions; DomainID and TenantID cover status flips that gate delivery.
type Change struct {
	Address  string
	DomainID int64
	TenantID int64
}

// Apply compensates one committed main-store change in the snapshot. A failed
// write marks the snapshot dirty and queues a bounded retry; the caller's main
// transaction has already committed, so this never rolls it back.
func (m *Manager) Apply(ctx context.Context, changes ...Change) error {
	if len(changes) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.applyLocked(ctx, changes); err != nil {
		m.markDirty()
		m.enqueue(changes)
		return err
	}
	if len(m.pending) == 0 {
		m.markOK()
	} else {
		// A successful write never masks a compensation another change still owes.
		m.markDirty()
	}
	return nil
}

// ApplyBestEffort is the post-commit hook shape: a snapshot failure degrades
// readiness instead of failing the committed change.
func (m *Manager) ApplyBestEffort(ctx context.Context, changes ...Change) {
	_ = m.Apply(ctx, changes...)
}

func (m *Manager) applyLocked(ctx context.Context, changes []Change) error {
	// WAL-fix: never open the published snapshot for writing. Copy it,
	// apply the compensations to the copy, and republish atomically —
	// byte-for-byte the same finalize contract as rebuildLocked
	// (checkpoint + DELETE journal + FileMode + sidecar cleanup), so the
	// read-only dovecot mount never sees a WAL flip or sidecars.
	dir := filepath.Dir(m.path)
	tmp, err := os.CreateTemp(dir, ".dovecot-auth-apply-*.tmp")
	if err != nil {
		return fmt.Errorf("create apply temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err = copyFileContents(m.path, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("copy published snapshot: %w", err)
	}

	db, err := sql.Open("sqlite", "file:"+tmpPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("open apply copy: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, change := range changes {
		if err = m.applyChange(ctx, tx, change); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("apply change to snapshot copy: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		_ = db.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("commit to snapshot copy: %w", err)
	}
	if err = db.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	// Finalize the copy: checkpoint, DELETE journal, FileMode, sidecar
	// cleanup — identical to rebuildLocked's contract.
	fin, err := sql.Open("sqlite", "file:"+tmpPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("finalize apply copy: %w", err)
	}
	if _, err = fin.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = fin.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("checkpoint apply copy: %w", err)
	}
	if _, err = fin.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
		_ = fin.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("finalize apply copy journal: %w", err)
	}
	if err = fin.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Remove(tmpPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("remove apply copy sidecar: %w", err)
		}
	}
	if err = os.Chmod(tmpPath, FileMode); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod apply copy: %w", err)
	}
	if err = os.Rename(tmpPath, m.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish apply copy: %w", err)
	}
	return nil
}

// copyFileContents copies one regular file's contents over another.
func copyFileContents(srcPath, dstPath string) error {
	src, err := os.Open(srcPath) // #nosec G304 -- manager-owned snapshot path.
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G304 -- manager-owned temp path.
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()
	_, err = io.Copy(dst, src)
	return err
}

func (m *Manager) applyChange(ctx context.Context, tx *sql.Tx, change Change) error {
	switch {
	case change.Address != "":
		rows, err := m.src.Query(ctx, mailboxQuery+` WHERE m.address = ?`, change.Address)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		if rows.Next() {
			var address, password, status, sieveMode, sieveScript string
			var tenantID, domainID, quota int64
			if err = rows.Scan(&address, &password, &tenantID, &domainID, &status, &quota, &sieveMode, &sieveScript); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO mailboxes(address,password_hash,tenant_id,domain_id,status,quota_bytes,sieve_mode,sieve_script)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(address) DO UPDATE SET password_hash=excluded.password_hash, tenant_id=excluded.tenant_id,
domain_id=excluded.domain_id, status=excluded.status, quota_bytes=excluded.quota_bytes,
sieve_mode=excluded.sieve_mode, sieve_script=excluded.sieve_script`,
				address, password, tenantID, domainID, status, quota, sieveMode, sieveScript)
			return err
		}
		if err = rows.Err(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM mailboxes WHERE address = ?`, change.Address)
		return err
	case change.DomainID != 0:
		rows, err := m.src.Query(ctx, `SELECT tenant_id, id, status, `+mailReadyExpr+` FROM domains d WHERE d.id = ?`, change.DomainID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		if rows.Next() {
			var tenantID, domainID, mailReady int64
			var status string
			if err = rows.Scan(&tenantID, &domainID, &status, &mailReady); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO domains(tenant_id,domain_id,status,mail_ready) VALUES(?,?,?,?)
ON CONFLICT(tenant_id,domain_id) DO UPDATE SET status=excluded.status, mail_ready=excluded.mail_ready`,
				tenantID, domainID, status, mailReady)
			return err
		}
		if err = rows.Err(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM domains WHERE domain_id = ?`, change.DomainID)
		return err
	case change.TenantID != 0:
		rows, err := m.src.Query(ctx, `SELECT id, status FROM tenants WHERE id = ?`, change.TenantID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		if rows.Next() {
			var id int64
			var status string
			if err = rows.Scan(&id, &status); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO tenants(tenant_id,status) VALUES(?,?)
ON CONFLICT(tenant_id) DO UPDATE SET status=excluded.status`, id, status)
			return err
		}
		if err = rows.Err(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM tenants WHERE tenant_id = ?`, change.TenantID)
		return err
	default:
		return errors.New("empty snapshot change")
	}
}

// pendingChange is one queued compensation the retry loop still owes.
type pendingChange struct {
	changes []Change
	nextAt  time.Time
	delay   time.Duration
}

// enqueue records failed changes for the retry loop. Called with m.mu held.
func (m *Manager) enqueue(changes []Change) {
	m.pending = append(m.pending, pendingChange{changes: changes, nextAt: m.now().Add(m.retryDelay), delay: m.retryDelay})
	m.startRetryLocked()
}

// startRetryLocked launches one retry goroutine for the manager lifetime.
func (m *Manager) startRetryLocked() {
	if m.retryRunning {
		return
	}
	m.retryRunning = true
	go m.retryLoop()
}

// retryLoop applies queued compensations with bounded exponential backoff. When
// every queued change has reached the maximum delay, a full rebuild runs: it is
// the repair channel for a damaged snapshot and clears the queue on success.
func (m *Manager) retryLoop() {
	for {
		time.Sleep(m.retryDelay)
		m.mu.Lock()
		if len(m.pending) == 0 {
			m.retryRunning = false
			m.mu.Unlock()
			return
		}
		now := m.now()
		remaining := m.pending[:0]
		for _, item := range m.pending {
			if now.Before(item.nextAt) {
				remaining = append(remaining, item)
				continue
			}
			if err := m.applyLocked(context.Background(), item.changes); err != nil {
				item.delay *= 2
				if item.delay > m.maxRetry {
					item.delay = m.maxRetry
				}
				item.nextAt = now.Add(item.delay)
				remaining = append(remaining, item)
				continue
			}
		}
		// Dirty clears only when nothing is still owed: one successful item in a
		// batch must not mask a later failing one.
		if len(remaining) == 0 {
			m.markOK()
		} else {
			m.markDirty()
		}
		m.pending = remaining
		if len(m.pending) > 0 && m.escalateLocked() {
			if _, err := m.rebuildLocked(context.Background()); err == nil {
				m.pending = nil
			}
		}
		m.mu.Unlock()
	}
}

// escalateLocked reports whether every queued item already waited the maximum
// backoff, which is the point where a full rebuild is cheaper than retrying.
func (m *Manager) escalateLocked() bool {
	for _, item := range m.pending {
		if item.delay < m.maxRetry {
			return false
		}
	}
	return true
}
