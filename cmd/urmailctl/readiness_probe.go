package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	_ "modernc.org/sqlite"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

// readinessProbeFormat is the canonical probe payload version. It is separate
// from upgrade_readiness.json: this probe never writes, never signs and never
// claims agent/runtime readiness. The authorized signer consumes its bindings.
const readinessProbeFormat = "urmail-readiness-probe-v1"

// readinessProbeMaxFile bounds every read-only input the probe inspects.
const readinessProbeMaxFile = 64 << 20

type readinessProbeHold struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	ReceiptPath   string `json:"receipt_path"`
	ReceiptSHA256 string `json:"receipt_sha256"`
	CreatedAt     string `json:"created_at"`
}

type readinessProbeTarget struct {
	Kind                string `json:"kind"`
	Database            string `json:"database"`
	DatabaseOID         string `json:"database_oid,omitempty"`
	User                string `json:"user,omitempty"`
	Path                string `json:"path,omitempty"`
	SchemaVersion       uint64 `json:"schema_version"`
	Dirty               bool   `json:"dirty"`
	TransactionReadOnly string `json:"transaction_read_only,omitempty"`
}

type readinessProbeFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type readinessProbeDoc struct {
	Format         string               `json:"format"`
	ProbedAt       time.Time            `json:"probed_at"`
	SourceRevision string               `json:"source_revision"`
	Hold           readinessProbeHold   `json:"hold"`
	Target         readinessProbeTarget `json:"target"`
	RequiredFiles  []readinessProbeFile `json:"required_files"`
	Generation     uint64               `json:"generation"`
	StateSHA256    string               `json:"state_sha256"`
}

type readinessProbeOptions struct {
	rootDir       string
	generation    uint64
	databaseEnv   string
	sqlitePath    string
	sqliteVersion uint64
	requiredFiles []string
}

// runUpgradeReadinessProbe is registered as "urmailctl upgrade readiness-probe".
func runUpgradeReadinessProbe(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("upgrade-readiness-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rootDir := flags.String("root-dir", currentMaintenanceRoot(), "maintenance root directory holding the live hold")
	generation := flags.Uint64("generation", 0, "expected applied schema generation (required)")
	databaseEnv := flags.String("database-url-env", "URMAIL_READINESS_DATABASE_URL_RO", "environment variable holding a read-only PostgreSQL URL")
	sqlitePath := flags.String("sqlite", "", "optional SQLite target path (read-only)")
	sqliteVersion := flags.Uint64("sqlite-generation", 0, "expected SQLite user_version when --sqlite is set")
	required := multiFlag{}
	flags.Var(&required, "required-file", "absolute required file whose digest is bound (repeatable)")
	verify := flags.String("verify", "", "path of a previous probe record to re-check; no record is written")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: urmailctl upgrade readiness-probe --generation N [--root-dir DIR] [--database-url-env NAME] [--sqlite PATH --sqlite-generation N] [--required-file PATH ...] [--verify RECORD]")
	}
	if *generation == 0 {
		return errors.New("readiness-probe requires --generation from the authorized upgrade plan")
	}
	opts := readinessProbeOptions{rootDir: *rootDir, generation: *generation, databaseEnv: *databaseEnv, sqlitePath: *sqlitePath, sqliteVersion: *sqliteVersion, requiredFiles: required}
	if opts.sqlitePath != "" && opts.sqliteVersion == 0 {
		return errors.New("readiness-probe requires --sqlite-generation with --sqlite")
	}
	doc, err := probeReadiness(ctx, opts)
	if err != nil {
		return err
	}
	if *verify == "" {
		raw, err := canonicalJSON(doc)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(raw))
		return err
	}
	if err := verifyReadinessProbe(ctx, opts, *verify, doc); err != nil {
		return err
	}
	fmt.Printf("MATCH state_sha256=%s generation=%d hold_sha256=%s\n", doc.StateSHA256, doc.Generation, doc.Hold.SHA256)
	return nil
}

