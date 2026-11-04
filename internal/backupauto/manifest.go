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
	"path"
	"strings"
	"time"
)

// Manifest format versions.
const (
	ManifestVersion1 = 1
	ManifestVersion2 = 2
	ManifestVersion3 = 3
	LayoutVersion1   = 1
)

// Well-known logical Slot identifiers (WP1.3 / F03).
const (
	SlotMailStorage       = "mail/storage"
	SlotMailArchive       = "mail/archive"
	SlotMailSpool         = "mail/archive-spool"
	SlotConfigMasterKey   = "config/master-key"
	SlotConfigOperator    = "config/operator"
	SlotConfigAgentCerts  = "config/agent-certs"
	SlotConfigPostfix     = "config/postfix"
	SlotConfigOpenDKIM    = "config/opendkim"
	SlotConfigCerts       = "config/certs"
	SlotConfigLetsEncrypt = "config/letsencrypt"
	SlotConfigStorageSSL  = "config/storage-ssl"
)

// Slot defines one logical slot in a backup set, mapping an artifact archive
// path to a logical domain that can be redirected to different target roots
// on recovery without hardcoded whitelist interception.
type Slot struct {
	// Artifact is the archive name (e.g. "urmail-mail-stamp.tar.gz").
	Artifact string `json:"artifact"`
	// ArchivePath is the path prefix inside the archive (e.g. "mnt/maildata/mail" or "etc/urmail").
	ArchivePath string `json:"archive_path"`
	// TargetPath is the original host path at backup time (e.g. "/mnt/maildata/mail").
	TargetPath string `json:"target_path,omitempty"`
	// Kind specifies whether the slot maps a directory ("dir") or single file ("file").
	Kind string `json:"kind,omitempty"`
}

// EncryptionInfo describes how every artifact of a backup was encrypted.
type EncryptionInfo struct {
	// Algorithm is "age" (X25519 recipient, ChaCha20-Poly1305 payload).
	Algorithm string `json:"algorithm"`
	// Recipient is the age public key the archive can be decrypted with.
	// The matching identity must be recoverable independently of this host.
	Recipient string `json:"recipient"`
}

// Artifact is one file in a backup set. For version 2 and 3 manifests the fields
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
// historical cmd/urmailctl manifest; version 2 adds encryption metadata;
// version 3 adds layout_version and logical slots for path-agnostic restore.
type Manifest struct {
	Version       int             `json:"version"`
	LayoutVersion int             `json:"layout_version,omitempty"`
	Scope         string          `json:"scope"`
	Consistent    bool            `json:"consistent_maintenance"`
	CreatedAt     time.Time       `json:"created_at"`
	Artifacts     []Artifact      `json:"artifacts"`
	Encryption    *EncryptionInfo `json:"encryption,omitempty"`
	Slots         map[string]Slot `json:"slots,omitempty"`
}

// DecodeManifest reads and validates a manifest from r. Versions 1, 2, and 3
// are accepted; any other version is rejected so that a future incompatible
// format fails closed instead of restoring unchecked.
func DecodeManifest(r io.Reader) (*Manifest, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse backup manifest: %w", err)
	}
	if m.Version != 1 && m.Version != ManifestVersion2 && m.Version != ManifestVersion3 {
		return nil, fmt.Errorf("unsupported backup manifest version %d", m.Version)
	}
	if m.Version == ManifestVersion3 {
		if m.LayoutVersion != LayoutVersion1 {
			return nil, fmt.Errorf("backup manifest v3 requires layout_version %d (got %d)", LayoutVersion1, m.LayoutVersion)
		}
		if len(m.Slots) == 0 {
			return nil, fmt.Errorf("backup manifest v3 requires logical slots")
		}
		type slotEntry struct {
			name     string
			artifact string
			arch     string
		}
		entries := make([]slotEntry, 0, len(m.Slots))
		for slotName, s := range m.Slots {
			if slotName == "" || s.Artifact == "" || s.ArchivePath == "" {
				return nil, fmt.Errorf("backup manifest v3 slot %q is invalid", slotName)
			}
			cleanArch := path.Clean(strings.TrimPrefix(s.ArchivePath, "/"))
			if cleanArch == "" || cleanArch == "." || cleanArch == ".." || strings.HasPrefix(cleanArch, "../") {
				return nil, fmt.Errorf("backup manifest v3 slot %q has invalid archive_path %q", slotName, s.ArchivePath)
			}
			entries = append(entries, slotEntry{
				name:     slotName,
				artifact: s.Artifact,
				arch:     cleanArch,
			})
		}

		// Contract: identical archive_path within the same artifact is strictly forbidden
		for i := 0; i < len(entries); i++ {
			for j := i + 1; j < len(entries); j++ {
				e1, e2 := entries[i], entries[j]
				if e1.artifact == e2.artifact && e1.arch == e2.arch {
					return nil, fmt.Errorf("backup manifest v3 slots %q and %q have duplicate archive_path %q in artifact %q", e1.name, e2.name, e1.arch, e1.artifact)
				}
			}
		}
	}
	if m.Version == ManifestVersion2 || (m.Version == ManifestVersion3 && m.Encryption != nil) {
		if m.Encryption == nil || m.Encryption.Algorithm == "" {
			return nil, fmt.Errorf("backup manifest v%d requires encryption metadata", m.Version)
		}
		for _, a := range m.Artifacts {
			if a.EncryptedName == "" || a.EncryptedSHA256 == "" {
				return nil, fmt.Errorf("backup manifest v%d artifact %q lacks encrypted metadata", m.Version, a.Name)
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
