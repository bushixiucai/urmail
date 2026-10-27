package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"

	"github.com/bushixiucai/urmail/internal/backupauto"
)

// ageIdentity keeps the CLI file self-contained without renaming imports.
type ageIdentity = age.Identity

func loadIdentities(path string) ([]ageIdentity, error) {
	return backupauto.LoadIdentities(path)
}

func plainFileHash(path string) (string, int64, error) {
	f, err := os.Open(path) // #nosec G304 G703 -- root-selected artifact path, never a request value.
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func manifestJSON(m *backupauto.Manifest) (string, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

func stampFromManifestName(manifestPath string) string {
	name := manifestPath
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == os.PathSeparator {
			name = name[i+1:]
			break
		}
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, "urmail-"), ".manifest.json")
}

func strReader(s string) io.Reader { return strings.NewReader(s) }

// decryptBackupArtifact verifies the ciphertext against the v2 manifest,
// decrypts it with the operator age identity, and verifies the plaintext
// against the manifest BEFORE the output file is used by restore. The
// output is written 0600 to a temp path and renamed, so a failed decrypt
// never leaves a plausible-looking half artifact.
func decryptBackupArtifact(manifestPath, artifactName, outPath, identityPath string) error {
	manifest, err := backupauto.LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	if manifest.Version != backupauto.ManifestVersion2 {
		return fmt.Errorf("manifest is not encrypted (version %d); nothing to decrypt", manifest.Version)
	}
	entries, err := backupauto.ArtifactByName(manifest)
	if err != nil {
		return err
	}
	artifact, ok := entries[artifactName]
	if !ok {
		return fmt.Errorf("artifact %q absent from manifest", artifactName)
	}
	identities, err := loadIdentities(identityPath)
	if err != nil {
		return err
	}
	cipherPath := dirOf(manifestPath) + string(os.PathSeparator) + artifact.EncryptedName
	sum, _, err := plainFileHash(cipherPath)
	if err != nil {
		return fmt.Errorf("read ciphertext %s: %w", artifact.EncryptedName, err)
	}
	if sum != artifact.EncryptedSHA256 {
		return fmt.Errorf("ciphertext checksum mismatch for %s", artifact.EncryptedName)
	}
	cipher, err := os.Open(cipherPath) // #nosec G304 G703 -- root-selected artifact path, never a request value.
	if err != nil {
		return err
	}
	defer func() { _ = cipher.Close() }()
	tmp := outPath + ".decrypting"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 G703 -- root-selected output path, never a request value.
	if err != nil {
		return err
	}
	digest := sha256.New()
	if err := backupauto.DecryptFrom(io.MultiWriter(out, digest), cipher, identities...); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp) // #nosec G703 -- sibling of the root-selected output path.
		return fmt.Errorf("decrypt %s: %w", artifactName, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp) // #nosec G703 -- sibling of the root-selected output path.
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != artifact.SHA256 {
		_ = os.Remove(tmp) // #nosec G703 -- sibling of the root-selected output path.
		return fmt.Errorf("plaintext checksum mismatch for %s after decryption", artifactName)
	}
	if err := os.Rename(tmp, outPath); err != nil { // #nosec G703 -- both sides are root-selected artifact paths.
		_ = os.Remove(tmp)
		return err
	}
	_ = bytes.MinRead
	return nil
}

var _ = context.Background
