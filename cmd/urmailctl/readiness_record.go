package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/maintenance"
)

// readinessRecordVersion is the schema version of upgrade_readiness.json.
const readinessRecordVersion = 2

// defaultReleaseSigningKeyPath holds the root-only ed25519 release signing key.
const defaultReleaseSigningKeyPath = "/etc/urmail/release-signing.key"

// DefaultReadinessRecordName is the signed readiness receipt filename.
const DefaultReadinessRecordName = "upgrade_readiness.json"

// readinessFreshnessWindow bounds how long a recorded readiness receipt stays
// acceptable for a default (non break-glass) release. Injectable for tests.
var readinessFreshnessWindow = 24 * time.Hour

// Test hooks: real implementations are replaced in unit tests only.
var (
	activeAgentConfigStatus = defaultAgentConfigStatus
	activeMigrationVersion  = defaultMigrationVersion
	readinessNow            = func() time.Time { return time.Now().UTC() }
	releaseSigningKeyPath   = defaultReleaseSigningKeyPath
)

// readinessAgentSnapshot is offline, secret-free preparation evidence. Ready
// must remain false; runtime protocols are verified only after controlled start.
type readinessAgentSnapshot struct {
	Ready          bool      `json:"ready"`
	Degraded       bool      `json:"degraded"`
	Desired        string    `json:"desired_generation"`
	Applied        string    `json:"applied_generation"`
	AppliedAt      time.Time `json:"applied_at"`
	Step           string    `json:"step"`
	ManifestSHA256 string    `json:"manifest_sha256"`
}

// upgradeReadinessDoc is the canonical payload of upgrade_readiness.json.
type upgradeReadinessDoc struct {
	Version       int                     `json:"version"`
	RecordedAt    time.Time               `json:"recorded_at"`
	HoldSHA256    string                  `json:"hold_sha256"`
	ReceiptSHA256 string                  `json:"receipt_sha256"`
	Generation    uint64                  `json:"generation"`
	AgentStatus   *readinessAgentSnapshot `json:"agent_status"`
}

func defaultAgentConfigStatus(ctx context.Context) (*readinessAgentSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	prepared, err := agentd.InspectPreparedRelease(cfg)
	if err != nil {
		return nil, err
	}
	status := prepared.Status
	return &readinessAgentSnapshot{Ready: false, Degraded: status.Degraded, Desired: status.Desired, Applied: status.Applied, AppliedAt: status.AppliedAt, Step: status.Step, ManifestSHA256: prepared.ManifestSHA256}, nil
}

// defaultMigrationVersion reads the live applied schema version from the database.
func defaultMigrationVersion(_ context.Context) (uint64, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, fmt.Errorf("load config for migration version: %w", err)
	}
	if cfg == nil {
		return 0, errors.New("configuration is nil")
	}
	migrateURL := cfg.DatabaseURLMigrate
	if migrateURL == "" {
		migrateURL = cfg.DatabaseURLPlatform
	}
	if migrateURL == "" {
		return 0, errors.New("database migration URL is required")
	}
	migrator, err := db.NewMigrator(migrateURL)
	if err != nil {
		return 0, fmt.Errorf("database connection failed: %w", err)
	}
	defer func() { _ = migrator.Close() }()
	v, dirty, err := migrator.Version()
	if err != nil {
		return 0, fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return 0, fmt.Errorf("database schema is dirty at version %d", v)
	}
	return uint64(v), nil
}

// latestEmbeddedMigrationVersion derives the target schema generation from the
// embedded migrations without touching the database.
func latestEmbeddedMigrationVersion() (uint64, error) {
	entries, err := fs.ReadDir(db.FS(), ".")
	if err != nil {
		return 0, fmt.Errorf("list embedded migrations: %w", err)
	}
	re := regexp.MustCompile(`^0*(\d+)_.*\.up\.sql$`)
	var latest uint64
	for _, entry := range entries {
		m := re.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		v, parseErr := strconv.ParseUint(m[1], 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parse migration version from %s: %w", entry.Name(), parseErr)
		}
		if v > latest {
			latest = v
		}
	}
	if latest == 0 {
		return 0, errors.New("no embedded migrations found")
	}
	return latest, nil
}

// canonicalJSON produces deterministic bytes (sorted keys, no whitespace) so
// signatures cover one and only one encoding of the document.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err = json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

func openRegularPrivate(path string) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errors.New("not a regular file: " + path)
	}
	return file, info, nil
}

