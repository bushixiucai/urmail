package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bushixiucai/urmail/internal/backupauto"
)

// backupSpoolDir returns the directory holding the newest local generation.
// The backup worker writes there; tests and unusual deployments may point
// URMAIL_BACKUP_SPOOL at another absolute directory.
func backupSpoolDir() string {
	if dir := strings.TrimSpace(os.Getenv("URMAIL_BACKUP_SPOOL")); dir != "" {
		return dir
	}
	return automaticBackupSpool
}

// verifyBackupArtifacts proves that the newest recorded generation actually
// exists on disk with the hashes its manifest names. The backup_runs row alone
// is metadata: a tampered or missing artifact must refuse the fence instead of
// letting an upgrade run on a backup that cannot be restored.
func verifyBackupArtifacts(spoolDir, manifestName string) error {
	name := strings.TrimSpace(manifestName)
	if name == "" {
		return errors.New("the newest backup run records no manifest name; artifact integrity cannot be verified")
	}
	if filepath.Base(name) != name || strings.Contains(name, "..") {
		return fmt.Errorf("backup manifest name %q is not a plain file name", name)
	}
	if spoolDir == "" || !filepath.IsAbs(spoolDir) {
		return fmt.Errorf("backup spool %q is not an absolute directory", spoolDir)
	}
	manifestPath := filepath.Join(spoolDir, name)
	raw, err := os.ReadFile(manifestPath) // #nosec G304 -- fixed spool directory plus a validated plain file name.
	if err != nil {
		return fmt.Errorf("read backup manifest %s: %w", name, err)
	}
	var manifest struct {
		Artifacts []backupauto.Artifact `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parse backup manifest %s: %w", name, err)
	}
	if len(manifest.Artifacts) == 0 {
		return fmt.Errorf("backup manifest %s lists no artifacts", name)
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Name == "" || filepath.Base(artifact.Name) != artifact.Name {
			return fmt.Errorf("backup manifest %s names an invalid artifact %q", name, artifact.Name)
		}
		if err := verifyArtifactFile(filepath.Join(spoolDir, artifact.Name), artifact.SHA256); err != nil {
			return err
		}
		if artifact.EncryptedName != "" {
			if filepath.Base(artifact.EncryptedName) != artifact.EncryptedName {
				return fmt.Errorf("backup manifest %s names an invalid encrypted artifact %q", name, artifact.EncryptedName)
			}
			if err := verifyArtifactFile(filepath.Join(spoolDir, artifact.EncryptedName), artifact.EncryptedSHA256); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyArtifactFile(path, want string) error {
	if len(want) != 64 {
		return fmt.Errorf("backup artifact %s has no usable sha256 in the manifest", filepath.Base(path))
	}
	file, err := os.Open(path) // #nosec G304 -- validated plain artifact name inside the spool.
	if err != nil {
		return fmt.Errorf("open backup artifact %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return fmt.Errorf("hash backup artifact %s: %w", filepath.Base(path), err)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("backup artifact %s sha256 mismatch (manifest %s, disk %s)", filepath.Base(path), want, got)
	}
	return nil
}

// verifyFreeSpace refuses the fence when the filesystem holding the backup
// spool cannot take another generation. The threshold defaults to 2 GiB and
// can be raised or lowered with URMAIL_UPGRADE_MIN_FREE_BYTES.
func verifyFreeSpace(spoolDir string) error {
	minimum := int64(2 << 30)
	if raw := strings.TrimSpace(os.Getenv("URMAIL_UPGRADE_MIN_FREE_BYTES")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("URMAIL_UPGRADE_MIN_FREE_BYTES %q is not an integer", raw)
		}
		minimum = parsed
	}
	if minimum < 0 {
		return fmt.Errorf("URMAIL_UPGRADE_MIN_FREE_BYTES must not be negative")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(spoolDir, &stat); err != nil {
		return fmt.Errorf("inspect free space for %s: %w", spoolDir, err)
	}
	if stat.Bsize <= 0 {
		return fmt.Errorf("backup spool %s reports an invalid block size %d", spoolDir, stat.Bsize)
	}
	bsize := uint64(stat.Bsize) // #nosec G115 -- checked positive above.
	// Compare in blocks so the free-space arithmetic cannot overflow: both
	// sides are uint64 and the minimum was already checked to be non-negative.
	minBlocks := uint64(minimum)/bsize + 1 // #nosec G115 -- minimum >= 0 checked above.
	if stat.Bavail < minBlocks {
		return fmt.Errorf("backup spool %s has %d blocks of %d bytes free, below the %d byte minimum for an upgrade", spoolDir, stat.Bavail, stat.Bsize, minimum)
	}
	return nil
}
