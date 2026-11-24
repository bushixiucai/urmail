package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

// readinessAttestFormat is the fixed payload an independent signer signs over
// a live probe. Verification always re-runs the probe; the caller's
// state_sha256 alone is never trusted.
const readinessAttestFormat = "urmail-readiness-attest-v1"

type readinessAttestPayload struct {
	Format              string               `json:"format"`
	IssuedAt            time.Time            `json:"issued_at"`
	ProbeSourceRevision string               `json:"probe_source_revision"`
	HoldSHA256          string               `json:"hold_sha256"`
	ReceiptSHA256       string               `json:"receipt_sha256"`
	HoldCreatedAt       string               `json:"hold_created_at"`
	Generation          uint64               `json:"generation"`
	ProbeStateSHA256    string               `json:"probe_state_sha256"`
	Target              readinessProbeTarget `json:"target"`
	RequiredFiles       []readinessProbeFile `json:"required_files"`
}

// runUpgradeReadinessAttest dispatches the independent sign/verify path.
func runUpgradeReadinessAttest(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: urmailctl upgrade readiness-attest sign|verify [flags]")
	}
	switch args[0] {
	case "sign":
		return runReadinessAttestSign(ctx, args[1:])
	case "verify":
		return runReadinessAttestVerify(ctx, args[1:])
	case "consume":
		return runReadinessAttestConsume(ctx, args[1:])
	default:
		return fmt.Errorf("unknown readiness-attest subcommand %q", args[0])
	}
}

// readinessAttestFlags carries the probe inputs shared by sign and verify plus
// the attestation-specific paths. The signer and verifier are separate
// processes; neither starts a service or writer.
type readinessAttestFlags struct {
	opts       readinessProbeOptions
	probePath  string
	attestPath string
	keyPath    string
	pubKeyPath string
	requestID  string
}

func parseReadinessAttestFlags(setName string, args []string) (readinessAttestFlags, error) {
	var out readinessAttestFlags
	flags := flag.NewFlagSet(setName, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rootDir := flags.String("root-dir", currentMaintenanceRoot(), "maintenance root directory holding the live hold")
	generation := flags.Uint64("generation", 0, "expected applied schema generation (required)")
	databaseEnv := flags.String("database-url-env", "URMAIL_READINESS_DATABASE_URL_RO", "environment variable holding a read-only PostgreSQL URL")
	sqlitePath := flags.String("sqlite", "", "optional SQLite target path (read-only)")
	sqliteVersion := flags.Uint64("sqlite-generation", 0, "expected SQLite user_version when --sqlite is set")
	lockFD := flags.Int("lock-fd", -1, "inherited exclusive operation-lock descriptor (formal inputs; required)")
	required := multiFlag{}
	flags.Var(&required, "required-file", "absolute required file whose digest is bound (repeatable)")
	probe := flags.String("probe", "", "readiness probe record to bind (required for sign)")
	attest := flags.String("attest", "", "signed attestation payload (required for verify)")
	key := flags.String("key", releaseSigningKeyPath, "existing ed25519 release signing key (never created)")
	pub := flags.String("pubkey", "", "test-only anchor override in tagged builds; the product uses a fixed trusted anchor")
	requestID := flags.String("request-id", os.Getenv("URMAIL_READINESS_REQUEST_ID"), "one-time attempt identity for consume")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return out, errors.New("usage: upgrade readiness-attest sign|verify --generation N --root-dir DIR [--database-url-env NAME|--sqlite PATH] [--required-file PATH ...] [--probe FILE] [--attest FILE] [--key PATH|--pubkey PATH]")
	}
	if *generation == 0 {
		return out, errors.New("readiness-attest requires --generation")
	}
	if *sqlitePath != "" && *sqliteVersion == 0 {
		return out, errors.New("readiness-attest requires --sqlite-generation with --sqlite")
	}
	out.opts = readinessProbeOptions{rootDir: *rootDir, generation: *generation, databaseEnv: *databaseEnv, sqlitePath: *sqlitePath, sqliteVersion: *sqliteVersion, requiredFiles: required, requireLock: true, lockFD: *lockFD}
	out.probePath, out.attestPath, out.keyPath, out.pubKeyPath = *probe, *attest, *key, *pub
	out.requestID = *requestID
	return out, nil
}