func writePrivateExclusive(path string, data []byte) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

// loadOrCreateReleaseSigningKey returns the root ed25519 signing key, creating
// a new key on first use. The key file must be a root-owned 0600 regular file
// holding hex(ed25519.PrivateKey) (seed+public, 128 hex chars).
func loadOrCreateReleaseSigningKey(path string) (ed25519.PrivateKey, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create signing key directory: %w", err)
		}
		_, priv, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return nil, fmt.Errorf("generate signing key: %w", genErr)
		}
		encoded := []byte(hex.EncodeToString(priv) + "\n")
		if err = writePrivateExclusive(path, encoded); err != nil {
			return nil, err
		}
	}
	file, info, err := openRegularPrivate(path)
	if err != nil {
		return nil, fmt.Errorf("open release signing key: %w", err)
	}
	defer func() { _ = file.Close() }()
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("release signing key must be 0600: %s", path)
	}
	if os.Geteuid() == 0 {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
			return nil, errors.New("release signing key must be owned by root")
		}
	}
	raw, err := readPrivateBoundedFrom(file, 4096)
	if err != nil {
		return nil, err
	}
	decoded, decodeErr := hex.DecodeString(strings.TrimSpace(string(raw)))
	if decodeErr != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("release signing key must be hex ed25519 private key (128 hex chars)")
	}
	priv := ed25519.PrivateKey(decoded)
	if !priv.Equal(ed25519.NewKeyFromSeed(priv.Seed())) {
		return nil, errors.New("release signing key public half does not match seed")
	}
	return priv, nil
}