// probeReadiness performs only read-only inspections: the live fence files,
// exactly one clean schema row from the actually connected restore database,
// and digests of the required files. It opens no service, agent, socket or
// migration handle and never writes to the targets.
func probeReadiness(ctx context.Context, opts readinessProbeOptions) (readinessProbeDoc, error) {
	doc := readinessProbeDoc{Format: readinessProbeFormat, ProbedAt: time.Now().UTC(), SourceRevision: buildRevision()}
	holdRaw, receiptRaw, err := maintenance.ReadUpgradeFenceRaw(opts.rootDir)
	if err != nil {
		return doc, fmt.Errorf("read live upgrade fence: %w", err)
	}
	var holdMeta maintenance.UpgradeFenceMeta
	var receipt maintenance.UpgradeReceipt
	if err := json.Unmarshal(holdRaw, &holdMeta); err != nil {
		return doc, fmt.Errorf("parse upgrade hold: %w", err)
	}
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
		return doc, fmt.Errorf("parse upgrade receipt: %w", err)
	}
	if holdMeta.Version < 2 || receipt.Version < 2 {
		return doc, errors.New("live upgrade fence is not v2; readiness probe refuses a legacy fence")
	}
	holdSHA, receiptSHA := sha256Sum(holdRaw), sha256Sum(receiptRaw)
	if receipt.HoldSHA256 != holdSHA {
		return doc, fmt.Errorf("upgrade receipt does not bind live hold bytes (receipt %s != live %s)", receipt.HoldSHA256, holdSHA)
	}
	if !holdMeta.CreatedAt.Equal(receipt.CreatedAt) {
		return doc, errors.New("upgrade hold and receipt created_at mismatch")
	}
	doc.Hold = readinessProbeHold{
		Path:          filepath.Join(maintenance.Root(opts.rootDir), maintenance.DefaultUpgradeHoldFile),
		SHA256:        holdSHA,
		ReceiptPath:   filepath.Join(maintenance.Root(opts.rootDir), maintenance.DefaultUpgradeReceiptName),
		ReceiptSHA256: receiptSHA,
		CreatedAt:     holdMeta.CreatedAt.UTC().Format(time.RFC3339Nano),
	}

	switch {
	case opts.sqlitePath != "":
		target, err := probeSQLite(opts.sqlitePath, opts.sqliteVersion)
		if err != nil {
			return doc, err
		}
		doc.Target = target
	case os.Getenv(opts.databaseEnv) != "":
		target, err := probePostgres(ctx, os.Getenv(opts.databaseEnv), opts.generation)
		if err != nil {
			return doc, err
		}
		doc.Target = target
	default:
		return doc, fmt.Errorf("readiness-probe requires --sqlite or a read-only URL in %s", opts.databaseEnv)
	}
	doc.Generation = opts.generation

	for _, path := range opts.requiredFiles {
		file, err := probeRequiredFile(path)
		if err != nil {
			return doc, err
		}
		doc.RequiredFiles = append(doc.RequiredFiles, file)
	}
	sort.Slice(doc.RequiredFiles, func(i, j int) bool { return doc.RequiredFiles[i].Path < doc.RequiredFiles[j].Path })

	state := doc
	state.ProbedAt = time.Time{}
	state.StateSHA256 = ""
	raw, err := canonicalJSON(state)
	if err != nil {
		return doc, err
	}
	doc.StateSHA256 = sha256Sum(raw)
	return doc, nil
}

func probePostgres(ctx context.Context, url string, generation uint64) (readinessProbeTarget, error) {
	var target readinessProbeTarget
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return target, errors.New("readiness-probe: invalid PostgreSQL URL")
	}
	// The probe enforces a read-only session: it cannot migrate or write even
	// if the supplied credential happens to carry wider grants.
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["application_name"] = "urmail-readiness-probe"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return target, fmt.Errorf("readiness-probe connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var readOnly string
	if err := conn.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		return target, fmt.Errorf("readiness-probe could not enforce a read-only session (read_only=%q err=%v)", readOnly, err)
	}
	var version uint64
	var dirty bool
	count := 0
	schemaRows, err := conn.Query(ctx, "SELECT version, dirty FROM schema_migrations")
	if err != nil {
		return target, fmt.Errorf("readiness-probe read schema version: %w", err)
	}
	for schemaRows.Next() {
		count++
		if count > 1 {
			schemaRows.Close()
			return target, errors.New("readiness-probe: schema_migrations is ambiguous (more than one row)")
		}
		if err := schemaRows.Scan(&version, &dirty); err != nil {
			schemaRows.Close()
			return target, err
		}
	}
	if err := schemaRows.Err(); err != nil {
		schemaRows.Close()
		return target, fmt.Errorf("readiness-probe read schema version: %w", err)
	}
	schemaRows.Close()
	if count == 0 {
		return target, errors.New("readiness-probe: schema_migrations is empty")
	}
	if dirty {
		return target, fmt.Errorf("readiness-probe: schema version %d is dirty", version)
	}
	if version != generation {
		return target, fmt.Errorf("readiness-probe: live schema version %d does not match expected generation %d", version, generation)
	}
	var dbName, dbOID, user string
	if err := conn.QueryRow(ctx, `SELECT current_database(), (SELECT oid::text FROM pg_database WHERE datname=current_database()), current_user`).Scan(&dbName, &dbOID, &user); err != nil {
		return target, fmt.Errorf("readiness-probe read target identity: %w", err)
	}
	return readinessProbeTarget{Kind: "postgres", Database: dbName, DatabaseOID: dbOID, User: user, SchemaVersion: version, Dirty: dirty, TransactionReadOnly: readOnly}, nil
}

