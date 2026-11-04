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

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
)

func runBackup(ctx context.Context, args []string) (returnErr error) {
	if _, selected, err := selectedFileProfile(args); err != nil {
		return err
	} else if selected {
		return runV80Backup(ctx, args, os.Stdout)
	}
	if len(args) > 0 && (args[0] == "offsite" || args[0] == "upload" || args[0] == "verify-offsite" || args[0] == "decrypt") {
		return runBackupOffsite(ctx, args)
	}
	outDir := "/var/backups/urmail"
	includeMail, maintenance := false, false
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for i := 0; i < len(args); i++ {
		if args[i] == "--stamp" {
			if i+1 >= len(args) {
				return fmt.Errorf("--stamp requires a UTC generation stamp")
			}
			stamp = args[i+1] // #nosec G602 -- the preceding explicit bounds check rejects a missing value.
			i++
			parsed, parseErr := time.Parse("20060102T150405Z", stamp)
			if parseErr != nil || parsed.Format("20060102T150405Z") != stamp {
				return fmt.Errorf("invalid backup generation stamp")
			}
			continue
		}
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
	if backupWritersHeld() {
		return errors.New("backup refused during restore or upgrade hold")
	}
	if os.Geteuid() != 0 && os.Getenv("URMAIL_ALLOW_TEST_USER") == "" {
		return fmt.Errorf("backup must run as root")
	}
	if owned, _ := ctx.Value(backupOperationContext{}).(bool); !owned {
		operationLock, lockErr := backupLock(currentMaintenanceRoot(), "operation.lock")
		if lockErr != nil {
			return lockErr
		}
		defer func() { _ = operationLock.Close() }()
		if backupWritersHeld() {
			return errors.New("backup refused during restore or upgrade hold")
		}
		ctx = context.WithValue(ctx, backupOperationContext{}, true)
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

	configArchiveBase := filepath.Base(configArchive)
	slots := map[string]backupauto.Slot{
		backupauto.SlotConfigOperator: {
			Artifact:    configArchiveBase,
			ArchivePath: "etc/urmail",
			TargetPath:  "/etc/urmail",
			Kind:        "dir",
		},
		backupauto.SlotConfigMasterKey: {
			Artifact:    configArchiveBase,
			ArchivePath: strings.TrimPrefix(filepath.Clean(cfg.MasterKeyPath), "/"),
			TargetPath:  filepath.Clean(cfg.MasterKeyPath),
			Kind:        "file",
		},
		backupauto.SlotConfigAgentCerts: {
			Artifact:    configArchiveBase,
			ArchivePath: "var/lib/urmail-agent/certs",
			TargetPath:  "/var/lib/urmail-agent/certs",
			Kind:        "dir",
		},
	}
	optionalSlotPaths := map[string]struct{ slotName, kind string }{
		"etc/postfix/urmail":   {backupauto.SlotConfigPostfix, "dir"},
		"etc/opendkim":         {backupauto.SlotConfigOpenDKIM, "dir"},
		"var/lib/urmail/certs": {backupauto.SlotConfigCerts, "dir"},
		"etc/letsencrypt":      {backupauto.SlotConfigLetsEncrypt, "dir"},
		strings.TrimPrefix(filepath.Clean(filepath.Join(cfg.StorageRoot, "ssl")), "/"): {backupauto.SlotConfigStorageSSL, "dir"},
	}
	for p, s := range optionalSlotPaths {
		if _, err = os.Stat("/" + p); err == nil {
			slots[s.slotName] = backupauto.Slot{
				Artifact:    configArchiveBase,
				ArchivePath: p,
				TargetPath:  "/" + p,
				Kind:        s.kind,
			}
		}
	}

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

		mailArchiveBase := filepath.Base(mailArchive)
		slots[backupauto.SlotMailStorage] = backupauto.Slot{
			Artifact:    mailArchiveBase,
			ArchivePath: strings.TrimPrefix(filepath.Clean(filepath.Join(cfg.StorageRoot, "mail")), "/"),
			TargetPath:  filepath.Clean(filepath.Join(cfg.StorageRoot, "mail")),
			Kind:        "dir",
		}
		for _, p := range archivePaths {
			switch p {
			case "var/lib/urmail/archive":
				slots[backupauto.SlotMailArchive] = backupauto.Slot{
					Artifact:    mailArchiveBase,
					ArchivePath: p,
					TargetPath:  "/" + p,
					Kind:        "dir",
				}
			case "var/lib/urmail/archive-spool":
				slots[backupauto.SlotMailSpool] = backupauto.Slot{
					Artifact:    mailArchiveBase,
					ArchivePath: p,
					TargetPath:  "/" + p,
					Kind:        "dir",
				}
			}
		}
	}
	manifest := filepath.Join(outDir, "urmail-"+stamp+".manifest.json")
	if err = writeBackupManifest(manifest, scope, maintenance, artifacts, slots); err != nil {
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

type backupArtifact = backupauto.Artifact

func writeBackupManifest(path, scope string, consistent bool, files []string, slots ...map[string]backupauto.Slot) error {
	var sMap map[string]backupauto.Slot
	if len(slots) > 0 && slots[0] != nil {
		sMap = slots[0]
	} else {
		sMap = make(map[string]backupauto.Slot)
		for _, name := range files {
			base := filepath.Base(name)
			switch {
			case strings.Contains(base, "mail"):
				sMap[backupauto.SlotMailStorage] = backupauto.Slot{
					Artifact: base, ArchivePath: "var/mail/mail", TargetPath: "/var/mail/mail", Kind: "dir",
				}
			case strings.Contains(base, "config"):
				sMap[backupauto.SlotConfigOperator] = backupauto.Slot{
					Artifact: base, ArchivePath: "etc/urmail", TargetPath: "/etc/urmail", Kind: "dir",
				}
				sMap[backupauto.SlotConfigMasterKey] = backupauto.Slot{
					Artifact: base, ArchivePath: "etc/urmail/master.key", TargetPath: "/etc/urmail/master.key", Kind: "file",
				}
			default:
				sMap["file/"+base] = backupauto.Slot{
					Artifact: base, ArchivePath: base, TargetPath: "/" + base, Kind: "file",
				}
			}
		}
	}
	m := backupauto.Manifest{
		Version:       backupauto.ManifestVersion3,
		LayoutVersion: backupauto.LayoutVersion1,
		Scope:         scope,
		Consistent:    consistent,
		CreatedAt:     time.Now().UTC(),
		Slots:         sMap,
	}
	for _, name := range files {
		info, err := os.Stat(name) // #nosec G703 -- inspects a root-selected backup artifact to record its metadata.
		if err != nil {
			return err
		}
		hash, err := fileSHA256(name)
		if err != nil {
			return err
		}
		base := filepath.Base(name)
		kind := ""
		switch {
		case strings.HasSuffix(base, ".dump"):
			kind = backupauto.KindPgDump
		case strings.Contains(base, "config") && strings.HasSuffix(base, ".tar.gz"):
			kind = backupauto.KindConfigTar
		case strings.Contains(base, "mail") && strings.HasSuffix(base, ".tar.gz"):
			kind = backupauto.KindMailTar
		}
		entry := backupauto.Artifact{Name: base, SHA256: hash, Size: info.Size(), Mode: uint32(info.Mode().Perm()), Kind: kind}
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
	return pauseBackupWritersDurable(ctx, currentMaintenanceRoot(), systemBackupUnit)
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
