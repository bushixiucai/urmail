package datamigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// StagingOptions configures one controlled offline recall preservation run.
// The target file must not exist: it never overwrites a published database.
type StagingOptions struct {
	// SourceURL is a loopback PostgreSQL URL for the migration role. The CLI
	// reads it from an environment variable, never argv.
	SourceURL string
	// TargetPath is the final, not-yet-existing SQLite file path.
	TargetPath string
	// Migrations is the SQLite schema chain (embedded in the product binary).
	Migrations fs.FS
	// MigrationsRoot is the chain root inside Migrations ("." for the embed).
	MigrationsRoot string
	// TypeMap is migrations_sqlite/type-map.tsv parsed once by the caller.
	TypeMap TypeMap
	// BatchSize overrides the keyset batch size (default 500).
	BatchSize int
	// SkipTables, when non-nil, is the explicit skip set for the copy (the
	// product derives the recall referential closure from live introspection;
	// controlled tests pass a fixed set).
	SkipTables []string
}

// StagingReport is the frozen ledger of one staging import.
type StagingReport struct {
	TargetPath        string
	Rows              int
	RecallMigrated    int
	RecallStatus      map[string]int64
	RecallTableHash   string
	RecallGuardHash   string
	SchemaDiff        int
	IndexDiff         int
	ForeignKeyIssues  int
	Integrity         string
	RuntimeGates      int
	RuntimeSkipped    string
	SourceFingerprint string
	SnapshotIsolation string
	SnapshotReadOnly  string
}

// ValidateStagingSourceURL enforces the managed local source contract: the
// migration role over a loopback address, no URL fragments, and only the
// documented connection parameters. Credentials never appear in errors.
func ValidateStagingSourceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil {
		return errors.New("staging source requires a PostgreSQL migration URL")
	}
	if u.User.Username() != "urmail_migrate" {
		return errors.New("staging source requires the urmail_migrate role")
	}
	if u.Fragment != "" {
		return errors.New("staging source must not carry a fragment")
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "" {
		return errors.New("staging source must be the managed local PostgreSQL instance")
	}
	database := strings.TrimPrefix(u.Path, "/")
	if database == "" || len(database) > 63 || strings.IndexFunc(database, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_", r)
	}) >= 0 {
		return errors.New("staging source database name is not supported")
	}
	for key := range u.Query() {
		if key != "sslmode" && key != "application_name" && key != "connect_timeout" {
			return fmt.Errorf("staging source connection override %q is not supported", key)
		}
	}
	return nil
}

// OpenPGSource connects to the source and exposes it as a datamigrate.Source.
// The caller closes the returned closer; the import never writes to it.
func OpenPGSource(ctx context.Context, dsn string) (Source, func(), error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, nil, errors.New("staging source URL is not parseable")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("open staging source: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("staging source unreachable: %w", err)
	}
	// One read-only REPEATABLE READ transaction backs every read of the
	// import: catalog introspection, row copy and verification all observe
	// the same snapshot, and PostgreSQL itself rejects any write on it.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("begin read snapshot: %w", err)
	}
	closer := func() {
		_ = tx.Rollback(context.Background())
		pool.Close()
	}
	return pgxSource{tx: tx}, closer, nil
}

type pgxSource struct{ tx pgx.Tx }

func (s pgxSource) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	rows, err := s.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgxRows{Rows: rows}, nil
}

type pgxRows struct{ pgx.Rows }

func (r pgxRows) Columns() ([]string, error) {
	fds := r.FieldDescriptions()
	names := make([]string, len(fds))
	for i, fd := range fds {
		names[i] = fd.Name
	}
	return names, nil
}

