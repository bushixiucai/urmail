// Package miadbinventory inspects legacy MIAB metadata without target writes.
package miadbinventory

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed source.py
var sourceReader string

// Options names explicit source files; no target database is opened.
type Options struct{ ConfigPath, SQLitePath, MappingPath string }

// Issue contains only controlled codes and opaque source row references.
type Issue struct {
	Code      string `json:"code"`
	SourceRef string `json:"source_ref,omitempty"`
}

// Domain describes an opaque domain and an explicitly selected mapping entry.
type Domain struct {
	SourceRef      string `json:"source_ref"`
	MappingRef     string `json:"mapping_ref,omitempty"`
	TargetGroupRef string `json:"target_group_ref,omitempty"`
	Mailboxes      int    `json:"mailboxes"`
}

// Report is safe for normal logs: it contains no address, password hash, secret or config value.
type Report struct {
	Version                       int              `json:"version"`
	Mode                          string           `json:"mode"`
	Status                        string           `json:"status"`
	Changed                       bool             `json:"changed"`
	ApplyAvailable                bool             `json:"apply_available"`
	CutoverReady                  bool             `json:"cutover_ready"`
	Schema                        string           `json:"schema"`
	MappingVersion                int              `json:"mapping_version"`
	ReviewSnapshotBound           bool             `json:"review_snapshot_bound"`
	BoundReviewPolicies           []string         `json:"bound_review_policies"`
	ReviewedEquivalentAliasGroups int              `json:"reviewed_equivalent_alias_groups"`
	SnapshotSHA256                string           `json:"reviewed_snapshot_sha256,omitempty"`
	Counts                        map[string]int64 `json:"counts"`
	ExistingServicePolicy         string           `json:"existing_service_policy"`
	TargetTenantGroups            int              `json:"target_tenant_groups"`
	UnlimitedQuotaMapping         string           `json:"unlimited_quota_mapping"`
	UnlimitedQuotaMailboxes       int              `json:"unlimited_quota_mailboxes"`
	Domains                       []Domain         `json:"domains"`
	Blockers                      []Issue          `json:"blockers"`
	Warnings                      []Issue          `json:"warnings"`
}

type sourceUser struct {
	ID            int64  `json:"id"`
	Email         string `json:"email"`
	HashSupported bool   `json:"hash_supported"`
	Privileges    string `json:"privileges"`
	Quota         string `json:"quota"`
	ExtraPresent  bool   `json:"extra_present"`
}
type sourceAlias struct {
	Table            string  `json:"table"`
	ID               int64   `json:"id"`
	Source           string  `json:"source"`
	Destination      string  `json:"destination"`
	PermittedSenders *string `json:"permitted_senders"`
}
type sourceMFA struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"user_id"`
	Type   string `json:"type"`
}
type snapshot struct {
	SchemaKnown         bool             `json:"schema_known"`
	ObjectLimitExceeded bool             `json:"object_limit_exceeded"`
	LimitExceeded       bool             `json:"limit_exceeded"`
	Counts              map[string]int64 `json:"counts"`
	SnapshotSHA256      string           `json:"reviewed_snapshot_sha256"`
	Users               []sourceUser     `json:"users"`
	Aliases             []sourceAlias    `json:"aliases"`
	MFA                 []sourceMFA      `json:"mfa"`
}
type mapping struct {
	Version                           int             `json:"version"`
	Domains                           []domainMapping `json:"domains"`
	ReviewedSnapshotSHA256            string          `json:"reviewed_snapshot_sha256,omitempty"`
	LegacyPrivileges                  string          `json:"legacy_privileges,omitempty"`
	AllowSameTenantCrossDomainAliases bool            `json:"allow_same_tenant_cross_domain_aliases,omitempty"`
	AllowEquivalentAliasDedup         bool            `json:"allow_equivalent_alias_dedup,omitempty"`
}
type domainMapping struct {
	Domain    string `json:"domain"`
	TenantRef string `json:"tenant_ref"`
}

var tenantReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var configKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("source must be an existing regular file; symlinks are not accepted")
	}
	f, err := os.Open(path) // #nosec G304 G703 -- explicit read-only source path, never executed or modified.
	if err != nil {
		return nil, errors.New("source file cannot be read")
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("source file identity changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("source file is unreadable or exceeds its bound")
	}
	return raw, nil
}

func parseSourceConfig(raw []byte) error {
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || !configKey.MatchString(key) {
			return errors.New("source config requires literal KEY=value assignments")
		}
		if _, exists := values[key]; exists {
			return errors.New("source config contains duplicate keys")
		}
		if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') {
			if value[len(value)-1] != value[0] {
				return errors.New("source config contains unsupported quoting")
			}
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	root := values["STORAGE_ROOT"]
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, "\x00\r\n$`\\\"'") {
		return errors.New("source STORAGE_ROOT must be an explicit literal absolute path")
	}
	return nil
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16<<20 {
		return 0, errors.New("source snapshot exceeds bound")
	}
	return b.Buffer.Write(p)
}

