package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
)

// runBackupOffsite implements the "backup offsite" / "backup verify-offsite"
// / "backup decrypt" subcommands (OPS-001). They are routed from runBackup
// so every new entry point stays inside the backup* command family.
//
//	backup offsite       --manifest FILE --uri URI [--execute]
//	backup upload        --manifest FILE --uri URI (same contract as offsite)
//
// --execute is accepted for compatibility and is a no-op: the upload command
// always executes the transfer.
//
//	backup verify-offsite --manifest FILE --uri URI --identity FILE
//	backup decrypt       --manifest FILE --artifact NAME --out FILE --identity FILE
//
// offsite uploads the encrypted artifacts of one LOCAL manifest and verifies
// them; verify-offsite only LISTs and checksums REMOTE artifacts and never
// writes local state; decrypt turns an encrypted offsite artifact back into
// the plaintext file the existing "restore --manifest" flow verifies.
func runBackupOffsite(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: backup offsite|upload|verify-offsite|decrypt")
	}
	switch args[0] {
	// backup offsite and backup upload are the same contract: upload one
	// manifest to the destination and record the run.
	case "upload", "offsite":
		return runBackupOffsiteUpload(ctx, args[1:])
	case "verify-offsite":
		return runBackupVerifyOffsite(ctx, args[1:])
	case "decrypt":
		return runBackupDecrypt(ctx, args[1:])
	default:
		return fmt.Errorf("unknown backup subcommand %q", args[0])
	}
}

func parseFlag(args []string, flag string) (string, []string, error) {
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%s requires a value", flag)
			}
			rest := append(append([]string{}, args[:i]...), args[i+2:]...)
			return args[i+1], rest, nil
		}
	}
	return "", args, nil
}

// offsiteEnv builds offsite credentials from the environment so keys never
// appear on a command line.
func offsiteEnv() (backupauto.Credentials, error) {
	creds := backupauto.Credentials{
		S3: backupauto.S3Credentials{
			AccessKeyID: os.Getenv("URMAIL_BACKUP_S3_ACCESS_KEY_ID"),
			SecretKey:   os.Getenv("URMAIL_BACKUP_S3_SECRET_KEY"),
			Region:      os.Getenv("URMAIL_BACKUP_S3_REGION"),
			Endpoint:    os.Getenv("URMAIL_BACKUP_S3_ENDPOINT"),
		},
		SFTP: backupauto.SFTPCredentials{
			Password:           os.Getenv("URMAIL_BACKUP_SFTP_PASSWORD"),
			PrivateKeyPath:     os.Getenv("URMAIL_BACKUP_SFTP_KEY"),
			HostKeyFingerprint: os.Getenv("URMAIL_BACKUP_SFTP_HOST_KEY"),
		},
	}
	if creds.S3.AccessKeyID == "" && creds.SFTP.PrivateKeyPath == "" && creds.SFTP.Password == "" {
		return creds, fmt.Errorf("set URMAIL_BACKUP_S3_* or URMAIL_BACKUP_SFTP_* credentials in the environment")
	}
	return creds, nil
}

func destinationFromEnv(uri string) (backupauto.OffsiteStore, error) {
	creds, err := offsiteEnv()
	if err != nil {
		return nil, err
	}
	return backupauto.ParseDestination(uri, creds)
}

// backupOffsiteEffectiveUID mirrors upgradeEffectiveUID: the manual upload
// is a root CLI, and tests stub the identity check the same way.
var backupOffsiteEffectiveUID = os.Geteuid

// openPlatformPool loads the platform store for run recording on whichever
// engine is configured. The manual offsite upload must record backup_runs
// (R1) so the upgrade-fence pre-flight and the release gate see one source of
// truth; single-engine SQLite has no separate platform DSN.
func openPlatformPool(ctx context.Context) (*db.Pool, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load configuration: %w", err)
	}
	pool, closePool, err := openPlatformStorePool(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("open platform store: %w", err)
	}
	return pool, closePool, nil
}