// ImportRecallStaging builds a private, schema-complete staging database,
// preserves every legal recall_requests row verbatim (all columns), verifies
// the recall guard/table ledger and the schema/index/FK integrity gates, and
// only then publishes the file with a no-overwrite link. Any failure removes
// the private staging file and leaves both the source and any pre-existing
// target byte-for-byte unchanged.
func ImportRecallStaging(ctx context.Context, opts StagingOptions) (StagingReport, error) {
	report := StagingReport{}
	if err := validateTargetPath(opts.TargetPath); err != nil {
		return report, err
	}
	if err := ValidateStagingSourceURL(opts.SourceURL); err != nil {
		return report, err
	}
	if opts.Migrations == nil || opts.TypeMap == nil {
		return report, errors.New("staging requires the embedded migrations and type map")
	}
	root := opts.MigrationsRoot
	if root == "" {
		root = "."
	}
	target, err := filepath.Abs(opts.TargetPath)
	if err != nil {
		return report, fmt.Errorf("resolve target: %w", err)
	}
	parent := filepath.Dir(target)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return report, errors.New("staging target parent directory is missing")
	}

	// Exclusive unpublished target: an existing file is never replaced.
	if _, err := os.Lstat(target); err == nil {
		return report, fmt.Errorf("refusing existing staging target %s: publish never overwrites", filepath.Base(target))
	} else if !errors.Is(err, os.ErrNotExist) {
		return report, fmt.Errorf("stat staging target: %w", err)
	}
	lockPath := target + ".lock"
	if !cleanWithin(lockPath, filepath.Dir(target)) || filepath.Dir(filepath.Clean(lockPath)) != filepath.Dir(filepath.Clean(target)) {
		return report, fmt.Errorf("staging lock path escapes the staging directory: %s", filepath.Base(lockPath))
	}
	if err := reclaimStaleLock(lockPath); err != nil {
		return report, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return report, fmt.Errorf("staging target is locked by another run (%s)", filepath.Base(lockPath))
		}
		return report, fmt.Errorf("create staging lock: %w", err)
	}
	defer func() {
		_ = lock.Close()
		_ = os.Remove(lockPath)
	}()
	if _, err := fmt.Fprintf(lock, "pid=%d started=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return report, fmt.Errorf("write staging lock: %w", err)
	}

	tmp, err := os.CreateTemp(parent, ".urmail-s5-recall-*.sqlite")
	if err != nil {
		return report, fmt.Errorf("create private staging file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		removeSQLiteFiles(tmpPath)
		return report, fmt.Errorf("chmod staging file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		removeSQLiteFiles(tmpPath)
		return report, fmt.Errorf("close staging placeholder: %w", err)
	}
	if err := lock.Truncate(0); err == nil {
		_, _ = lock.Seek(0, 0)
		_, _ = fmt.Fprintf(lock, "pid=%d tmp=%s started=%s\n", os.Getpid(), tmpPath, time.Now().UTC().Format(time.RFC3339))
	}
	published := false
	defer func() {
		if !published {
			removeSQLiteFiles(tmpPath)
		}
	}()

	db, err := sqlitedb.Open(ctx, tmpPath)
	if err != nil {
		return report, fmt.Errorf("open private staging database: %w", err)
	}
	closed := false
	closeDB := func() {
		if !closed {
			closed = true
			_ = db.Close()
		}
	}
	defer closeDB()
	if err := sqlitedb.Migrate(ctx, db, opts.Migrations, root); err != nil {
		closeDB()
		return report, fmt.Errorf("migrate staging schema: %w", err)
	}

	src, closer, err := OpenPGSource(ctx, opts.SourceURL)
	if err != nil {
		closeDB()
		return report, err
	}
	defer closer()

	if err := verifySourceSchema(ctx, src); err != nil {
		closeDB()
		return report, err
	}
	if err := verifyReadSnapshot(ctx, src, &report); err != nil {
		closeDB()
		return report, err
	}

	skip := opts.SkipTables
	if skip == nil {
		plan, _, err := IntrospectSource(ctx, src)
		if err != nil {
			closeDB()
			return report, fmt.Errorf("introspect source: %w", err)
		}
		skip = recallClosureSkip(plan)
	}
	fingerprintBefore, err := sourceRecallFingerprint(ctx, src)
	if err != nil {
		closeDB()
		return report, err
	}
	stats, err := Migrate(ctx, src, &Target{DB: db}, opts.TypeMap, Config{SkipTables: skip, BatchSize: opts.BatchSize})
	if err != nil {
		closeDB()
		return report, fmt.Errorf("recall staging import: %w", err)
	}
	if len(stats.RecallRefused) != 0 {
		closeDB()
		return report, fmt.Errorf("recall staging refused %d illegal row(s)", len(stats.RecallRefused))
	}
	if stats.RecallMigrated != stats.PerTable["recall_requests"] {
		closeDB()
		return report, errors.New("recall staging ledger mismatch")
	}

	// Other S5 gates before publish: 20-column recall zero-diff, schema and
	// unique-index parity, FK and page integrity.
	if err := verifyStagedRecall(ctx, src, db, opts.TypeMap, stats, &report); err != nil {
		closeDB()
		return report, err
	}
	if err := verifySourceUnchanged(ctx, src, fingerprintBefore); err != nil {
		closeDB()
		return report, err
	}
	gateReport, err := runRuntimeRecallGates(ctx, db)
	if err != nil {
		closeDB()
		return report, err
	}
	report.RuntimeGates = gateReport.Checked
	report.RuntimeSkipped = gateReport.Skipped

	// Close cleanly, fold the WAL back into the main file, fsync, then publish
	// with link(2): the publish fails if any file appeared at the target.
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		closeDB()
		return report, fmt.Errorf("checkpoint staging WAL: %w", err)
	}
	closed = true
	if err := db.Close(); err != nil {
		return report, fmt.Errorf("close staging database: %w", err)
	}
	if err := syncFile(tmpPath); err != nil {
		return report, err
	}
	if err := os.Link(tmpPath, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return report, fmt.Errorf("staging target appeared during import: %s", filepath.Base(target))
		}
		return report, fmt.Errorf("publish staging target: %w", err)
	}
	published = true
	if err := syncDir(parent); err != nil {
		// The directory entry was not durably recorded: remove the new name so
		// the failure leaves no published target, then report.
		_ = os.Remove(target)
		return report, err
	}
	removeSQLiteFiles(tmpPath)

	report.TargetPath = target
	report.Rows = stats.RecallMigrated
	report.RecallMigrated = stats.RecallMigrated
	report.RecallStatus = stats.RecallStatusCounts
	report.RecallTableHash = stats.RecallTableHash
	report.RecallGuardHash = stats.RecallGuardHash
	report.SchemaDiff = 0
	report.IndexDiff = 0
	report.SourceFingerprint = fingerprintBefore
	closer()
	return report, nil
}

