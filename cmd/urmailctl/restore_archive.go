package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func verifyBackupManifest(manifestPath string, files []string, requireMail bool) error {
	raw, err := os.ReadFile(manifestPath) // #nosec G304 G703 -- root explicitly selects the backup manifest.
	if err != nil {
		return err
	}
	var m backupManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m.Version != 1 {
		return fmt.Errorf("unsupported backup manifest version")
	}
	if requireMail && (m.Scope != "local-mail-and-metadata" || !m.Consistent) {
		return fmt.Errorf("mail restore requires a complete maintenance-window backup")
	}
	entries := map[string]backupArtifact{}
	for _, entry := range m.Artifacts {
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
