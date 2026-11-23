package main

import (
	"context"
	"crypto/ed25519"
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
	required := multiFlag{}
	flags.Var(&required, "required-file", "absolute required file whose digest is bound (repeatable)")
	probe := flags.String("probe", "", "readiness probe record to bind (required for sign)")
	attest := flags.String("attest", "", "signed attestation payload (required for verify)")
	key := flags.String("key", releaseSigningKeyPath, "existing ed25519 release signing key (never created)")
	pub := flags.String("pubkey", "", "trusted ed25519 public key anchor (required for verify)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return out, errors.New("usage: upgrade readiness-attest sign|verify --generation N --root-dir DIR [--database-url-env NAME|--sqlite PATH] [--required-file PATH ...] [--probe FILE] [--attest FILE] [--key PATH|--pubkey PATH]")
	}
	if *generation == 0 {
		return out, errors.New("readiness-attest requires --generation")
	}
	if *sqlitePath != "" && *sqliteVersion == 0 {
		return out, errors.New("readiness-attest requires --sqlite-generation with --sqlite")
	}
	out.opts = readinessProbeOptions{rootDir: *rootDir, generation: *generation, databaseEnv: *databaseEnv, sqlitePath: *sqlitePath, sqliteVersion: *sqliteVersion, requiredFiles: required}
	out.probePath, out.attestPath, out.keyPath, out.pubKeyPath = *probe, *attest, *key, *pub
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
	if f.pubKeyPath == "" {
		return errors.New("readiness-attest verify requires a trusted --pubkey anchor")
	}
	pub, err := loadPublicAnchor(f.pubKeyPath)
	if err != nil {
		return err
	}
	payloadRaw, err := readBoundedRegular(f.attestPath, 1<<20)
	if err != nil {
		return fmt.Errorf("readiness-attest verify: %w", err)
	}
	var payload readinessAttestPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return fmt.Errorf("readiness-attest verify: parse attestation: %w", err)
	}
	if payload.Format != readinessAttestFormat {
		return fmt.Errorf("readiness-attest verify: unsupported format %q", payload.Format)
	}
	now := time.Now().UTC()
	if payload.IssuedAt.After(now.Add(time.Minute)) || now.Sub(payload.IssuedAt) > readinessFreshnessWindow {
		return errors.New("readiness-attest verify: attestation is expired or not yet valid")
	}
	sigRaw, err := readBoundedRegular(f.attestPath+".sig", 4096)
	if err != nil {
		return fmt.Errorf("readiness-attest verify: %w", err)
	}
	signature, err := hex.DecodeString(strings.TrimSpace(string(sigRaw)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("readiness-attest verify: signature is malformed")
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, canonical, signature) {
		return errors.New("readiness-attest verify: signature does not verify against the pinned anchor")
	}
	live, err := probeReadiness(ctx, f.opts)
	if err != nil {
		return err
	}
	livePayload := readinessAttestFromDoc(live, payload.IssuedAt)
	if err := compareAttestToLive(payload, livePayload); err != nil {
		return fmt.Errorf("readiness-attest verify: STATE_CHANGED: %w", err)
	}
	fmt.Printf("VALID attest=%s state_sha256=%s generation=%d\n", f.attestPath, payload.ProbeStateSHA256, payload.Generation)
	return nil
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
