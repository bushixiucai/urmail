package backupauto

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReadSpool inspects the root-produced backup spool and returns both the
// loaded manifest (preserving Version, LayoutVersion, Slots, and Scope)
// and the local plaintext artifacts with verified hashes.
func ReadSpool(dir, stamp string) (*Manifest, []LocalArtifact, error) {
	names := []struct{ kind, name string }{
		{KindPgDump, "urmail-" + stamp + ".dump"},
		{KindConfigTar, "urmail-config-" + stamp + ".tar.gz"},
		{KindMailTar, "urmail-mail-" + stamp + ".tar.gz"},
	}
	var artifacts []LocalArtifact
	includedMail := false
	for i, candidate := range names {
		path := filepath.Join(dir, candidate.name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) && candidate.kind == KindMailTar {
				continue // metadata-only backup: the CLI omits the mail tar
			}
			return nil, nil, fmt.Errorf("backup artifact %s: %w", candidate.name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("backup artifact %s is not a regular file", candidate.name)
		}
		data, err := os.Open(path) // #nosec G304 -- fixed spool path assembled from the fixed stamp.
		if err != nil {
			return nil, nil, err
		}
		h := sha256.New()
		if _, err := io.Copy(h, data); err != nil {
			_ = data.Close()
			return nil, nil, err
		}
		_ = data.Close()
		artifacts = append(artifacts, LocalArtifact{
			Name: candidate.name, Path: path, Kind: candidate.kind,
			Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil)),
		})
		if i == 2 {
			includedMail = true
		}
	}
	if len(artifacts) < 2 {
		return nil, nil, fmt.Errorf("backup spool %s lacks the dump or config archive", dir)
	}
	if !includedMail {
		return nil, nil, fmt.Errorf("backup spool %s lacks the mail archive", dir)
	}
	manifest, err := LoadManifest(filepath.Join(dir, ManifestName(stamp)))
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("read spool manifest: %w", err)
	}
	return manifest, artifacts, nil
}

// ReadSpoolArtifacts inspects the root-produced backup spool after the
// agent ran the fixed "urmailctl backup" command and returns the plaintext
// artifacts for one stamp, with the SHA-256 sums restore verifies later.
// It is read-only: a missing artifact is an error and never a deletion.
func ReadSpoolArtifacts(dir, stamp string) ([]LocalArtifact, string, bool, error) {
	manifest, artifacts, err := ReadSpool(dir, stamp)
	if err != nil {
		return nil, "", false, err
	}
	scope := ScopeComplete
	consistent := true
	if manifest != nil {
		scope = manifest.Scope
		consistent = manifest.Consistent
	}
	return artifacts, scope, consistent, nil
}