func readinessAttestFromDoc(doc readinessProbeDoc, issued time.Time) readinessAttestPayload {
	return readinessAttestPayload{
		Format: readinessAttestFormat, IssuedAt: issued.UTC(), ProbeSourceRevision: doc.SourceRevision,
		HoldSHA256: doc.Hold.SHA256, ReceiptSHA256: doc.Hold.ReceiptSHA256, HoldCreatedAt: doc.Hold.CreatedAt,
		Generation: doc.Generation, ProbeStateSHA256: doc.StateSHA256,
		Target: doc.Target, RequiredFiles: doc.RequiredFiles,
	}
}

// runReadinessAttestSign re-runs the probe with the requested inputs and only
// signs when the supplied probe record matches the live state exactly. The
// private key must already exist (0600); this command never creates one.
func runReadinessAttestSign(ctx context.Context, args []string) error {
	f, err := parseReadinessAttestFlags("upgrade-readiness-attest-sign", args)
	if err != nil {
		return err
	}
	if f.probePath == "" {
		return errors.New("readiness-attest sign requires --probe FILE")
	}
	recorded, err := loadProbeRecord(f.probePath)
	if err != nil {
		return err
	}
	live, err := probeReadiness(ctx, f.opts)
	if err != nil {
		return err
	}
	if err := compareProbeToLive(recorded, live); err != nil {
		return fmt.Errorf("readiness-attest sign: probe does not match live state: %w", err)
	}
	key, err := loadExistingReleaseKey(f.keyPath)
	if err != nil {
		return err
	}
	payload := readinessAttestFromDoc(live, time.Now().UTC())
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	signature := ed25519.Sign(key, raw)
	attestRaw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if f.attestPath == "" {
		f.attestPath = filepath.Join(f.opts.rootDir, "upgrade_readiness_attest.json")
	}
	if err := writePrivateExclusive(f.attestPath, attestRaw); err != nil {
		return err
	}
	if err := writePrivateExclusive(f.attestPath+".sig", []byte(hex.EncodeToString(signature)+"\n")); err != nil {
		_ = os.Remove(f.attestPath)
		return err
	}
	fmt.Printf("SIGNED attest=%s state_sha256=%s generation=%d\n", f.attestPath, payload.ProbeStateSHA256, payload.Generation)
	return nil
}

// runReadinessAttestVerify re-runs the probe with the same inputs, verifies the
// pinned-anchor signature, freshness and every binding, and refuses on missing,
// wrong-signature, stale or changed state. It is safe to call before clearing
// the hold; it opens no writer.
func runReadinessAttestVerify(ctx context.Context, args []string) error {
	f, err := parseReadinessAttestFlags("upgrade-readiness-attest-verify", args)
	if err != nil {
		return err
	}
	if f.attestPath == "" {
		f.attestPath = filepath.Join(f.opts.rootDir, "upgrade_readiness_attest.json")
	}
	payload, err := verifyReadinessAttestation(ctx, f)
	if err != nil {
		return err
	}
	fmt.Printf("VALID attest=%s state_sha256=%s generation=%d\n", f.attestPath, payload.ProbeStateSHA256, payload.Generation)
	return nil
}

// verifyReadinessAttestation performs the fixed-anchor signature, freshness
// and live binding checks shared by verify and consume.
func verifyReadinessAttestation(ctx context.Context, f readinessAttestFlags) (readinessAttestPayload, error) {
	var payload readinessAttestPayload
	pub, err := readinessVerifyAnchor(f.pubKeyPath)
	if err != nil {
		return payload, err
	}
	payloadRaw, err := readBoundedRegular(f.attestPath, 1<<20)
	if err != nil {
		return payload, fmt.Errorf("readiness-attest verify: %w", err)
	}
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return payload, fmt.Errorf("readiness-attest verify: parse attestation: %w", err)
	}
	if payload.Format != readinessAttestFormat {
		return payload, fmt.Errorf("readiness-attest verify: unsupported format %q", payload.Format)
	}
	now := time.Now().UTC()
	if payload.IssuedAt.After(now.Add(time.Minute)) || now.Sub(payload.IssuedAt) > readinessFreshnessWindow {
		return payload, errors.New("readiness-attest verify: attestation is expired or not yet valid")
	}
	sigRaw, err := readBoundedRegular(f.attestPath+".sig", 4096)
	if err != nil {
		return payload, fmt.Errorf("readiness-attest verify: %w", err)
	}
	signature, err := hex.DecodeString(strings.TrimSpace(string(sigRaw)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return payload, errors.New("readiness-attest verify: signature is malformed")
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return payload, err
	}
	if !ed25519.Verify(pub, canonical, signature) {
		return payload, errors.New("readiness-attest verify: signature does not verify against the pinned anchor")
	}
	live, err := probeReadiness(ctx, f.opts)
	if err != nil {
		return payload, err
	}
	livePayload := readinessAttestFromDoc(live, payload.IssuedAt)
	if err := compareAttestToLive(payload, livePayload); err != nil {
		return payload, fmt.Errorf("readiness-attest verify: STATE_CHANGED: %w", err)
	}
	return payload, nil
}