func probeSQLite(path string, generation uint64) (readinessProbeTarget, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return readinessProbeTarget{}, errors.New("readiness-probe: SQLite target is missing or not a regular file")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe open SQLite: %w", err)
	}
	defer func() { _ = db.Close() }()
	var version uint64
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe read SQLite version: %w", err)
	}
	if version != generation {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe: SQLite user_version %d does not match expected generation %d", version, generation)
	}
	return readinessProbeTarget{Kind: "sqlite", Path: path, SchemaVersion: version}, nil
}

func probeRequiredFile(path string) (readinessProbeFile, error) {
	if !filepath.IsAbs(path) {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: required file %q is not absolute", path)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: required file %q is missing or not a regular file", path)
	}
	if info.Size() > readinessProbeMaxFile {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: required file %q exceeds the size bound", path)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: open required file %q: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(file, readinessProbeMaxFile+1))
	if err != nil || n > readinessProbeMaxFile {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: read required file %q: %w", path, err)
	}
	return readinessProbeFile{Path: path, Size: n, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func verifyReadinessProbe(ctx context.Context, opts readinessProbeOptions, recordPath string, live readinessProbeDoc) error {
	info, err := os.Lstat(recordPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("readiness-probe verify: record is missing, not regular or too large")
	}
	raw, err := os.ReadFile(recordPath) //nolint:gosec // Operator-supplied verification path.
	if err != nil {
		return fmt.Errorf("readiness-probe verify: read record: %w", err)
	}
	var recorded readinessProbeDoc
	if err := json.Unmarshal(raw, &recorded); err != nil {
		return fmt.Errorf("readiness-probe verify: parse record: %w", err)
	}
	if recorded.Format != readinessProbeFormat {
		return fmt.Errorf("readiness-probe verify: unsupported record format %q", recorded.Format)
	}
	var mismatches []string
	if recorded.Generation != live.Generation {
		mismatches = append(mismatches, fmt.Sprintf("generation %d != live %d", recorded.Generation, live.Generation))
	}
	if recorded.Hold.SHA256 != live.Hold.SHA256 || recorded.Hold.ReceiptSHA256 != live.Hold.ReceiptSHA256 {
		mismatches = append(mismatches, "hold or receipt bytes changed")
	}
	if recorded.Target != live.Target {
		mismatches = append(mismatches, "target identity or schema state changed")
	}
	if len(recorded.RequiredFiles) != len(live.RequiredFiles) {
		mismatches = append(mismatches, "required file set changed")
	} else {
		for i := range recorded.RequiredFiles {
			if recorded.RequiredFiles[i] != live.RequiredFiles[i] {
				mismatches = append(mismatches, "required file "+recorded.RequiredFiles[i].Path+" changed")
			}
		}
	}
	if recorded.StateSHA256 != live.StateSHA256 {
		mismatches = append(mismatches, "state_sha256 differs")
	}
	if len(mismatches) != 0 {
		sort.Strings(mismatches)
		return fmt.Errorf("readiness-probe verify: STATE_CHANGED: %s", strings.Join(mismatches, "; "))
	}
	return nil
}

func buildRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return "unknown"
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
