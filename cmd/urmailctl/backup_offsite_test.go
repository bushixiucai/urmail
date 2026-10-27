package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/bushixiucai/urmail/internal/backupauto"
)

// TestBackupDecryptRoundTrip exercises the offsite/decrypt path end to end
// with an in-memory-ish local flow: a v2 manifest is produced via
// EncryptAndUpload semantics, then decryptBackupArtifact must restore the
// exact plaintext and refuse tampered ciphertext.
func TestBackupDecryptRoundTrip(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plaintext := []byte("restore me exactly")
	plainPath := filepath.Join(dir, "urmail-x.dump")
	if err := os.WriteFile(plainPath, plaintext, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plaintext)
	cipherSum := sha256.Sum256(append([]byte(nil), plaintext...)) // placeholder, replaced below

	// Produce the encrypted artifact with the package pipeline against a
	// local file "store" via a tiny adapter around the filesystem.
	store := newFileStore(t, dir)
	entry, err := backupauto.EncryptAndUpload(context.Background(), store, backupauto.LocalArtifact{
		Name: "urmail-x.dump", Path: plainPath, Kind: backupauto.KindPgDump,
		Size: int64(len(plaintext)), SHA256: hex.EncodeToString(sum[:]),
	}, identity.Recipient().String())
	if err != nil {
		t.Fatal(err)
	}
	manifest := &backupauto.Manifest{
		Version: backupauto.ManifestVersion2, Scope: backupauto.ScopeComplete, Consistent: true,
		Artifacts:  []backupauto.Artifact{entry},
		Encryption: &backupauto.EncryptionInfo{Algorithm: backupauto.EncryptionAlgorithmAge, Recipient: identity.Recipient().String()},
	}
	manifestPath := filepath.Join(dir, backupauto.ManifestName("x"))
	raw, err := jsonMarshalIndent(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(dir, "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(dir, "restored.dump")
	if err := decryptBackupArtifact(manifestPath, "urmail-x.dump", outPath, identityPath); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	restored, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(plaintext) {
		t.Fatal("decrypted plaintext differs")
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("decrypted artifact must be 0600, got %v", info.Mode().Perm())
	}

	// Tampered ciphertext must be rejected before restore sees the file.
	if err := os.WriteFile(filepath.Join(dir, entry.EncryptedName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := decryptBackupArtifact(manifestPath, "urmail-x.dump", outPath+".bad", identityPath); err == nil {
		t.Fatal("tampered ciphertext must be rejected")
	}
	if _, err := os.Stat(outPath + ".bad"); !os.IsNotExist(err) {
		t.Fatal("failed decrypt must not leave an output file")
	}
	_ = cipherSum
}

func jsonMarshalIndent(m *backupauto.Manifest) (string, error) {
	raw, err := jsonMarshalIndentRaw(m)
	return raw, err
}