func runBackupOffsiteUpload(ctx context.Context, args []string) (returnErr error) {
	// --execute stays a documented no-op; drop it before flag parsing so an
	// older operator script keeps working and never trips an unknown flag.
	args = dropFlag(args, "--execute")
	manifestPath, _, err := parseFlag(args, "--manifest")
	if err != nil {
		return err
	}
	uri, _, err := parseFlag(args, "--uri")
	if err != nil {
		return err
	}
	if manifestPath == "" || uri == "" {
		return fmt.Errorf("offsite upload requires --manifest FILE and --uri URI")
	}
	if backupOffsiteEffectiveUID() != 0 {
		return fmt.Errorf("backup offsite upload must run as root")
	}
	store, err := destinationFromEnv(uri)
	if err != nil {
		return err
	}
	manifest, err := backupauto.LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	recipient, _, err := parseFlag(args, "--recipient")
	if err != nil {
		return err
	}
	if manifest.Version == backupauto.ManifestVersion2 || manifest.Version == backupauto.ManifestVersion3 {
		if recipient == "" && manifest.Encryption != nil {
			recipient = manifest.Encryption.Recipient
		}
	}
	if recipient == "" {
		return fmt.Errorf("manifest carries no encryption metadata; pass --recipient KEY (the archive will be encrypted) ")
	}

	// R1 (v0.6.25): manual uploads record a backup_runs row, so the
	// upgrade-fence fresh-backup pre-flight can trust one source of truth.
	// The run is only success after the uploaded generation re-verifies
	// remotely -- the same bar the scheduled worker must clear.
	pool, closePool, err := openPlatformPool(ctx)
	if err != nil {
		return err
	}
	defer closePool()
	recorder := &backupauto.StateRecorder{Pool: pool}
	scope := backupauto.ScopeComplete
	if manifest.Scope != "" {
		scope = manifest.Scope
	}
	started := time.Now().UTC()
	runUID, err := recorder.StartRun(ctx, scope, started)
	if err != nil {
		return fmt.Errorf("record offsite run: %w", err)
	}
	// The fresh-backup pre-flight only accepts uploaded or pending_local_only;
	// a manual upload is by definition the uploaded disposition, so record it
	// on the run it just opened instead of leaving a historical empty state.
	if err = recorder.SetOffsiteState(ctx, runUID, backupauto.OffsiteStateUploaded); err != nil {
		return fmt.Errorf("record offsite disposition: %w", err)
	}
	var uploadedBytes int64
	var remoteManifestName string
	defer func() {
		finishErr := recorder.FinishRun(ctx, runUID, returnErr == nil, returnErr, uploadedBytes, time.Since(started), remoteManifestName, uri, 0)
		if returnErr == nil && finishErr != nil {
			returnErr = finishErr
		}
	}()

	base := dirOf(manifestPath)
	for i, artifact := range manifest.Artifacts {
		plainPath := base + string(os.PathSeparator) + artifact.Name
		sum, size, err := plainFileHash(plainPath)
		if err != nil {
			return fmt.Errorf("artifact %s: %w", artifact.Name, err)
		}
		if artifact.SHA256 != "" && sum != artifact.SHA256 {
			return fmt.Errorf("artifact %s does not match the manifest checksum", artifact.Name)
		}
		entry, err := backupauto.EncryptAndUpload(ctx, store, backupauto.LocalArtifact{
			Name: artifact.Name, Path: plainPath, Kind: artifact.Kind, Size: size, SHA256: sum,
		}, recipient)
		if err != nil {
			return err
		}
		entry.Mode = artifact.Mode
		entry.UID = artifact.UID
		entry.GID = artifact.GID
		manifest.Artifacts[i] = entry
		uploadedBytes += entry.EncryptedSize
	}
	if manifest.Version < backupauto.ManifestVersion3 {
		manifest.Version = backupauto.ManifestVersion2
	}
	manifest.Encryption = &backupauto.EncryptionInfo{Algorithm: backupauto.EncryptionAlgorithmAge, Recipient: recipient}
	raw, err := manifestJSON(manifest)
	if err != nil {
		return err
	}
	name := backupauto.ManifestName(stampFromManifestName(manifestPath))
	remoteManifestName = name
	if err := store.Upload(ctx, name, strReader(raw), int64(len(raw))); err != nil {
		return fmt.Errorf("upload manifest: %w", err)
	}
	if err := backupauto.VerifyUploadedGeneration(ctx, store, manifest, name, raw); err != nil {
		return fmt.Errorf("%w", err)
	}
	fmt.Printf("offsite upload complete: %d artifacts, %d ciphertext bytes, manifest %s (run %s verified and recorded)\n", len(manifest.Artifacts), uploadedBytes, name, runUID)
	return nil
}

func runBackupVerifyOffsite(ctx context.Context, args []string) error {
	manifestPath, rest, err := parseFlag(args, "--manifest")
	if err != nil {
		return err
	}
	uri, rest, err := parseFlag(rest, "--uri")
	if err != nil {
		return err
	}
	identityPath, _, err := parseFlag(rest, "--identity")
	if err != nil {
		return err
	}
	if manifestPath == "" || uri == "" {
		return fmt.Errorf("verify-offsite requires --manifest FILE and --uri URI")
	}
	store, err := destinationFromEnv(uri)
	if err != nil {
		return err
	}
	manifest, err := backupauto.LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	var identities []ageIdentity
	if identityPath != "" {
		identities, err = loadIdentities(identityPath)
		if err != nil {
			return err
		}
	}
	results, err := backupauto.VerifyOffsite(ctx, store, manifest, identities)
	if err != nil {
		return err
	}
	allOK := true
	for _, result := range results {
		state := "OK"
		if !result.OK() {
			state = "FAIL"
			allOK = false
		}
		fmt.Printf("%s %s (encrypted=%t plaintext=%t)\n", state, result.Name, result.EncryptedOK, result.PlaintextOK)
		if result.Error != nil {
			fmt.Printf("     %v\n", result.Error)
		}
	}
	if !allOK {
		return fmt.Errorf("offsite verification failed")
	}
	fmt.Printf("verify-offsite: %d artifacts verified; no local state written\n", len(results))
	return nil
}

func runBackupDecrypt(_ context.Context, args []string) error {
	manifestPath, rest, err := parseFlag(args, "--manifest")
	if err != nil {
		return err
	}
	artifactName, rest, err := parseFlag(rest, "--artifact")
	if err != nil {
		return err
	}
	outPath, rest, err := parseFlag(rest, "--out")
	if err != nil {
		return err
	}
	identityPath, _, err := parseFlag(rest, "--identity")
	if err != nil {
		return err
	}
	if manifestPath == "" || artifactName == "" || outPath == "" || identityPath == "" {
		return fmt.Errorf("decrypt requires --manifest FILE --artifact NAME --out FILE --identity FILE")
	}
	if err := decryptBackupArtifact(manifestPath, artifactName, outPath, identityPath); err != nil {
		return err
	}
	fmt.Printf("decrypted %s to %s; restore with: urmailctl restore --dump %s --manifest %s\n", artifactName, outPath, outPath, manifestPath)
	return nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == os.PathSeparator {
			return path[:i]
		}
	}
	return "."
}

var _ = io.Discard

// dropFlag removes every occurrence of a value-less compatibility flag.
func dropFlag(args []string, flag string) []string {
	out := args[:0:0]
	for _, arg := range args {
		if arg != flag {
			out = append(out, arg)
		}
	}
	return out
}