func runUpgradeReadinessRecord(ctx context.Context, args []string) error {
	if err := checkUpgradePrivileges(); err != nil {
		return err
	}

	flags := flag.NewFlagSet("upgrade-readiness-record", flag.ContinueOnError)
	generation := flags.Uint64("generation", 0, "recorded schema generation (default: live applied version; must match it)")
	keyPath := flags.String("key", releaseSigningKeyPath, "root ed25519 release signing key path")
	replace := flags.Bool("replace", false, "atomically replace an existing readiness record (tmp+rename, signature first)")
	timeout := flags.Duration("timeout", 30*time.Second, "timeout waiting for operation lock")
	rootDir := flags.String("root-dir", currentMaintenanceRoot(), "maintenance root directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: upgrade readiness-record [--generation N] [--key PATH]")
	}

	// Bind to a quiescent operation: no concurrent backup/restore/upgrade work.
	lock, err := maintenance.AcquireLockWithTimeout(ctx, *rootDir, maintenance.DefaultLockName, *timeout)
	if err != nil {
		return fmt.Errorf("acquire operation lock: %w", err)
	}
	defer func() { _ = maintenance.ReleaseLock(lock) }()

	// 1. Live fence must be active; bind to its exact bytes.
	holdRaw, receiptRaw, err := maintenance.ReadUpgradeFenceRaw(*rootDir)
	if err != nil {
		return fmt.Errorf("read live upgrade fence: %w", err)
	}
	var holdMeta maintenance.UpgradeFenceMeta
	var receipt maintenance.UpgradeReceipt
	if err = json.Unmarshal(holdRaw, &holdMeta); err != nil {
		return fmt.Errorf("parse upgrade hold: %w", err)
	}
	if err = json.Unmarshal(receiptRaw, &receipt); err != nil {
		return fmt.Errorf("parse upgrade receipt: %w", err)
	}
	if holdMeta.Version < 2 || receipt.Version < 2 {
		return errors.New("live upgrade fence is not v2; re-establish with 'urmailctl upgrade fence' before recording readiness")
	}
	holdSum := sha256Sum(holdRaw)
	receiptSum := sha256Sum(receiptRaw)
	if receipt.HoldSHA256 != holdSum {
		return fmt.Errorf("upgrade receipt does not bind live hold bytes (receipt hold_sha256 %s != live %s)", receipt.HoldSHA256, holdSum)
	}
	if !holdMeta.CreatedAt.Equal(receipt.CreatedAt) {
		return errors.New("upgrade hold and receipt created_at mismatch")
	}

	// 2. Verify offline preparation; do not require a fenced agent or claim runtime readiness.
	agentStatus, err := activeAgentConfigStatus(ctx)
	if err != nil {
		return fmt.Errorf("agent readiness: %w", err)
	}
	if !validPreparedEvidence(agentStatus) {
		return fmt.Errorf("configuration is not ready/converged: ready=%t degraded=%t desired=%q applied=%q step=%q",
			agentStatus.Ready, agentStatus.Degraded, agentStatus.Desired, agentStatus.Applied, agentStatus.Step)
	}

	// 3. Schema generation: live applied version is authoritative; a
	// --generation override must match it exactly (drift is fail-closed).
	liveVersion, err := activeMigrationVersion(ctx)
	if err != nil {
		return fmt.Errorf("live migration version: %w", err)
	}
	recorded := liveVersion
	if *generation != 0 {
		if *generation != liveVersion {
			return fmt.Errorf("requested generation %d does not match live applied version %d (generation drift, fence retained)", *generation, liveVersion)
		}
		recorded = *generation
	}
	if liveVersion == 0 {
		return errors.New("live migration version is 0; refuse to record readiness without an applied schema (fence retained)")
	}
	targetVersion, targetErr := latestEmbeddedMigrationVersion()
	if targetErr == nil && liveVersion < targetVersion {
		return fmt.Errorf("live schema version %d is behind embedded target %d; apply migrations before recording readiness", liveVersion, targetVersion)
	}

	// 4. Build, sign and persist the root-only readiness receipt.
	doc := upgradeReadinessDoc{
		Version:       readinessRecordVersion,
		RecordedAt:    readinessNow().UTC(),
		HoldSHA256:    holdSum,
		ReceiptSHA256: receiptSum,
		Generation:    recorded,
		AgentStatus:   agentStatus,
	}
	payload, err := canonicalJSON(doc)
	if err != nil {
		return fmt.Errorf("canonicalize readiness record: %w", err)
	}
	key, err := loadOrCreateReleaseSigningKey(*keyPath)
	if err != nil {
		return fmt.Errorf("load release signing key: %w", err)
	}
	signature := ed25519.Sign(key, payload)

	recordPath := filepath.Join(maintenance.Root(*rootDir), DefaultReadinessRecordName)
	writeRecord := func(path string, data []byte) error {
		if !*replace {
			return writePrivateExclusive(path, data)
		}
		// Atomic replace: clear any stale tmp left by a crashed prior replace,
		// write a private tmp, then rename over the target.
		tmp := path + ".replace.tmp"
		_ = os.Remove(tmp)
		if tmpErr := writePrivateExclusive(tmp, data); tmpErr != nil {
			_ = os.Remove(tmp)
			return tmpErr
		}
		if renameErr := os.Rename(tmp, path); renameErr != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("replace %s: %w", path, renameErr)
		}
		return nil
	}
	if *replace {
		// Signature is renamed FIRST: a mid-window reader sees the new payload
		// with the old signature and verify fails closed (acceptable); the
		// final state is always consistent.
		if err = writeRecord(recordPath+".sig", []byte(hex.EncodeToString(signature)+"\n")); err != nil {
			return err
		}
		if err = writeRecord(recordPath, payload); err != nil {
			return err
		}
	} else {
		if err = writeRecord(recordPath, payload); err != nil {
			return err
		}
		if err = writeRecord(recordPath+".sig", []byte(hex.EncodeToString(signature)+"\n")); err != nil {
			return err
		}
	}
	if err = maintenance.SyncDirectory(maintenance.Root(*rootDir)); err != nil {
		return err
	}

	fmt.Println("Offline release preparation recorded (signed); runtime readiness is NOT established.")
	fmt.Printf("Receipt:     %s\n", recordPath)
	fmt.Printf("Generation:  %d\n", recorded)
	fmt.Printf("Hold bind:   %s\n", holdSum)
	fmt.Println("Release with 'urmailctl upgrade release' (no break-glass needed).")
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validPreparedEvidence(status *readinessAgentSnapshot) bool {
	return status != nil && !status.Ready && !status.Degraded && status.Step == "prepared" && status.Desired != "" && validSHA256(status.ManifestSHA256)
}

// activeLiveAgentStatus reports the RUNNING agent's readiness over its socket.
// Injectable for tests; the release path uses it to close the prepared-stuck
// receipt gap observed in production (v0.6.0 receipt never left "prepared"
// even after the agent reported ready).
var activeLiveAgentStatus = defaultLiveAgentStatus

