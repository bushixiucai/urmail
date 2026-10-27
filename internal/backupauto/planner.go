package backupauto

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ScopeComplete marks a backup that contains the PostgreSQL dump (platform,
// tenants, billing, collaboration data) AND all mail storage.
const ScopeComplete = "local-mail-and-metadata"

// Artifact kinds recorded in the manifest and used by restore tooling to
// locate the database dump without parsing file names.
const (
	KindPgDump    = "pg_dump"
	KindConfigTar = "config_tar"
	KindMailTar   = "mail_tar"
)

// PlannerConfig is the host snapshot the planner needs. It is a plain struct
// so PlanBackupSet stays a pure function (AGENTS.md: generators are pure).
type PlannerConfig struct {
	// OutDir is the local spool directory (/var/backups/urmail).
	OutDir string
	// Stamp is the UTC backup stamp, format 20060102T150405Z.
	Stamp string
	// IncludeMail selects the Maildir/archive tar archives.
	IncludeMail bool
	// Configured reports that the host finished setup (a dump always exists).
	Configured bool
}

// PlanEntry is one artifact the backup step must produce.
type PlanEntry struct {
	Kind string
	// Name is the base file name inside OutDir.
	Name string
}

// PlanBackupSet assembles the backup set for one run:
//
//   - the PostgreSQL dump (single source of truth: tenants, mailboxes,
//     aliases, quotas, contacts, calendars and every collaboration table
//     that lives in PG since migrations 0108-0111),
//   - the configuration/certificate tar,
//   - the Maildir + archive spool tar when IncludeMail is set,
//   - the manifest and checksum files themselves.
//
// Collaboration data is intentionally NOT archived separately: it lives in
// PostgreSQL (contact groups 0108/0109/0110, calendar events 0111) and is
// captured by the dump; a second copy would double the archive size and
// risk divergence.
func PlanBackupSet(cfg PlannerConfig) ([]PlanEntry, error) {
	if cfg.Stamp == "" {
		return nil, fmt.Errorf("backup stamp is required")
	}
	if cfg.OutDir == "" {
		return nil, fmt.Errorf("backup output directory is required")
	}
	entries := []PlanEntry{
		{Kind: KindPgDump, Name: "urmail-" + cfg.Stamp + ".dump"},
		{Kind: KindConfigTar, Name: "urmail-config-" + cfg.Stamp + ".tar.gz"},
	}
	if cfg.IncludeMail {
		entries = append(entries, PlanEntry{Kind: KindMailTar, Name: "urmail-mail-" + cfg.Stamp + ".tar.gz"})
	}
	return entries, nil
}

// EncryptedName returns the offsite object name for a plaintext artifact.
func EncryptedName(plaintextName string) string {
	return plaintextName + ".age"
}

// OffsitePrefix joins an offsite key prefix with an object name.
func OffsitePrefix(prefix, name string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return name
	}
	return prefix + "/" + strings.TrimLeft(name, "/")
}

// StampFor returns the backup stamp for a UTC time.
func StampFor(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// ManifestName returns the manifest file name for a stamp.
func ManifestName(stamp string) string {
	return "urmail-" + stamp + ".manifest.json"
}

// ArtifactByName indexes a manifest by plaintext artifact name.
func ArtifactByName(m *Manifest) (map[string]Artifact, error) {
	out := make(map[string]Artifact, len(m.Artifacts))
	for _, a := range m.Artifacts {
		if filepath.Base(a.Name) != a.Name || a.Name == "." || a.Name == "" {
			return nil, fmt.Errorf("invalid artifact name %q", a.Name)
		}
		if _, dup := out[a.Name]; dup {
			return nil, fmt.Errorf("duplicate artifact %q", a.Name)
		}
		out[a.Name] = a
	}
	return out, nil
}