func compareProbeToLive(recorded, live readinessProbeDoc) error {
	return compareBindings(recorded, live)
}

func compareAttestToLive(attested, live readinessAttestPayload) error {
	var problems []string
	if attested.ProbeSourceRevision != live.ProbeSourceRevision {
		problems = append(problems, "probe source revision differs")
	}
	if attested.HoldSHA256 != live.HoldSHA256 || attested.ReceiptSHA256 != live.ReceiptSHA256 || attested.HoldCreatedAt != live.HoldCreatedAt {
		problems = append(problems, "hold or receipt bytes changed")
	}
	if attested.Generation != live.Generation {
		problems = append(problems, "generation changed")
	}
	if attested.ProbeStateSHA256 != live.ProbeStateSHA256 {
		problems = append(problems, "probe state hash differs")
	}
	if err := sameTargetFiles(attested.Target, attested.RequiredFiles, live.Target, live.RequiredFiles); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) != 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func compareBindings(recorded, live readinessProbeDoc) error {
	return compareAttestToLive(readinessAttestFromDoc(recorded, recorded.ProbedAt), readinessAttestFromDoc(live, live.ProbedAt))
}

func sameTargetFiles(wantTarget readinessProbeTarget, wantFiles []readinessProbeFile, gotTarget readinessProbeTarget, gotFiles []readinessProbeFile) error {
	if wantTarget != gotTarget {
		return errors.New("target identity or schema state changed")
	}
	if len(wantFiles) != len(gotFiles) {
		return errors.New("required file set changed")
	}
	for i := range wantFiles {
		if wantFiles[i] != gotFiles[i] {
			return fmt.Errorf("required file %s changed", wantFiles[i].Path)
		}
	}
	return nil
}

// loadExistingReleaseKey reads the existing 0600 hex ed25519 private key. It
// never generates or replaces a key, unlike the readiness-record path.
func loadExistingReleaseKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("release signing key must exist as a 0600 regular file")
	}
	if os.Geteuid() == 0 {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
			return nil, errors.New("release signing key must be owned by root")
		}
	}
	raw, err := readBoundedRegular(path, 4096)
	if err != nil {
		return nil, fmt.Errorf("read release signing key: %w", err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("release signing key is not a hex ed25519 private key")
	}
	return ed25519.PrivateKey(decoded), nil
}

// loadPublicAnchor reads a hex or base64 ed25519 public key from a trusted path.
func loadPublicAnchor(path string) (ed25519.PublicKey, error) {
	raw, err := readBoundedRegular(path, 4096)
	if err != nil {
		return nil, fmt.Errorf("read trusted public anchor: %w", err)
	}
	text := strings.TrimSpace(string(raw))
	if decoded, err := hex.DecodeString(text); err == nil && len(decoded) == ed25519.PublicKeySize {
		return ed25519.PublicKey(decoded), nil
	}
	if decoded, err := decodeBase64(text); err == nil && len(decoded) == ed25519.PublicKeySize {
		return ed25519.PublicKey(decoded), nil
	}
	return nil, errors.New("trusted public anchor is not a hex or base64 ed25519 public key")
}

func decodeBase64(text string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(text)
}