func defaultLiveAgentStatus(ctx context.Context) (*readinessAgentSnapshot, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config for live agent status: %w", err)
	}
	client := agent.NewClient(cfg.AgentSocket)
	resp, err := client.Do(ctx, agentd.OpConfigStatus, struct{}{})
	if err != nil {
		return nil, fmt.Errorf("query live agent status: %w", err)
	}
	var status agentd.ApplyStatus
	if err = json.Unmarshal([]byte(resp.Stdout), &status); err != nil {
		return nil, fmt.Errorf("parse live agent status: %w", err)
	}
	return &readinessAgentSnapshot{
		Ready:     status.Ready,
		Degraded:  status.Degraded,
		Desired:   status.Desired,
		Applied:   status.Applied,
		AppliedAt: status.AppliedAt,
		Step:      status.Step,
	}, nil
}

// rewriteReadinessForRelease atomically re-signs the readiness receipt so it
// binds the live, ready agent snapshot instead of the offline prepared one.
// Fence hold/receipt digests are kept as historical bindings of the fence that
// was just released. The signature is renamed first (matching --replace
// semantics), so a crash mid-rewrite leaves a verify-fails-closed state, never
// an unsigned or half-signed receipt.
func rewriteReadinessForRelease(rootDir, keyPath string, doc *upgradeReadinessDoc, live *readinessAgentSnapshot) error {
	if doc == nil || live == nil {
		return nil
	}
	if doc.AgentStatus == nil || doc.AgentStatus.Ready || !live.Ready || live.Degraded ||
		live.Desired == "" || live.Desired != live.Applied || live.Step == "" ||
		live.Desired != doc.AgentStatus.Desired {
		return nil
	}
	updated := *doc
	snapshot := *live
	updated.AgentStatus = &snapshot
	updated.RecordedAt = readinessNow().UTC()
	payload, err := canonicalJSON(updated)
	if err != nil {
		return fmt.Errorf("canonicalize rewritten readiness record: %w", err)
	}
	key, err := loadOrCreateReleaseSigningKey(keyPath)
	if err != nil {
		return fmt.Errorf("load release signing key: %w", err)
	}
	signature := ed25519.Sign(key, payload)
	recordPath := filepath.Join(maintenance.Root(rootDir), DefaultReadinessRecordName)
	tmp := recordPath + ".replace.tmp"
	_ = os.Remove(tmp)
	if err = writePrivateExclusive(tmp, []byte(hex.EncodeToString(signature)+"\n")); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("stage rewritten readiness signature: %w", err)
	}
	if err = os.Rename(tmp, recordPath+".sig"); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install rewritten readiness signature: %w", err)
	}
	tmp = recordPath + ".replace.tmp"
	_ = os.Remove(tmp)
	if err = writePrivateExclusive(tmp, payload); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("stage rewritten readiness receipt: %w", err)
	}
	if err = os.Rename(tmp, recordPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install rewritten readiness receipt: %w", err)
	}
	if err = maintenance.SyncDirectory(maintenance.Root(rootDir)); err != nil {
		return err
	}
	fmt.Println("Readiness receipt re-signed with the live ready agent snapshot (no longer 'prepared').")
	return nil
}

