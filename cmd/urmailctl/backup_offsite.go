package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/bushixiucai/urmail/internal/backupauto"
)

// runBackupOffsite implements the "backup offsite" / "backup verify-offsite"
// / "backup decrypt" subcommands (OPS-001). They are routed from runBackup
// so every new entry point stays inside the backup* command family.
//
//	backup offsite       --manifest FILE --uri URI [--execute]
//	backup verify-offsite --manifest FILE --uri URI --identity FILE
//	backup decrypt       --manifest FILE --artifact NAME --out FILE --identity FILE
//
// offsite uploads the encrypted artifacts of one LOCAL manifest and verifies
// them; verify-offsite only LISTs and checksums REMOTE artifacts and never
// writes local state; decrypt turns an encrypted offsite artifact back into
// the plaintext file the existing "restore --manifest" flow verifies.
func runBackupOffsite(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: backup offsite|verify-offsite|decrypt")
	}
	switch args[0] {
	case "upload":
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

func runBackupOffsiteUpload(ctx context.Context, args []string) error {
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
	if os.Geteuid() != 0 {
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
	if err := store.Upload(ctx, name, strReader(raw), int64(len(raw))); err != nil {
		return fmt.Errorf("upload manifest: %w", err)
	}
	fmt.Printf("offsite upload complete: %d artifacts, manifest %s\n", len(manifest.Artifacts), name)
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