// recallClosureSkip computes the copy set the staging database needs for a
// referencing-integrity-valid recall dataset: recall_requests plus its
// transitive FK parents (archive_entries, mailboxes, domains, tenants, users,
// brands, ...). Every other source table is skipped.
func recallClosureSkip(plan []TableInfo) []string {
	byName := make(map[string]TableInfo, len(plan))
	for _, ti := range plan {
		byName[ti.Name] = ti
	}
	wanted := map[string]bool{"recall_requests": true}
	queue := []string{"recall_requests"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		ti, ok := byName[name]
		if !ok {
			continue
		}
		for _, fk := range ti.FKs {
			if fk.RefTable == name || wanted[fk.RefTable] {
				continue
			}
			if _, ok := byName[fk.RefTable]; !ok {
				continue
			}
			wanted[fk.RefTable] = true
			queue = append(queue, fk.RefTable)
		}
	}
	var skip []string
	for _, ti := range plan {
		if !wanted[ti.Name] {
			skip = append(skip, ti.Name)
		}
	}
	return skip
}

// verifyStagedRecall runs the pre-publish S5 gates on the staged database:
// recall row/column zero-diff read back from the source, full schema parity,
// unique-index parity, foreign-key and page integrity.
func verifyStagedRecall(ctx context.Context, src Source, db *sql.DB, tm TypeMap, stats Stats, report *StagingReport) error {
	if diffs, err := VerifyRecallMigration(ctx, src, &Target{DB: db}); err != nil {
		return fmt.Errorf("verify recall: %w", err)
	} else if len(diffs) != 0 {
		return fmt.Errorf("recall staging diff (%d rows/columns); publish blocked", len(diffs))
	}
	if diffs, err := DiffSchema(ctx, src, &Target{DB: db}, tm); err != nil {
		return fmt.Errorf("schema diff: %w", err)
	} else if len(diffs) != 0 {
		return fmt.Errorf("schema diff (%d); publish blocked", len(diffs))
	}
	if diffs, err := DiffIndexes(ctx, src, &Target{DB: db}, tm); err != nil {
		return fmt.Errorf("index diff: %w", err)
	} else if len(diffs) != 0 {
		return fmt.Errorf("index diff (%d); publish blocked", len(diffs))
	}
	fkViolations, err := countRows(ctx, db, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	if fkViolations != 0 {
		return fmt.Errorf("foreign_key_check reported %d violation(s); publish blocked", fkViolations)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&report.Integrity); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if report.Integrity != "ok" {
		return fmt.Errorf("integrity_check=%q; publish blocked", report.Integrity)
	}
	report.SchemaDiff = 0
	report.IndexDiff = 0
	report.ForeignKeyIssues = 0
	report.Rows = stats.RecallMigrated
	report.RecallMigrated = stats.RecallMigrated
	report.RecallStatus = stats.RecallStatusCounts
	report.RecallTableHash = stats.RecallTableHash
	report.RecallGuardHash = stats.RecallGuardHash
	return nil
}

// reclaimStaleLock removes a lock left by a dead prior run, together with the
// orphan private staging file it recorded, and never touches a live lock, the
// published target, or any file outside the private naming pattern in the
// same directory.
func reclaimStaleLock(lockPath string) error {
	data, err := os.ReadFile(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read staging lock: %w", err)
	}
	var pid int
	var tmp string
	for _, field := range strings.Fields(string(data)) {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "pid":
			_, _ = fmt.Sscanf(kv[1], "%d", &pid)
		case "tmp":
			tmp = kv[1]
		}
	}
	if pid > 0 {
		if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("staging target is locked by another run (%s)", filepath.Base(lockPath))
		}
	}
	if tmp != "" && filepath.Dir(tmp) == filepath.Dir(lockPath) && strings.HasPrefix(filepath.Base(tmp), ".urmail-s5-recall-") {
		removeSQLiteFiles(tmp)
	}
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear stale staging lock: %w", err)
	}
	return nil
}

func validateTargetPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("staging target path is empty")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink staging target %s", filepath.Base(path))
		}
		return fmt.Errorf("refusing existing staging target %s: publish never overwrites", filepath.Base(path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat staging target: %w", err)
	}
	return nil
}

func countRows(ctx context.Context, db *sql.DB, query string) (int64, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("run %s: %w", query, err)
	}
	defer func() { _ = rows.Close() }() // read-only rows
	var n int64
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate %s: %w", query, err)
	}
	return n, nil
}

func removeSQLiteFiles(path string) {
	if path == "" || filepath.Clean(path) != path {
		return
	}
	if !filepath.IsAbs(path) && strings.HasPrefix(filepath.Clean(path), "..") {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open staging file for sync: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync staging file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close staging file after fsync: %w", err)
	}
	return nil
}

// cleanWithin reports whether path is already clean and does not climb out of
// base: staging targets and their sidecars are created next to the operator's
// chosen target, so a path that escapes its directory is refused.
func cleanWithin(path, base string) bool {
	if path == "" || filepath.Clean(path) != path {
		return false
	}
	root := filepath.Clean(base)
	full := filepath.Clean(filepath.Join(root, path))
	return full == root || strings.HasPrefix(full, root+string(os.PathSeparator))
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open staging directory for sync: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("fsync staging directory: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close staging directory after fsync: %w", err)
	}
	return nil
}
