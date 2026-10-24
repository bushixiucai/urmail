package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

func runBackup(ctx context.Context, args []string) (returnErr error) {
	if _, selected, err := selectedFileProfile(args); err != nil {
		return err
	} else if selected {
		return runV80Backup(ctx, args, os.Stdout)
	}
	outDir := "/var/backups/urmail"
	includeMail, maintenance := false, false
	for i := 0; i < len(args); i++ {
		if args[i] == "--include-mail" {
			includeMail = true
		}
		if args[i] == "--maintenance" {
			maintenance = true
		}
		if args[i] == "--out-dir" && i+1 < len(args) {
			outDir = args[i+1]
			i++
		}
	}
	if includeMail && !maintenance {
		return fmt.Errorf("complete mail backups require --maintenance")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("backup must run as root")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// A full archive includes FORCE-RLS tables and the isolated Roundcube schema.
	// Root peer authentication supplies both without widening runtime roles.
	dbURL, err := backupPeerDatabaseURL(cfg.DatabaseURLMigrate)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil { // #nosec G703 -- destination is explicitly selected by the root administrator and checked as a real private directory.
		return err
	}
	if info, err := os.Lstat(outDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 { // #nosec G703 -- destination is explicitly selected by the root administrator and checked as a real private directory.
		return fmt.Errorf("backup directory must be a real directory")
	}
	if err = validatePrivateBackupDirectory(outDir); err != nil {
		return err
	}
	if err = os.Chmod(outDir, 0o700); err != nil { // #nosec G703 -- destination is explicitly selected by the root administrator and checked as a real private directory.
		return err
	}
	if maintenance {
		resume, err := pauseBackupWriters(ctx)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, resume()) }()
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	dump := filepath.Join(outDir, "urmail-"+stamp+".dump")
	cmd, err := backupDatabaseCommand(ctx, dbURL)
	if err != nil {
		return err
	}

	dumpFile, err := os.OpenFile(dump, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 G703 -- root-authorized private backup artifact, never opened by the postgres account.
	if err != nil {
		return err
	}
	defer func() { _ = dumpFile.Close() }()
	cmd.Stdout = dumpFile
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("database backup failed: %w", err)
	}
	if err = dumpFile.Sync(); err != nil {
		return err
	}
	if err = dumpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dump, 0o600); err != nil { // #nosec G703 -- root-authorized artifact is inside the private backup directory.
		return err
	}
	configArchive := filepath.Join(outDir, "urmail-config-"+stamp+".tar.gz")
	paths := []string{"etc/urmail", strings.TrimPrefix(cfg.MasterKeyPath, "/"), "var/lib/urmail-agent/certs"}
	optional := []string{"etc/postfix/urmail", "etc/opendkim", "var/lib/urmail/certs", "etc/letsencrypt", strings.TrimPrefix(filepath.Join(cfg.StorageRoot, "ssl"), "/")}
	if err = validateBackupInputs(paths); err != nil {
		return err
	}

	for _, path := range optional {
		if _, err = os.Stat("/" + path); err == nil {
			paths = append(paths, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	tarArgs := append([]string{"-czf", configArchive, "-C", "/", "--"}, paths...)
	cmd = exec.CommandContext(ctx, "tar", tarArgs...) // #nosec G204 G702 -- root CLI uses a fixed executable and separate validated path arguments.

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar config: %w", err)
	}
	if err := os.Chmod(configArchive, 0o600); err != nil { // #nosec G703 -- root-authorized artifact is inside the private backup directory.
		return err
	}
	artifacts := []string{dump, configArchive}
	scope := "metadata-only"
	if includeMail {
		mailArchive := filepath.Join(outDir, "urmail-mail-"+stamp+".tar.gz")
		archivePaths, archiveErr := archiveBackupPaths("/", cfg.ArchiveEnabled)
		if archiveErr != nil {
			return archiveErr
		}
		mailPaths := make([]string, 0, 1+len(archivePaths))
		mailPaths = append(mailPaths, strings.TrimPrefix(filepath.Join(cfg.StorageRoot, "mail"), "/"))
		mailPaths = append(mailPaths, archivePaths...)
		for _, path := range mailPaths {
			if _, err = os.Stat("/" + path); err != nil {
				return fmt.Errorf("required mail input %s: %w", path, err)
			}
		}
		cmd = exec.CommandContext(ctx, "tar", append([]string{"-czf", mailArchive, "-C", "/", "--"}, mailPaths...)...) // #nosec G204 G702 -- fixed tar command; root-selected storage paths are separate arguments.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			return fmt.Errorf("tar mail: %w", err)
		}
		if err = os.Chmod(mailArchive, 0o600); err != nil { // #nosec G703 -- root-authorized artifact is inside the private backup directory.
			return err
		}
		artifacts = append(artifacts, mailArchive)
		scope = "local-mail-and-metadata"
	}
	manifest := filepath.Join(outDir, "urmail-"+stamp+".manifest.json")
	if err = writeBackupManifest(manifest, scope, maintenance, artifacts); err != nil {
		return err
	}
	if err = writeChecksums(filepath.Join(outDir, "urmail-"+stamp+".sha256"), artifacts); err != nil {
		return err
	}
	fmt.Printf("backup complete (scope=%s, maintenance=%t)\n  manifest: %s\n", scope, maintenance, manifest)
	if !includeMail {
		fmt.Println("mailbox contents, archived message bodies and pending archive spool are NOT included")
	}

	return nil
}

func writeChecksums(path string, files []string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 G703 -- local artifact path is selected by the root CLI or its caller.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, name := range files {
		h, err := fileSHA256(name)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(f, "%s  %s\n", h, filepath.Base(name)); err != nil {
			return err
		}
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 G703 -- local artifact path is selected by the root CLI or its caller.
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type backupArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
}
type backupManifest struct {
	Version    int              `json:"version"`
	Scope      string           `json:"scope"`
	Consistent bool             `json:"consistent_maintenance"`
	CreatedAt  time.Time        `json:"created_at"`
	Artifacts  []backupArtifact `json:"artifacts"`
}

func writeBackupManifest(path, scope string, consistent bool, files []string) error {
	m := backupManifest{Version: 1, Scope: scope, Consistent: consistent, CreatedAt: time.Now().UTC()}
	for _, name := range files {
		info, err := os.Stat(name) // #nosec G703 -- inspects a root-selected backup artifact to record its metadata.
		if err != nil {
			return err
		}
		hash, err := fileSHA256(name)
		if err != nil {
			return err
		}
		entry := backupArtifact{Name: filepath.Base(name), SHA256: hash, Size: info.Size(), Mode: uint32(info.Mode().Perm())}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			entry.UID = stat.Uid
			entry.GID = stat.Gid
		}
		m.Artifacts = append(m.Artifacts, entry)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600) // #nosec G703 -- root-selected manifest destination inside the private backup directory.
}
func pauseBackupWriters(ctx context.Context) (func() error, error) {
	active := []string{}
	resume := func() error {
		var failures []error
		for i := len(active) - 1; i >= 0; i-- {
			if err := exec.CommandContext(context.Background(), "/usr/bin/systemctl", "start", active[i]).Run(); err != nil { // #nosec G204 G702 -- privileged local CLI; fixed executable and separate root-controlled arguments, no shell.
				failures = append(failures, fmt.Errorf("restart %s after backup: %w", active[i], err))
			}
		}
		return errors.Join(failures...)
	}
	if _, err := os.Stat("/usr/bin/systemctl"); err != nil {
		return nil, fmt.Errorf("maintenance requires systemd: %w", err)
	}
	for _, unit := range restoreWriterUnits {
		if exec.CommandContext(ctx, "/usr/bin/systemctl", "is-active", "--quiet", unit).Run() == nil { // #nosec G204 G702 -- privileged local CLI; fixed executable and separate root-controlled arguments, no shell.
			if err := exec.CommandContext(ctx, "/usr/bin/systemctl", "stop", unit).Run(); err != nil { // #nosec G204 G702 -- privileged local CLI; fixed executable and separate root-controlled arguments, no shell.
				_ = resume()
				return nil, fmt.Errorf("stop backup writer %s: %w", unit, err)
			}
			active = append(active, unit)
		}
	}
	return resume, nil
}

// validatePrivateBackupDirectory prevents a daemon-owned parent from swapping
// a root backup directory for a symlink while privileged tools write secrets.
func validatePrivateBackupDirectory(directory string) error {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current) // #nosec G703 -- walks the selected destination ancestry only to reject unsafe directories.
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup destination and its parents must be root-owned real directories")
		}
		if info.Mode().Perm()&0o022 != 0 && (current == absolute || info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("backup destination parent is writable by another user")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func validateBackupInputs(paths []string) error {
	for _, name := range paths {
		absolute := filepath.Join("/", name)
		if _, err := os.Stat(absolute); err != nil {
			return fmt.Errorf("required backup input %s: %w", absolute, err)
		}
	}
	return nil
}

// Archive copies and pending spool are data even after the feature is disabled.
// Never silently drop an existing spool from a complete maintenance backup.
func archiveBackupPaths(base string, enabled bool) ([]string, error) {
	var paths []string
	for _, relative := range []string{"var/lib/urmail/archive", "var/lib/urmail/archive-spool"} {
		info, err := os.Lstat(filepath.Join(base, relative)) // #nosec G304 G703 -- production uses only the two fixed data paths under root; tests provide an isolated root.
		if errors.Is(err, os.ErrNotExist) && (!enabled || strings.HasSuffix(relative, "archive-spool")) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("required archive backup input %s: %w", relative, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("archive backup input must be a real directory")
		}
		paths = append(paths, relative)
	}
	return paths, nil
}