func loadProbeRecord(path string) (readinessProbeDoc, error) {
	var doc readinessProbeDoc
	raw, err := readBoundedRegular(path, 1<<20)
	if err != nil {
		return doc, fmt.Errorf("readiness-attest sign: read probe record: %w", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("readiness-attest sign: parse probe record: %w", err)
	}
	if doc.Format != readinessProbeFormat {
		return doc, fmt.Errorf("readiness-attest sign: unsupported probe format %q", doc.Format)
	}
	return doc, nil
}

// readBoundedRegular opens a regular file without following links.
func readBoundedRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is missing or not a regular file", filepath.Base(path))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds the size bound", filepath.Base(path))
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s exceeds the size bound", filepath.Base(path))
	}
	return raw, nil
}

// readinessConsumedRecord is the one-time attempt identity persisted under the
// maintenance root. It binds the exact attestation and live inputs that were
// accepted; a second consume refuses against this original.
type readinessProbeInputs struct {
	RootDir       string   `json:"root_dir"`
	Generation    uint64   `json:"generation"`
	DatabaseEnv   string   `json:"database_url_env,omitempty"`
	SQLitePath    string   `json:"sqlite_path,omitempty"`
	SQLiteVersion uint64   `json:"sqlite_generation,omitempty"`
	RequiredFiles []string `json:"required_files,omitempty"`
}

type readinessConsumedRecord struct {
	Format           string               `json:"format"`
	ConsumedAt       time.Time            `json:"consumed_at"`
	RequestID        string               `json:"request_id"`
	UID              int                  `json:"uid"`
	Hostname         string               `json:"hostname"`
	AttestSHA256     string               `json:"attest_sha256"`
	SignatureSHA256  string               `json:"signature_sha256"`
	HoldSHA256       string               `json:"hold_sha256"`
	ReceiptSHA256    string               `json:"receipt_sha256"`
	Generation       uint64               `json:"generation"`
	ProbeStateSHA256 string               `json:"probe_state_sha256"`
	ProbeRevision    string               `json:"probe_source_revision"`
	TargetDigest     string               `json:"target_digest"`
	RequiredDigest   string               `json:"required_files_digest"`
	AttestPath       string               `json:"attest_path"`
	Inputs           readinessProbeInputs `json:"probe_inputs"`
}