// Inspect obtains one SQLite read snapshot and analyzes explicit mappings.
// It does not load platform configuration, open PostgreSQL, or invoke an agent.
func Inspect(ctx context.Context, options Options) (Report, error) {
	report := Report{Version: 1, Mode: "dry_run", Status: "blocked", Counts: map[string]int64{}, Domains: []Domain{}, Blockers: []Issue{}, Warnings: []Issue{}}
	config, err := readRegular(options.ConfigPath, 64<<10)
	if err != nil {
		return report, err
	}
	if err = parseSourceConfig(config); err != nil {
		return report, err
	}
	info, err := os.Lstat(options.SQLitePath)
	if err != nil || !info.Mode().IsRegular() {
		return report, errors.New("source SQLite must be an existing regular file")
	}
	if info.Size() > 512<<20 {
		report.Schema = "not_inspected"
		report.Blockers = append(report.Blockers, Issue{Code: "SOURCE_METADATA_FILE_LIMIT_EXCEEDED"})
		return report, nil
	}
	// Even read-only SQLite can maintain WAL shared-memory read marks. Do not
	// touch a live sidecar layout: require a separately authorized consistent
	// snapshot instead of ignoring WAL or creating a source SHM file.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, statErr := os.Lstat(options.SQLitePath + suffix); statErr == nil {
			report.Schema = "not_inspected"
			report.Blockers = append(report.Blockers, Issue{Code: "SQLITE_SIDECAR_REQUIRES_CONSISTENT_READONLY_SNAPSHOT"})
			return report, nil
		} else if !os.IsNotExist(statErr) {
			return report, errors.New("SQLite sidecar state cannot be verified")
		}
	}
	absolute, err := filepath.Abs(options.SQLitePath)
	if err != nil {
		return report, errors.New("source SQLite path is invalid")
	}
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(timeout, "/usr/bin/python3", "-I", "-S", "-B", "-c", sourceReader, absolute) // #nosec G204 G702 -- fixed isolated interpreter and embedded reader; source path is a separate read-only argument.
	var output boundedBuffer
	command.Stdout = &output
	command.Stderr = io.Discard
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1"}
	if err = command.Run(); err != nil {
		return report, errors.New("consistent read-only SQLite snapshot failed; no target changed")
	}
	var source snapshot
	if err = json.Unmarshal(output.Bytes(), &source); err != nil {
		return report, errors.New("source snapshot has unsupported field types")
	}
	var selected mapping
	if options.MappingPath != "" {
		raw, readErr := readRegular(options.MappingPath, 1<<20)
		if readErr != nil {
			return report, readErr
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&selected); err != nil {
			return report, errors.New("mapping file is not the supported explicit format")
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return report, errors.New("mapping file contains trailing data")
		}
		if selected.Version != 1 && selected.Version != 2 {
			return report, errors.New("mapping file version is unsupported")
		}
	}
	// The fingerprint covers schema and reviewed core fields only, not MFA seeds,
	// Maildir, DNS, certificates, groupware or the complete machine snapshot.
	report = analyze(source, selected)
	after, statErr := os.Lstat(options.SQLitePath)
	if statErr != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		report.Status = "blocked"
		report.Blockers = append(report.Blockers, Issue{Code: "SOURCE_CHANGED_DURING_INSPECTION"})
	}
	return report, nil
}

func rowRef(table string, id int64) string { return fmt.Sprintf("%s:%d", table, id) }
