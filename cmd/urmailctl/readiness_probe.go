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
	// SQLite identity: a path and user_version alone are not the target.
	Device  uint64 `json:"device,omitempty"`
	Inode   uint64 `json:"inode,omitempty"`
	Size    int64  `json:"size,omitempty"`
	MtimeNS int64  `json:"mtime_ns,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	Special uint32 `json:"special,omitempty"`
	Nlink   uint64 `json:"nlink,omitempty"`
	FileSHA string `json:"file_sha256,omitempty"`
}

type readinessProbeFile struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	Special uint32 `json:"special"`
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Nlink   uint64 `json:"nlink"`
	SHA256  string `json:"sha256"`
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
		// Struct order is fixed and json.Marshal keeps int64 identity fields
		// exact; a generic map round-trip would round nanosecond mtimes.
		raw, err := json.Marshal(doc)
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
	// Hold, receipt, requested generation and the actually connected schema
	// must agree; a hold recorded for another generation proves nothing about
	// this target and is refused instead of returning a mixed result.
	if holdMeta.Generation == 0 || receipt.Generation == 0 {
		return doc, errors.New("upgrade hold/receipt carries no schema generation; readiness probe refuses an unbound fence")
	}
	if holdMeta.Generation != receipt.Generation || holdMeta.Generation != opts.generation {
		return doc, fmt.Errorf("upgrade hold generation %d / receipt generation %d does not match requested generation %d", holdMeta.Generation, receipt.Generation, opts.generation)
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
	raw, err := json.Marshal(state)
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
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := path + suffix
		info, err := os.Lstat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return readinessProbeTarget{}, fmt.Errorf("readiness-probe: SQLite sidecar %s is an unsafe link or irregular file; refusing", filepath.Base(sidecar))
		}
		if info.Size() > 0 {
			return readinessProbeTarget{}, fmt.Errorf("readiness-probe: SQLite sidecar %s is non-empty; WAL/SHM/rollback-journal identity contract is not frozen, refusing", filepath.Base(sidecar))
		}
	}
	file, stat, err := openControlledRegular(path)
	if err != nil {
		return readinessProbeTarget{}, err
	}
	defer func() { _ = file.Close() }()
	if special := specialBits(stat.Mode); special != 0 {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe: SQLite target has unsafe special permission bits %#x; refusing", special)
	}
	if stat.Size > readinessProbeMaxFile {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe: SQLite target exceeds the size bound")
	}
	// Prove the snapshot is not being written while inspected. A watcher that
	// cannot be established means the no-concurrent-write property is unproven,
	// so the probe refuses instead of assuming it.
	watch, err := watchForWrites(path)
	if err != nil {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe: cannot prove the SQLite snapshot is not being written: %w", err)
	}
	defer watch.Close()
	// Detect WAL mode from the SQLite header before any driver opens the file:
	// a read-only driver open still creates a 32KiB -shm sidecar for WAL
	// databases, which would be a write. Without a frozen WAL contract the
	// target is refused without checkpointing, deleting or modifying anything.
	header := make([]byte, 100)
	if _, err := file.ReadAt(header, 0); err != nil && err != io.EOF {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe read SQLite header: %w", err)
	}
	if len(header) >= 20 && (header[18] == 2 || header[19] == 2) {
		return readinessProbeTarget{}, errors.New("readiness-probe: SQLite target is in WAL mode; WAL identity contract is not frozen, refusing before opening")
	}
	fileSHA, size, err := hashOpenFile(file)
	if err != nil {
		return readinessProbeTarget{}, err
	}
	// Read SQLite through /proc/self/fd/<fd>: the same inode and file
	// description content via a new descriptor, not a re-resolved path.
	db, err := sql.Open("sqlite", fmt.Sprintf("file:/proc/self/fd/%d?mode=ro", file.Fd()))
	if err != nil {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe open SQLite: %w", err)
	}
	var version uint64
	queryErr := db.QueryRow("PRAGMA user_version").Scan(&version)
	_ = db.Close()
	if queryErr != nil {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe read SQLite version: %w", queryErr)
	}
	if version != generation {
		return readinessProbeTarget{}, fmt.Errorf("readiness-probe: SQLite user_version %d does not match expected generation %d", version, generation)
	}
	// Prove the object, its full metadata, its content and its sidecars did
	// not change while read.
	var post syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &post); err != nil {
		return readinessProbeTarget{}, errors.New("readiness-probe: SQLite target metadata unreadable after inspection; refusing")
	}
	if !sameFullStat(stat, post) {
		return readinessProbeTarget{}, errors.New("readiness-probe: SQLite target metadata changed while inspected; refusing")
	}
	if again, _, err := hashOpenFile(file); err != nil || again != fileSHA {
		return readinessProbeTarget{}, errors.New("readiness-probe: SQLite target content changed while inspected; refusing")
	}
	if err := watch.NoWrites(); err != nil {
		return readinessProbeTarget{}, err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := path + suffix
		info, err := os.Lstat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
			return readinessProbeTarget{}, fmt.Errorf("readiness-probe: SQLite sidecar %s appeared or changed during inspection; refusing", filepath.Base(sidecar))
		}
	}
	// Prove the path still names the object that was hashed and read.
	after, err := os.Lstat(path)
	if err != nil {
		return readinessProbeTarget{}, errors.New("readiness-probe: SQLite target disappeared during inspection")
	}
	afterStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || !sameFullStat(stat, *afterStat) {
		return readinessProbeTarget{}, errors.New("readiness-probe: SQLite target changed while inspected; refusing")
	}
	return readinessProbeTarget{
		Kind: "sqlite", Path: path, SchemaVersion: version,
		Device: uint64(stat.Dev), Inode: stat.Ino, Size: size, MtimeNS: stat.Mtim.Nano(),
		Mode: uint32(stat.Mode & 0o7777), Special: specialBits(stat.Mode), Nlink: uint64(stat.Nlink), FileSHA: fileSHA,
	}, nil
}

func probeRequiredFile(path string) (readinessProbeFile, error) {
	if !filepath.IsAbs(path) {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: required file %q is not absolute", path)
	}
	file, stat, err := openControlledRegular(path)
	if err != nil {
		return readinessProbeFile{}, err
	}
	defer func() { _ = file.Close() }()
	if special := specialBits(stat.Mode); special != 0 {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: required file %q has disallowed special permission bits %#x; refusing", path, special)
	}
	if stat.Size > readinessProbeMaxFile {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: required file %q exceeds the size bound", path)
	}
	fileSHA, size, err := hashOpenFile(file)
	if err != nil {
		return readinessProbeFile{}, err
	}
	var post syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &post); err != nil || !sameFullStat(stat, post) {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: required file %q changed while inspected; refusing", path)
	}
	if again, _, err := hashOpenFile(file); err != nil || again != fileSHA {
		return readinessProbeFile{}, fmt.Errorf("readiness-probe: required file %q content changed while inspected; refusing", path)
	}
	return readinessProbeFile{
		Path: path, Size: size, Mode: uint32(stat.Mode & 0o7777), Special: specialBits(stat.Mode),
		UID: stat.Uid, GID: stat.Gid, Device: uint64(stat.Dev), Inode: stat.Ino, Nlink: uint64(stat.Nlink), SHA256: fileSHA,
	}, nil
}

// openControlledRegular opens exactly the named regular file without following
// links and returns its fstat identity, so every later check uses the same
// object rather than re-resolving the path.
func openControlledRegular(path string) (*os.File, syscall.Stat_t, error) {
	var stat syscall.Stat_t
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, stat, fmt.Errorf("readiness-probe: open %q: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, stat, fmt.Errorf("readiness-probe: stat %q: %w", path, err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = file.Close()
		return nil, stat, fmt.Errorf("readiness-probe: %q is not a regular file", path)
	}
	return file, stat, nil
}

// sameFullStat compares every identity and metadata field that a concurrent
// write, chmod, chown, link or replace would alter.
func sameFullStat(a, b syscall.Stat_t) bool {
	return uint64(a.Dev) == uint64(b.Dev) && a.Ino == b.Ino && a.Mode == b.Mode &&
		a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink && a.Size == b.Size &&
		a.Mtim.Nano() == b.Mtim.Nano() && a.Ctim.Nano() == b.Ctim.Nano()
}

// inotifyWatch reports whether the inspected path received any write, attrib,
// move or delete event while the probe read it.
type inotifyWatch struct{ fd int }

func watchForWrites(path string) (*inotifyWatch, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	mask := uint32(syscall.IN_MODIFY | syscall.IN_ATTRIB | syscall.IN_CLOSE_WRITE | syscall.IN_MOVE_SELF | syscall.IN_DELETE_SELF)
	if _, err := syscall.InotifyAddWatch(fd, path, mask); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return &inotifyWatch{fd: fd}, nil
}

func (w *inotifyWatch) NoWrites() error {
	if w == nil || w.fd < 0 {
		return errors.New("readiness-probe: snapshot write watch unavailable; refusing")
	}
	buf := make([]byte, 4096)
	for {
		n, err := syscall.Read(w.fd, buf)
		if errors.Is(err, syscall.EAGAIN) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("readiness-probe: snapshot write watch failed: %w", err)
		}
		if n > 0 {
			return errors.New("readiness-probe: SQLite snapshot received a write while inspected; refusing")
		}
	}
}

func (w *inotifyWatch) Close() {
	if w != nil && w.fd >= 0 {
		_ = syscall.Close(w.fd)
		w.fd = -1
	}
}

func specialBits(mode uint32) uint32 {
	return mode & (syscall.S_ISUID | syscall.S_ISGID | syscall.S_ISVTX)
}

func hashOpenFile(file *os.File) (string, int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, fmt.Errorf("readiness-probe: rewind %q: %w", file.Name(), err)
	}
	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(file, readinessProbeMaxFile+1))
	if err != nil || n > readinessProbeMaxFile {
		return "", 0, fmt.Errorf("readiness-probe: read %q: %w", file.Name(), err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), n, nil
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