// runReadinessAttestConsume is the fixed consumption entry before a hold may be
// cleared: under the maintenance operation lock it re-runs verification
// against the pinned anchor, then writes a one-time attempt record with O_EXCL.
// It does not remove the hold and does not replace the existing release gate.
func runReadinessAttestConsume(ctx context.Context, args []string) error {
	f, err := parseReadinessAttestFlags("upgrade-readiness-attest-consume", args)
	if err != nil {
		return err
	}
	requestID := f.requestID
	if strings.TrimSpace(requestID) == "" || len(requestID) > 128 || strings.ContainsAny(requestID, "/\\\x00") {
		return errors.New("readiness-attest consume requires a safe --request-id (or URMAIL_READINESS_REQUEST_ID)")
	}
	if f.attestPath == "" {
		f.attestPath = filepath.Join(f.opts.rootDir, "upgrade_readiness_attest.json")
	}
	// The same orchestrator attempt must pass its held descriptor; consume
	// never acquires a second lock or treats another holder as approval.
	if err := verifyOwnedOperationLock(f.opts.rootDir, f.opts.lockFD); err != nil {
		return err
	}
	payload, err := verifyReadinessAttestation(ctx, f)
	if err != nil {
		return err
	}
	attestRaw, err := readBoundedRegular(f.attestPath, 1<<20)
	if err != nil {
		return err
	}
	sigRaw, err := readBoundedRegular(f.attestPath+".sig", 4096)
	if err != nil {
		return err
	}
	targetRaw, _ := json.Marshal(payload.Target)
	requiredRaw, _ := json.Marshal(payload.RequiredFiles)
	hostname, _ := os.Hostname()
	record := readinessConsumedRecord{
		Format: "urmail-readiness-consumed-v1", ConsumedAt: time.Now().UTC(), RequestID: requestID,
		UID: os.Geteuid(), Hostname: hostname, AttestSHA256: sha256Hex(attestRaw), SignatureSHA256: sha256Hex(sigRaw),
		HoldSHA256: payload.HoldSHA256, ReceiptSHA256: payload.ReceiptSHA256, Generation: payload.Generation,
		ProbeStateSHA256: payload.ProbeStateSHA256, ProbeRevision: payload.ProbeSourceRevision,
		TargetDigest: sha256Hex(targetRaw), RequiredDigest: sha256Hex(requiredRaw),
		AttestPath: f.attestPath, Inputs: readinessProbeInputs{
			RootDir: f.opts.rootDir, Generation: f.opts.generation, DatabaseEnv: f.opts.databaseEnv,
			SQLitePath: f.opts.sqlitePath, SQLiteVersion: f.opts.sqliteVersion, RequiredFiles: f.opts.requiredFiles,
		},
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(maintenance.Root(f.opts.rootDir), "upgrade_readiness_consumed.json")
	if err := writePrivateExclusive(path, raw); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, _ := readBoundedRegular(path, 1<<20)
			return fmt.Errorf("readiness-attest consume: already consumed by %s (sha256 %s)", path, sha256Hex(existing))
		}
		return err
	}
	fmt.Printf("CONSUMED record=%s request_id=%s hold_sha256=%s generation=%d\n", path, requestID, payload.HoldSHA256, payload.Generation)
	return nil
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// verifyReleaseConsume is the actual release-side gate for the same attempt:
// under the inherited operation lock it compares the one-time consume record
// with the live hold/receipt, re-verifies the signed attestation and live
// probe, then writes a release attempt record with O_EXCL to refuse replay.
func verifyReleaseConsume(ctx context.Context, rootDir string, lockFD int) error {
	if err := verifyOwnedOperationLock(rootDir, lockFD); err != nil {
		return err
	}
	recordPath := filepath.Join(maintenance.Root(rootDir), "upgrade_readiness_consumed.json")
	raw, err := readBoundedRegular(recordPath, 1<<20)
	if err != nil {
		return fmt.Errorf("release consume: %w", err)
	}
	var record readinessConsumedRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return fmt.Errorf("release consume: parse record: %w", err)
	}
	if record.Format != "urmail-readiness-consumed-v1" {
		return fmt.Errorf("release consume: unsupported record format %q", record.Format)
	}
	holdRaw, receiptRaw, err := maintenance.ReadUpgradeFenceRaw(rootDir)
	if err != nil {
		return fmt.Errorf("release consume: read live fence: %w", err)
	}
	var receipt maintenance.UpgradeReceipt
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
		return fmt.Errorf("release consume: parse receipt: %w", err)
	}
	if record.HoldSHA256 != sha256Hex(holdRaw) || record.ReceiptSHA256 != sha256Hex(receiptRaw) {
		return errors.New("release consume: record does not bind the live hold/receipt (wrong round)")
	}
	if record.Generation != receipt.Generation {
		return fmt.Errorf("release consume: record generation %d does not match live receipt generation %d", record.Generation, receipt.Generation)
	}
	opts := readinessProbeOptions{
		rootDir: record.Inputs.RootDir, generation: record.Inputs.Generation, databaseEnv: record.Inputs.DatabaseEnv,
		sqlitePath: record.Inputs.SQLitePath, sqliteVersion: record.Inputs.SQLiteVersion,
		requiredFiles: record.Inputs.RequiredFiles, requireLock: true, lockFD: lockFD,
	}
	payload, err := verifyReadinessAttestation(ctx, readinessAttestFlags{opts: opts, attestPath: record.AttestPath})
	if err != nil {
		return fmt.Errorf("release consume: %w", err)
	}
	targetRaw, _ := json.Marshal(payload.Target)
	requiredRaw, _ := json.Marshal(payload.RequiredFiles)
	if payload.HoldSHA256 != record.HoldSHA256 || payload.ReceiptSHA256 != record.ReceiptSHA256 ||
		payload.Generation != record.Generation || payload.ProbeStateSHA256 != record.ProbeStateSHA256 ||
		sha256Hex(targetRaw) != record.TargetDigest || sha256Hex(requiredRaw) != record.RequiredDigest {
		return errors.New("release consume: live attestation no longer matches the consumed record")
	}
	attemptPath := filepath.Join(maintenance.Root(rootDir), "upgrade_release_attempt.json")
	attempt := readinessReleaseAttempt{
		Format: "urmail-release-attempt-v1", State: "attempted", AttemptedAt: time.Now().UTC(), RequestID: record.RequestID,
		ConsumeSHA256: sha256Hex(raw), HoldSHA256: record.HoldSHA256, ReceiptSHA256: record.ReceiptSHA256,
		Generation: record.Generation, ProbeStateSHA256: record.ProbeStateSHA256,
	}
	if _, statErr := os.Lstat(attemptPath); statErr == nil {
		existingRaw, err := readBoundedRegular(attemptPath, 1<<20)
		if err != nil {
			return fmt.Errorf("release consume: inspect prior attempt: %w", err)
		}
		var existing readinessReleaseAttempt
		if err := json.Unmarshal(existingRaw, &existing); err != nil {
			return fmt.Errorf("release consume: unreadable prior release attempt: %w", err)
		}
		if existing.State == "completed" {
			return fmt.Errorf("release consume: this hold was already released (completed attempt %s sha256 %s); refusing replay", attemptPath, sha256Hex(existingRaw))
		}
		if existing.ConsumeSHA256 != attempt.ConsumeSHA256 || existing.HoldSHA256 != attempt.HoldSHA256 || existing.RequestID != record.RequestID {
			return fmt.Errorf("release consume: an interrupted attempt for a different consume/hold exists (%s); manual reconciliation required", attemptPath)
		}
		// An interrupted attempt that never removed the hold may continue under
		// the same lock and same consume record; it is not a success claim.
		fmt.Printf("CONSUME_VERIFIED_CONTINUE request_id=%s hold_sha256=%s generation=%d\n", record.RequestID, record.HoldSHA256, record.Generation)
		return nil
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("release consume: inspect prior attempt: %w", statErr)
	}
	attemptRaw, err := json.MarshalIndent(attempt, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateExclusive(attemptPath, attemptRaw); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("release consume: concurrent release attempt appeared; refusing")
		}
		return err
	}
	fmt.Printf("CONSUME_VERIFIED request_id=%s hold_sha256=%s generation=%d\n", record.RequestID, record.HoldSHA256, record.Generation)
	return nil
}

