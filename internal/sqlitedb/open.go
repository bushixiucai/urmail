// Package sqlitedb is the Phase 1 SQLite connection factory (S1). The engine
// has no roles and no server: every guarantee is either a PRAGMA enforced
// PER CONNECTION (foreign_keys is a per-connection switch, so a startup
// assertion alone is insufficient — see v3.2 §3.3) or an application-layer
// contract (S2 decorator).
package sqlitedb

import (
	"context"
	"crypto/md5"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite" // pure-Go driver; no cgo, cross-compiles like the rest of the stack.

	"modernc.org/sqlite"
)

const (
	// ExpectedSchemaVersion is the current migrations_sqlite chain tip
	// (0001_init + 0002_write_side_effects + 0003_mailbox_readonly +
	// 0004_archive_trigram_fts + 0005_archive_bigram_fts：§3.5 trigram/bigram FTS5 升级 +
	// 0006_domain_tls_period_check：既有库的 TLS 周期 CHECK 重建)。
	// Startup verification fails closed unless PRAGMA user_version matches this exact version.
	ExpectedSchemaVersion = 6

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

// md5Scalar is PG's md5(text): the lowercase 32-character hex digest of the
// UTF-8 bytes. NULL stays NULL (PG md5(NULL) IS NULL), and any non-text argument
// is refused exactly like PG's missing md5(integer) overload — silently hashing
// an integer's decimal text would produce a digest PG never produced.
//
// This is a UDF rather than a rewrite to a builtin because the value participates
// in equality checks written before the migration (mailbox_sessions.password_version
// = md5(mailboxes.password_hash)), where "close enough" is a silent auth failure.
func md5Scalar(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("md5: want 1 argument, got %d", len(args))
	}
	var raw []byte
	switch v := args[0].(type) {
	case nil:
		return nil, nil
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return nil, fmt.Errorf("md5: unsupported argument type %T (PG has md5(text) only)", args[0])
	}
	sum := md5.Sum(raw)
	return hex.EncodeToString(sum[:]), nil
}

func asciiLowerString(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if b.Len() == 0 {
				b.Grow(len(s))
				b.WriteString(s[:i])
			}
			b.WriteByte(c + ('a' - 'A'))
		} else if b.Len() > 0 {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return s
	}
	return b.String()
}

// ArchiveBigramToken encodes a 2-rune query into the exact hex token indexed by
// archive_search_bigram_fts.
func ArchiveBigramToken(q string) string {
	return hex.EncodeToString([]byte(asciiLowerString(q)))
}

// ArchiveBigrams emits space-separated deduplicated hex-encoded 2-rune windows
// for FTS5 ascii indexing (supports 2-char CJK/ASCII queries in <3-rune path).
func ArchiveBigrams(s string) string {
	if s == "" {
		return ""
	}
	s = asciiLowerString(s)
	var prev rune
	hasPrev := false
	var buf [utf8.UTFMax * 2]byte
	seen := make(map[string]struct{}, 64)
	var out strings.Builder
	for _, r := range s {
		if !hasPrev {
			prev = r
			hasPrev = true
			continue
		}
		n := utf8.EncodeRune(buf[:], prev)
		n += utf8.EncodeRune(buf[n:], r)
		key := string(buf[:n])
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			if out.Len() > 0 {
				out.WriteByte(' ')
			}
			var hexBuf [utf8.UTFMax * 4]byte
			hn := hex.Encode(hexBuf[:], buf[:n])
			out.Write(hexBuf[:hn])
		}
		prev = r
	}
	return out.String()
}

func bigramsScalar(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("urmail_bigrams: want 1 argument, got %d", len(args))
	}
	switch v := args[0].(type) {
	case nil:
		return "", nil
	case string:
		return ArchiveBigrams(v), nil
	case []byte:
		return ArchiveBigrams(string(v)), nil
	default:
		return nil, fmt.Errorf("urmail_bigrams: unsupported argument type %T", args[0])
	}
}

// withTxLock forces every transaction the driver begins to be BEGIN IMMEDIATE.
// SQLite has no pg_advisory_xact_lock: the port replaces it with single-writer
// transaction serialization (see scripts/sqlite/lock-semantics.md). A deferred
// transaction does not serialize the read-then-write window — two transactions
// can both read, and the second write fails with SQLITE_BUSY_SNAPSHOT instead of
// waiting — so the lock semantic requires IMMEDIATE.
func withTxLock(path string) string {
	if strings.Contains(path, "_txlock=") {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "_txlock=immediate"
}

// RegisterConnectionHook wires the per-connection PRAGMA contract into the
// "sqlite" driver exactly once, and registers the PG builtin UDFs the query
// layer depends on (md5). Every connection the pool ever creates executes the
// pragmas; without this, foreign_keys silently defaults OFF on each new
// connection.
func RegisterConnectionHook() {
	hookOnce.Do(func() {
		if err := sqlite.RegisterScalarFunction("md5", 1, md5Scalar); err != nil {
			panic(fmt.Sprintf("register md5 UDF: %v", err))
		}
		if err := sqlite.RegisterDeterministicScalarFunction("urmail_bigrams", 1, bigramsScalar); err != nil {
			panic(fmt.Sprintf("register urmail_bigrams UDF: %v", err))
		}
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
	db, err := sql.Open("sqlite", withTxLock(path))
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

// OpenExisting opens an already-migrated SQLite database at path without
// creating a new file or running migrations (contract V2 §1.9.1). It rejects
// missing paths, symlinks, non-regular files, PRAGMA contract mismatches,
// dirty migration rows, and any PRAGMA user_version != expectedVersion.
func OpenExisting(ctx context.Context, path string, expectedVersion int) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite path is empty")
	}
	if expectedVersion <= 0 {
		return nil, fmt.Errorf("expected schema version must be positive, got %d", expectedVersion)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat existing sqlite %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing symlink sqlite path %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("sqlite path %s is not a regular file", path)
	}

	db, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithTimeout(ctx, pragmaTimeout)
	defer cancel()

	var version int
	if err := db.QueryRowContext(cctx, "PRAGMA user_version").Scan(&version); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read sqlite user_version: %w", err)
	}
	if version != expectedVersion {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite schema user_version = %d, want %d (unmigrated or version gap; refusing to start)", version, expectedVersion)
	}

	var dirtyCount int
	err = db.QueryRowContext(cctx,
		"SELECT count(*) FROM schema_migrations WHERE dirty <> 0",
	).Scan(&dirtyCount)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify schema_migrations table: %w", err)
	}
	if dirtyCount != 0 {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite schema_migrations has %d dirty row(s); refusing to start", dirtyCount)
	}

	if err := AssertPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
