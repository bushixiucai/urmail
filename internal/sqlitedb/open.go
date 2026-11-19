// Package sqlitedb is the Phase 1 SQLite connection factory (S1). The engine
// has no roles and no server: every guarantee is either a PRAGMA enforced
// PER CONNECTION (foreign_keys is a per-connection switch, so a startup
// assertion alone is insufficient — see v3.2 §3.3) or an application-layer
// contract (S2 decorator).
package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; no cgo, cross-compiles like the rest of the stack.

	"modernc.org/sqlite"
)

const (
	busyTimeoutMs    = 5000
	pragmaTimeout    = 10 * time.Second
	expectJournalWAL = "wal"
	expectSyncNormal = 1
)

var (
	hookOnce sync.Once
)

// pragmaStatements run on EVERY new connection the driver opens. PRAGMA
// foreign_keys and busy_timeout are connection-scoped; journal_mode and
// synchronous are set here as well so a single hook establishes the whole
// contract and AssertPragmas can verify it per connection.
func pragmaStatements() []string {
	return []string{
		"PRAGMA foreign_keys=ON",
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMs),
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	}
}

// RegisterConnectionHook wires the per-connection PRAGMA contract into the
// "sqlite" driver exactly once. Every connection the pool ever creates
// executes the pragmas; without this, foreign_keys silently defaults OFF on
// each new connection.
func RegisterConnectionHook() {
	hookOnce.Do(func() {
		sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
			ctx, cancel := context.WithTimeout(context.Background(), pragmaTimeout)
			defer cancel()
			for _, stmt := range pragmaStatements() {
				if _, err := conn.ExecContext(ctx, stmt, nil); err != nil {
					return fmt.Errorf("connection pragma %q: %w", stmt, err)
				}
			}
			return nil
		})
	})
}

// Open opens the SQLite database at path with the full PRAGMA contract
// enforced on every connection the pool ever creates, plus one immediate
// assertion so misconfiguration fails at startup, not on first write.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	RegisterConnectionHook()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Open one connection eagerly: the pragma hook runs here, and failures
	// surface at Open instead of the first query.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite %s (first connection): %w", path, err)
	}
	if err := AssertPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// AssertPragmas verifies the connection contract on a live connection.
// Startup calls it once; tests and the migrator call it after any
// suspicious reconfiguration.
func AssertPragmas(ctx context.Context, db *sql.DB) error {
	cctx, cancel := context.WithTimeout(ctx, pragmaTimeout)
	defer cancel()
	var fk int
	if err := db.QueryRowContext(cctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("assert foreign_keys: %w", err)
	}
	if fk != 1 {
		return fmt.Errorf("foreign_keys is OFF — refusing to serve (per-connection contract)")
	}
	var mode string
	if err := db.QueryRowContext(cctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("assert journal_mode: %w", err)
	}
	if mode != expectJournalWAL {
		return fmt.Errorf("journal_mode = %q, want %q", mode, expectJournalWAL)
	}
	var busy int
	if err := db.QueryRowContext(cctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		return fmt.Errorf("assert busy_timeout: %w", err)
	}
	if busy != busyTimeoutMs {
		return fmt.Errorf("busy_timeout = %d, want %d", busy, busyTimeoutMs)
	}
	var sync int
	if err := db.QueryRowContext(cctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		return fmt.Errorf("assert synchronous: %w", err)
	}
	if sync != expectSyncNormal {
		return fmt.Errorf("synchronous = %d, want %d (NORMAL)", sync, expectSyncNormal)
	}
	return nil
}
