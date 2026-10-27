package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bushixiucai/urmail/internal/backupauto"
)

// verifyBackupManifest verifies plaintext restore artifacts against the
// backup manifest. Both manifest versions are accepted: version 1 is the
// historical plaintext manifest; version 2 keeps the same PLAINTEXT name,
// size and SHA-256 fields per artifact (the encrypted offsite form is
// described by extra fields that backupauto.DecodeManifest validates), so
// an operator who ran "backup decrypt" verifies the decrypted artifacts
// exactly as before. Unsupported future versions fail closed inside
// backupauto.DecodeManifest.
func verifyBackupManifest(manifestPath string, files []string, requireMail bool) error {
	manifest, err := backupauto.LoadManifest(manifestPath) // #nosec G703 -- root explicitly selects the backup manifest.
	if err != nil {
		return err
	}
	if requireMail && (manifest.Scope != backupauto.ScopeComplete || !manifest.Consistent) {
		return fmt.Errorf("mail restore requires a complete maintenance-window backup")
	}
	entries := map[string]backupauto.Artifact{}
	for _, entry := range manifest.Artifacts {
		if filepath.Base(entry.Name) != entry.Name || entry.Name == "." {
			return fmt.Errorf("invalid artifact name")
		}
		entries[entry.Name] = entry
	}
	for _, name := range files {
		if name == "" {
			continue
		}
		entry, ok := entries[filepath.Base(name)]
		if !ok {
			return fmt.Errorf("artifact absent from manifest")
		}
		info, err := os.Stat(name) // #nosec G703 -- validates an explicitly selected restore artifact against its manifest.
		if err != nil {
			return err
		}
		hash, err := fileSHA256(name)
		if err != nil {
			return err
		}
		if hash != entry.SHA256 || info.Size() != entry.Size {
			return fmt.Errorf("backup checksum/size mismatch for %s", filepath.Base(name))
		}
	}

	provided := map[string]struct{}{}
	for _, name := range files {
		if name != "" {
			provided[filepath.Base(name)] = struct{}{}
		}
	}
	for name := range entries {
		if _, ok := provided[name]; !ok {
			return fmt.Errorf("manifest artifact not provided")
		}
	}
	return nil
}

// validateRestoreArchive rejects traversal, devices, and escaping links before
// the privileged extractor sees any archive member.
func validateRestoreArchive(filename string) error {
	file, err := os.Open(filename) // #nosec G304 G703 -- root explicitly chooses the archive to validate.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe archive member")
		}
		if !managedArchiveName(name) {
			return fmt.Errorf("archive member outside managed backup roots: %s", name)
		}

		switch header.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink, tar.TypeLink:
			target := header.Linkname
			if path.IsAbs(target) {
				target = strings.TrimPrefix(target, "/")
			} else {
				target = path.Clean(path.Join(path.Dir(name), target)) // #nosec G305 -- checked against the managed-root allowlist immediately below.
			}
			if !managedArchiveName(target) {
				return fmt.Errorf("escaping archive link")
			}
		default:
			return fmt.Errorf("unsupported archive member type")
		}
	}
}

func managedArchiveName(name string) bool {
	for _, root := range []string{"etc/urmail", "etc/postfix/urmail", "etc/opendkim", "etc/letsencrypt", "var/lib/urmail", "var/lib/urmail-agent/certs", "var/mail"} {
		if name == root || strings.HasPrefix(name, root+"/") {
			return true
		}
	}
	return false
}
