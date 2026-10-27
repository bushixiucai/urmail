// Package backupauto implements OPS-001: automated, encrypted, offsite,
// retention-protected and independently restorable backups of urMail data.
//
// The backup set is a PostgreSQL dump plus tar archives (configuration,
// certificates, Maildirs, archive spool). Collaboration data (contacts,
// address books, calendars) lives in PostgreSQL since migrations 0108-0111
// and is therefore already covered by the dump; the planner records this
// explicitly in the manifest scope.
//
// Archives are encrypted with age (X25519) before leaving the machine.
// The manifest format is versioned: version 1 is the historical plaintext
// manifest produced by "urmailctl backup"; version 2 adds encryption fields
// and is documented in manifest.go. Restore compatibility: a v2 manifest
// lists the same artifacts under their PLAINTEXT names, sizes and SHA-256
// sums, so "urmailctl restore --manifest" continues to verify decrypted
// files without change; v1 manifests restore identically (no decryption).
package backupauto

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// ManifestVersion2 is the encrypted-offsite manifest format.
const ManifestVersion2 = 2

// EncryptionInfo describes how every artifact of a v2 backup was encrypted.
type EncryptionInfo struct {
	// Algorithm is "age" (X25519 recipient, ChaCha20-Poly1305 payload).
	Algorithm string `json:"algorithm"`
	// Recipient is the age public key the archive can be decrypted with.
	// The matching identity must be recoverable independently of this host.
	Recipient string `json:"recipient"`
}

// Artifact is one file in a backup set. For version 2 manifests the fields
// keep their version-1 meaning for the PLAINTEXT artifact (Name, Size and
// SHA256 are what "urmailctl restore" verifies after decryption); the
// encrypted form actually stored offsite is described by EncryptedSHA256,
// EncryptedSize and EncryptionAlgorithm.
type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	// EncryptedName is the offsite object name (Name + ".age" for age).
	EncryptedName string `json:"encrypted_name,omitempty"`
	// EncryptedSHA256 is the integrity hash of the ciphertext object.
	EncryptedSHA256 string `json:"encrypted_sha256,omitempty"`
	// EncryptedSize is the on-wire size of the ciphertext object.
	EncryptedSize int64  `json:"encrypted_size,omitempty"`
	Algorithm     string `json:"encryption,omitempty"`
	// Kind classifies the artifact for planners and restore tooling.
	Kind string `json:"kind,omitempty"`
}

// Manifest is the backup manifest. Version 1 is byte-compatible with the
// historical cmd/urmailctl manifest; version 2 adds encryption metadata.
type Manifest struct {
	Version    int             `json:"version"`
	Scope      string          `json:"scope"`
	Consistent bool            `json:"consistent_maintenance"`
	CreatedAt  time.Time       `json:"created_at"`
	Artifacts  []Artifact      `json:"artifacts"`
	Encryption *EncryptionInfo `json:"encryption,omitempty"`
}

// DecodeManifest reads and validates a manifest from r. Both version 1 and
// version 2 manifests are accepted; any other version is rejected so that a
// future incompatible format fails closed instead of restoring unchecked.
func DecodeManifest(r io.Reader) (*Manifest, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse backup manifest: %w", err)
	}
	if m.Version != 1 && m.Version != ManifestVersion2 {
		return nil, fmt.Errorf("unsupported backup manifest version %d", m.Version)
	}
	if m.Version == ManifestVersion2 {
		if m.Encryption == nil || m.Encryption.Algorithm == "" {
			return nil, fmt.Errorf("backup manifest v2 requires encryption metadata")
		}
		for _, a := range m.Artifacts {
			if a.EncryptedName == "" || a.EncryptedSHA256 == "" {
				return nil, fmt.Errorf("backup manifest v2 artifact %q lacks encrypted metadata", a.Name)
			}
		}
	}
	return &m, nil
}

// LoadManifest opens and decodes a manifest file.
func LoadManifest(path string) (*Manifest, error) {
	f, err := os.Open(path) // #nosec G304 -- path is chosen by the root operator or the local daemon.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return DecodeManifest(f)
}