func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// verifyReadinessForRelease validates the signed readiness receipt against the
// live fence bytes (bidirectional digests), the live schema generation and the
// freshness window. It collects ALL failures so the operator sees the complete
// list of missing or invalid evidence.
func verifyReadinessForRelease(ctx context.Context, rootDir, keyPath string) (*upgradeReadinessDoc, error) {
	rootDir = maintenance.Root(rootDir)
	var problems []string

	payload, payloadErr := readPrivateBounded(filepath.Join(rootDir, DefaultReadinessRecordName), 65536)
	if payloadErr != nil {
		problems = append(problems, fmt.Sprintf("read readiness receipt: %v", payloadErr))
	}
	sigRaw, sigErr := readPrivateBounded(filepath.Join(rootDir, DefaultReadinessRecordName+".sig"), 4096)
	if sigErr != nil {
		problems = append(problems, fmt.Sprintf("read readiness signature: %v", sigErr))
	}

	var doc upgradeReadinessDoc
	if payloadErr == nil && sigErr == nil {
		if err := json.Unmarshal(payload, &doc); err != nil {
			problems = append(problems, fmt.Sprintf("parse readiness receipt: %v", err))
		} else {
			canonical, canonErr := canonicalJSON(doc)
			if canonErr != nil || string(canonical) != string(payload) {
				problems = append(problems, "readiness receipt is not canonical JSON")
			}
			if doc.Version != readinessRecordVersion {
				problems = append(problems, fmt.Sprintf("unsupported readiness receipt version %d", doc.Version))
			}
			decoded, decodeErr := hex.DecodeString(strings.TrimSpace(string(sigRaw)))
			if decodeErr != nil || len(decoded) != ed25519.SignatureSize {
				problems = append(problems, "readiness signature is malformed")
			} else {
				key, keyErr := loadOrCreateReleaseSigningKey(keyPath)
				if keyErr != nil {
					problems = append(problems, fmt.Sprintf("load release signing key: %v", keyErr))
				} else if !ed25519.Verify(key.Public().(ed25519.PublicKey), payload, decoded) {
					problems = append(problems, "readiness signature verification FAILED (forged or foreign key)")
				}
			}
		}
	}

	// Bind to the exact live fence bytes (bidirectional digests + created_at).
	holdRaw, receiptRaw, fenceErr := maintenance.ReadUpgradeFenceRaw(rootDir)
	if fenceErr != nil {
		problems = append(problems, fmt.Sprintf("read live upgrade fence: %v", fenceErr))
	} else {
		holdSum, receiptSum := sha256Sum(holdRaw), sha256Sum(receiptRaw)
		if !validSHA256(doc.HoldSHA256) || doc.HoldSHA256 != holdSum {
			problems = append(problems, fmt.Sprintf("hold digest mismatch (receipt %s != live %s)", doc.HoldSHA256, holdSum))
		}
		if !validSHA256(doc.ReceiptSHA256) || doc.ReceiptSHA256 != receiptSum {
			problems = append(problems, fmt.Sprintf("receipt digest mismatch (receipt %s != live %s)", doc.ReceiptSHA256, receiptSum))
		}
		var receipt maintenance.UpgradeReceipt
		var holdMeta maintenance.UpgradeFenceMeta
		if json.Unmarshal(receiptRaw, &receipt) != nil || json.Unmarshal(holdRaw, &holdMeta) != nil {
			problems = append(problems, "parse live upgrade fence JSON")
		} else {
			if receipt.Version < 2 || holdMeta.Version < 2 {
				problems = append(problems, "live upgrade fence is not v2")
			}
			if receipt.HoldSHA256 != holdSum {
				problems = append(problems, "live receipt does not bind live hold bytes")
			}
			if !holdMeta.CreatedAt.Equal(receipt.CreatedAt) {
				problems = append(problems, "live hold and receipt created_at mismatch")
			}
		}
	}

	// Generation must match the live database schema version exactly.
	liveVersion, versionErr := activeMigrationVersion(ctx)
	if versionErr != nil {
		problems = append(problems, fmt.Sprintf("live migration version: %v", versionErr))
	} else {
		if liveVersion == 0 {
			problems = append(problems, "live migration version is 0 (no applied schema)")
		}
		if doc.Generation == 0 {
			problems = append(problems, "readiness receipt has no recorded generation")
		} else if doc.Generation != liveVersion {
			problems = append(problems, fmt.Sprintf("generation drift (receipt %d != live %d)", doc.Generation, liveVersion))
		}
	}

	// Freshness window (injectable for tests) and bound agent evidence.
	if !validPreparedEvidence(doc.AgentStatus) {
		problems = append(problems, "bound offline preparation status is invalid")
	} else {
		age := readinessNow().Sub(doc.RecordedAt)
		if age > readinessFreshnessWindow {
			problems = append(problems, fmt.Sprintf("readiness receipt is stale (age %s > window %s)", age.Truncate(time.Second), readinessFreshnessWindow))
		}
		if age < -5*time.Minute {
			problems = append(problems, "readiness receipt recorded_at is in the future")
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("cannot release upgrade fence: signed readiness receipt verification failed (upgrade fence retained); missing/invalid:\n - %s\nComplete readiness with 'urmailctl upgrade readiness-record', or release explicitly with --break-glass --ack-risk",
			strings.Join(problems, "\n - "))
	}
	return &doc, nil
}

func readPrivateBounded(path string, maxBytes int64) ([]byte, error) {
	file, _, err := openRegularPrivate(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return readPrivateBoundedFrom(file, maxBytes)
}

func readPrivateBoundedFrom(file *os.File, maxBytes int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(bufio.NewReader(file), maxBytes))
}