type readinessReleaseAttempt struct {
	Format           string    `json:"format"`
	State            string    `json:"state"`
	AttemptedAt      time.Time `json:"attempted_at"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
	RequestID        string    `json:"request_id"`
	ConsumeSHA256    string    `json:"consume_sha256"`
	HoldSHA256       string    `json:"hold_sha256"`
	ReceiptSHA256    string    `json:"receipt_sha256"`
	Generation       uint64    `json:"generation"`
	ProbeStateSHA256 string    `json:"probe_state_sha256"`
}

// markReleaseCompleted flips only an existing same-attempt record to completed
// after the hold was actually removed; a missing/different record is refused.
func markReleaseCompleted(rootDir, holdSHA string) error {
	attemptPath := filepath.Join(maintenance.Root(rootDir), "upgrade_release_attempt.json")
	raw, err := readBoundedRegular(attemptPath, 1<<20)
	if err != nil {
		return fmt.Errorf("mark release completed: %w", err)
	}
	var attempt readinessReleaseAttempt
	if err := json.Unmarshal(raw, &attempt); err != nil {
		return fmt.Errorf("mark release completed: parse attempt: %w", err)
	}
	if attempt.HoldSHA256 != holdSHA || attempt.State != "attempted" {
		return errors.New("mark release completed: attempt does not match this hold/state")
	}
	attempt.State = "completed"
	attempt.CompletedAt = time.Now().UTC()
	updated, err := json.MarshalIndent(attempt, "", "  ")
	if err != nil {
		return err
	}
	tmp := attemptPath + ".tmp"
	if err := writePrivateExclusive(tmp, updated); err != nil {
		return err
	}
	if err := os.Rename(tmp, attemptPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// noHoldReleaseOutcome decides the no-fence branch under --require-consume:
// only a completed attempt may report an idempotent release; anything else is
// an incomplete/manual reconciliation, never a first success.
func noHoldReleaseOutcome(rootDir string, requireConsume bool) (bool, error) {
	if !requireConsume {
		return false, nil
	}
	attemptPath := filepath.Join(maintenance.Root(rootDir), "upgrade_release_attempt.json")
	raw, err := readBoundedRegular(attemptPath, 1<<20)
	if err != nil {
		return false, errors.New("no active upgrade fence and no release attempt record; manual reconciliation required (not a successful first release)")
	}
	var attempt readinessReleaseAttempt
	if err := json.Unmarshal(raw, &attempt); err != nil || attempt.State != "completed" {
		return false, errors.New("no active upgrade fence but the release attempt is not completed; manual reconciliation required (not a successful first release)")
	}
	return true, nil
}
