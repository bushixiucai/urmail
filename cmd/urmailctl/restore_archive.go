package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
)

// verifyBackupManifest verifies plaintext restore artifacts against the
// backup manifest. Versions 1, 2, and 3 are accepted: version 1 is the
// historical plaintext manifest; version 2 keeps the same PLAINTEXT name,
// size and SHA-256 fields per artifact (with extra encryption metadata);
// version 3 adds LayoutVersion and logical slots.
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

// validateRestoreArchive validates archive members for backward-compatible invocations.
func validateRestoreArchive(filename string) error {
	var cfg *config.Config
	if c, err := config.Load(); err == nil {
		cfg = c
	}
	return validateRestoreArchiveWithManifest(filename, filepath.Base(filename), nil, cfg, nil)
}

// validateRestoreArchiveWithManifest rejects traversal, absolute paths, and escaping links
// before extraction, validating against manifest v3 logical slots or allowed managed roots.
func validateRestoreArchiveWithManifest(filename, artifact string, manifest *backupauto.Manifest, cfg *config.Config, legacySlotMap map[string]string) error {
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
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return fmt.Errorf("unsafe archive member: %s", name)
		}
		slotName, prefix, kind, ok := matchArchiveMemberToSlot(artifact, name, manifest, legacySlotMap, cfg)
		if !ok {
			return fmt.Errorf("archive member outside managed backup roots: %s", name)
		}

		_, trustedKind := targetDirectoryForSlot(slotName, cfg, "")
		if trustedKind == "" || (kind != "" && kind != trustedKind) {
			return fmt.Errorf("unknown or inconsistent restore slot: %s", slotName)
		}
		kind = trustedKind
		if kind == "file" {
			if name != prefix {
				return fmt.Errorf("file slot %s only permits exact match, got descendant: %s", slotName, name)
			}
			if header.Typeflag != tar.TypeReg {
				return fmt.Errorf("file slot %s only permits regular file, got typeflag %c: %s", slotName, header.Typeflag, name)
			}
		}

		switch header.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink:
			target := header.Linkname
			if path.IsAbs(target) {
				return fmt.Errorf("escaping archive link: absolute link %s", target)
			}
			dest := path.Clean(path.Join(path.Dir(name), target)) // #nosec G305 -- checked against managed roots immediately below.
			if dest == ".." || strings.HasPrefix(dest, "../") || strings.Contains(dest, "/../") {
				return fmt.Errorf("escaping archive link")
			}
			if _, _, _, ok := matchArchiveMemberToSlot(artifact, dest, manifest, legacySlotMap, cfg); !ok {
				return fmt.Errorf("escaping archive link")
			}
		case tar.TypeLink:
			target := path.Clean(strings.TrimPrefix(header.Linkname, "/"))
			if target == "." || target == ".." || strings.HasPrefix(target, "../") || strings.Contains(target, "/../") {
				return fmt.Errorf("escaping archive hard link: %s", header.Linkname)
			}
			destSlot, _, _, ok := matchArchiveMemberToSlot(artifact, target, manifest, legacySlotMap, cfg)
			if !ok {
				return fmt.Errorf("escaping archive hard link outside roots: %s", target)
			}
			if destSlot != slotName {
				return fmt.Errorf("cross-slot hard link forbidden: %s -> %s", name, target)
			}
		default:
			return fmt.Errorf("unsupported archive member type %v for %s", header.Typeflag, name)
		}
	}
}

// matchArchiveMemberToSlot matches an archive member name against Manifest v3 slots,
// explicit legacy slot mappings, or historical default managed roots.
func matchArchiveMemberToSlot(artifact, name string, manifest *backupauto.Manifest, legacySlotMap map[string]string, cfg *config.Config) (slotName, archivePrefix, kind string, ok bool) {
	name = path.Clean(strings.TrimPrefix(name, "/"))
	if name == "." || name == "" {
		return "", "", "", false
	}

	// 1. Manifest v3 Logical Slots
	if manifest != nil && manifest.Version >= backupauto.ManifestVersion3 && len(manifest.Slots) > 0 {
		var bestSlot string
		var bestPrefix string
		var bestKind string
		for sName, slot := range manifest.Slots {
			if artifact != "" && slot.Artifact != "" && slot.Artifact != artifact {
				continue
			}
			_, trustedKind := targetDirectoryForSlot(sName, cfg, "")
			if trustedKind == "" || (slot.Kind != "" && slot.Kind != trustedKind) {
				return "", "", "", false
			}
			prefix := path.Clean(strings.TrimPrefix(slot.ArchivePath, "/"))
			if trustedKind == "file" && strings.HasPrefix(name, prefix+"/") {
				return "", "", "", false
			}
			if prefix == "" || prefix == "." {
				continue
			}
			if trustedKind == "file" {
				if name == prefix {
					if len(prefix) >= len(bestPrefix) {
						bestSlot = sName
						bestPrefix = prefix
						bestKind = "file"
					}
				}
				continue
			}
			if name == prefix || strings.HasPrefix(name, prefix+"/") {
				if len(prefix) > len(bestPrefix) {
					bestSlot = sName
					bestPrefix = prefix
					bestKind = trustedKind
				}
			}
		}
		if bestPrefix != "" {
			return bestSlot, bestPrefix, bestKind, true
		}
		// v3未匹配不可fallback legacy roots!
		return "", "", "", false
	}

	// 2. Explicit legacy slot mappings (--legacy-slot-map)
	if len(legacySlotMap) > 0 {
		var bestSlot string
		var bestPrefix string
		for k, v := range legacySlotMap {
			var candidateSlot, candidatePrefix string
			switch {
			case strings.HasPrefix(k, "mail/") || strings.HasPrefix(k, "config/"):
				candidateSlot = k
				candidatePrefix = path.Clean(strings.TrimPrefix(v, "/"))
			default:
				candidateSlot = v
				candidatePrefix = path.Clean(strings.TrimPrefix(k, "/"))
			}
			if name == candidatePrefix || strings.HasPrefix(name, candidatePrefix+"/") {
				if len(candidatePrefix) > len(bestPrefix) {
					bestSlot = candidateSlot
					bestPrefix = candidatePrefix
				}
			}
		}
		if bestPrefix != "" {
			kind := "dir"
			if strings.Contains(bestSlot, "key") || strings.HasSuffix(bestPrefix, ".key") {
				kind = "file"
			}
			return bestSlot, bestPrefix, kind, true
		}
	}

	// 3. Historical default roots & current config roots (v1/v2 backward compatibility)
	defaultRoots := []string{
		"etc/urmail",
		"var/lib/urmail-agent/certs",
		"etc/postfix/urmail",
		"etc/opendkim",
		"var/lib/urmail",
		"etc/letsencrypt",
		"var/mail/mail",
		"var/mail",
	}
	if cfg != nil {
		if cfg.StorageRoot != "" {
			cleanStorage := path.Clean(strings.TrimPrefix(cfg.StorageRoot, "/"))
			cleanStorageMail := path.Clean(path.Join(cleanStorage, "mail"))
			defaultRoots = append([]string{cleanStorageMail, cleanStorage}, defaultRoots...)
		}
		if cfg.MasterKeyPath != "" {
			cleanKey := path.Clean(strings.TrimPrefix(cfg.MasterKeyPath, "/"))
			defaultRoots = append([]string{cleanKey}, defaultRoots...)
		}
	}

	var bestRoot string
	for _, root := range defaultRoots {
		root = path.Clean(root)
		if name == root || strings.HasPrefix(name, root+"/") {
			if len(root) > len(bestRoot) {
				bestRoot = root
			}
		}
	}
	if bestRoot != "" {
		slot := rootToSlotName(bestRoot, cfg)
		kind := "dir"
		if strings.HasSuffix(bestRoot, ".key") || (cfg != nil && bestRoot == path.Clean(strings.TrimPrefix(cfg.MasterKeyPath, "/"))) {
			kind = "file"
		}
		return slot, bestRoot, kind, true
	}

	return "", "", "", false
}

func rootToSlotName(root string, cfg *config.Config) string {
	if cfg != nil {
		if root == path.Clean(strings.TrimPrefix(cfg.MasterKeyPath, "/")) {
			return backupauto.SlotConfigMasterKey
		}
		cleanStorageMail := path.Clean(path.Join(strings.TrimPrefix(cfg.StorageRoot, "/"), "mail"))
		if root == cleanStorageMail || root == path.Clean(strings.TrimPrefix(cfg.StorageRoot, "/")) {
			return backupauto.SlotMailStorage
		}
	}
	switch root {
	case "etc/urmail":
		return backupauto.SlotConfigOperator
	case "var/lib/urmail-agent/certs":
		return backupauto.SlotConfigAgentCerts
	case "etc/postfix/urmail":
		return backupauto.SlotConfigPostfix
	case "etc/opendkim":
		return backupauto.SlotConfigOpenDKIM
	case "etc/letsencrypt":
		return backupauto.SlotConfigLetsEncrypt
	case "var/lib/urmail/archive":
		return backupauto.SlotMailArchive
	case "var/lib/urmail/archive-spool":
		return backupauto.SlotMailSpool
	case "var/mail/mail", "var/mail":
		return backupauto.SlotMailStorage
	default:
		return root
	}
}

// targetDirectoryForSlot computes the target destination directory or file on the current
// machine, applying any root override (for isolated testing or custom chroot environments).
func targetDirectoryForSlot(slotName string, cfg *config.Config, rootOverride string) (string, string) {
	targetStorageRoot := "/var/mail"
	targetMasterKey := "/etc/urmail/master.key"
	if cfg != nil {
		if cfg.StorageRoot != "" {
			targetStorageRoot = cfg.StorageRoot
		}
		if cfg.MasterKeyPath != "" {
			targetMasterKey = cfg.MasterKeyPath
		}
	}
	var target string
	kind := "dir"
	switch slotName {
	case backupauto.SlotMailStorage:
		target = filepath.Join(targetStorageRoot, "mail")
	case backupauto.SlotMailArchive:
		target = "/var/lib/urmail/archive"
	case backupauto.SlotMailSpool:
		target = "/var/lib/urmail/archive-spool"
	case backupauto.SlotConfigMasterKey:
		target = targetMasterKey
		kind = "file"
	case backupauto.SlotConfigOperator:
		target = "/etc/urmail"
	case backupauto.SlotConfigAgentCerts:
		target = "/var/lib/urmail-agent/certs"
	case backupauto.SlotConfigPostfix:
		target = "/etc/postfix/urmail"
	case backupauto.SlotConfigOpenDKIM:
		target = "/etc/opendkim"
	case backupauto.SlotConfigCerts:
		target = "/var/lib/urmail/certs"
	case backupauto.SlotConfigLetsEncrypt:
		target = "/etc/letsencrypt"
	case backupauto.SlotConfigStorageSSL:
		target = filepath.Join(targetStorageRoot, "ssl")
	default:
		return "", ""
	}
	if rootOverride != "" {
		target = filepath.Join(rootOverride, strings.TrimPrefix(target, "/"))
	}
	return filepath.Clean(target), kind
}

// openSecureRoot ensures that dir is beneath a trusted anchor without traversing or containing
// any untrusted symlink segments. The anchor is opened with os.OpenRoot once, and each path component
// is traversed incrementally.
//
// Security boundary & contract:
//  1. The anchor directory itself must be trusted and administrator-owned (e.g. system root "/"
//     or an isolated root override directory).
//  2. Each component is checked without following links and its opened inode is
//     compared with the checked inode. OpenRoot may resolve relative links; the
//     identity comparison rejects a concurrent swap to a different directory.
//  3. Any attempt to traverse through symlinks within the managed path fails closed with an error.
func openSecureRoot(dir string, anchor string) (*os.Root, error) {
	cleanDir := filepath.Clean(dir)
	var baseAnchor string
	if anchor != "" {
		baseAnchor = filepath.Clean(anchor)
	} else {
		baseAnchor = "/"
	}

	// Canonicalize trusted anchor once (e.g. resolve macOS /tmp -> /private/tmp)
	if realAnchor, err := filepath.EvalSymlinks(baseAnchor); err == nil {
		baseAnchor = realAnchor
	}
	if anchor != "" && strings.HasPrefix(dir, anchor) {
		cleanDir = filepath.Join(baseAnchor, strings.TrimPrefix(cleanDir, anchor))
	}

	rel, err := filepath.Rel(baseAnchor, cleanDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("insecure target directory %s is outside trusted anchor %s", dir, baseAnchor)
	}

	// Open the trusted anchor as os.Root
	currRoot, err := os.OpenRoot(baseAnchor)
	if err != nil {
		return nil, fmt.Errorf("open trusted anchor %s: %w", baseAnchor, err)
	}

	if rel == "." || rel == "" {
		return currRoot, nil
	}

	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			_ = currRoot.Close()
			return nil, fmt.Errorf("traversal component forbidden in target path: %s", dir)
		}

		info, lstatErr := currRoot.Lstat(part)
		if lstatErr != nil {
			if errors.Is(lstatErr, os.ErrNotExist) {
				if mkErr := currRoot.Mkdir(part, 0o755); mkErr != nil && !errors.Is(mkErr, os.ErrExist) {
					_ = currRoot.Close()
					return nil, fmt.Errorf("mkdir component %s in root: %w", part, mkErr)
				}
				info, lstatErr = currRoot.Lstat(part)
				if lstatErr != nil {
					_ = currRoot.Close()
					return nil, fmt.Errorf("stat newly created component %s: %w", part, lstatErr)
				}
			} else {
				_ = currRoot.Close()
				return nil, fmt.Errorf("lstat component %s: %w", part, lstatErr)
			}
		}

		if info.Mode()&os.ModeSymlink != 0 {
			_ = currRoot.Close()
			return nil, fmt.Errorf("insecure target directory: component %s is a symlink", part)
		}
		if !info.IsDir() {
			_ = currRoot.Close()
			return nil, fmt.Errorf("target component %s is not a directory", part)
		}

		nextRoot, openErr := openVerifiedRestoreComponent(currRoot, part, info)
		_ = currRoot.Close()
		if openErr != nil {
			return nil, fmt.Errorf("open root component %s: %w", part, openErr)
		}
		currRoot = nextRoot
	}

	return currRoot, nil
}

// openVerifiedRestoreComponent binds the opened directory to its no-follow observation.
func openVerifiedRestoreComponent(parent *os.Root, name string, expected os.FileInfo) (*os.Root, error) {
	opened, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := opened.Stat(".")
	if err != nil || !os.SameFile(expected, actual) {
		_ = opened.Close()
		return nil, fmt.Errorf("target directory identity changed while opening component %s", name)
	}
	return opened, nil
}

type pendingDirMeta struct {
	root    *os.Root
	relPath string
	header  *tar.Header
}

func applyDirMetadata(root *os.Root, relPath string, header *tar.Header) error {
	if err := root.Chmod(relPath, header.FileInfo().Mode().Perm()); err != nil {
		return fmt.Errorf("chmod dir %s: %w", relPath, err)
	}
	if header.Uid >= 0 && header.Gid >= 0 {
		if err := root.Chown(relPath, header.Uid, header.Gid); err != nil {
			if os.Geteuid() == 0 || (!os.IsPermission(err) && !errors.Is(err, syscall.EPERM)) {
				return fmt.Errorf("chown dir %s: %w", relPath, err)
			}
		}
	}
	if !header.ModTime.IsZero() {
		if err := root.Chtimes(relPath, header.ModTime, header.ModTime); err != nil {
			return fmt.Errorf("chtimes dir %s: %w", relPath, err)
		}
	}
	return nil
}

// extractRestoreArchive safely extracts archive members into their mapped slot destinations
// using os.OpenRoot to ensure no traversal, absolute escapes, or symlink escapes occur.
func extractRestoreArchive(archive, artifact string, manifest *backupauto.Manifest, cfg *config.Config, legacySlotMap map[string]string, rootOverride string) error {
	file, err := os.Open(archive) // #nosec G304 G703 -- root explicitly chooses the archive to extract.
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

	roots := make(map[string]*os.Root)
	defer func() {
		for _, r := range roots {
			_ = r.Close()
		}
	}()

	getRoot := func(dir string) (*os.Root, error) {
		if r, ok := roots[dir]; ok {
			return r, nil
		}
		r, err := openSecureRoot(dir, rootOverride)
		if err != nil {
			return nil, fmt.Errorf("open secure root %s: %w", dir, err)
		}
		roots[dir] = r
		return r, nil
	}

	var pendingDirs []pendingDirMeta

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return fmt.Errorf("unsafe archive member: %s", name)
		}

		slotName, prefix, kind, ok := matchArchiveMemberToSlot(artifact, name, manifest, legacySlotMap, cfg)
		if !ok {
			return fmt.Errorf("archive member outside managed backup roots: %s", name)
		}

		targetRoot, slotKind := targetDirectoryForSlot(slotName, cfg, rootOverride)
		if targetRoot == "" || (kind != "" && kind != slotKind) {
			return fmt.Errorf("unknown or inconsistent restore slot: %s", slotName)
		}
		if kind == "" {
			kind = slotKind
		}

		if kind == "file" {
			if header.Typeflag != tar.TypeReg {
				return fmt.Errorf("file slot only permits regular file: %s", name)
			}
			targetDir := filepath.Dir(targetRoot)
			fileName := filepath.Base(targetRoot)
			root, err := getRoot(targetDir)
			if err != nil {
				return err
			}

			// Atomic replacement inside root using O_CREAT|O_EXCL with no absolute paths
			tmpName := fmt.Sprintf(".restore-tmp-%s-%d", fileName, time.Now().UnixNano())
			tmpFile, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, header.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("create temp file in root for slot %s: %w", slotName, err)
			}
			cleanup := func() {
				_ = tmpFile.Close()
				_ = root.Remove(tmpName)
			}

			if _, err := io.Copy(tmpFile, io.LimitReader(reader, 10<<30)); err != nil { // #nosec G110 -- limit bounds decompression
				cleanup()
				return err
			}
			if err := tmpFile.Chmod(header.FileInfo().Mode().Perm()); err != nil {
				cleanup()
				return err
			}
			if err := preserveFileOwnership(tmpFile, header.FileInfo()); err != nil {
				cleanup()
				return err
			}
			if err := tmpFile.Sync(); err != nil {
				cleanup()
				return err
			}
			if err := tmpFile.Close(); err != nil {
				_ = root.Remove(tmpName)
				return err
			}
			_ = root.Chtimes(tmpName, header.ModTime, header.ModTime)

			// Atomic rename to replace target directly without prior remove (preserves old file if rename fails)
			if err := root.Rename(tmpName, fileName); err != nil {
				_ = root.Remove(tmpName)
				return fmt.Errorf("atomic rename file slot %s: %w", fileName, err)
			}
			continue
		}

		// Directory slot
		relPath := strings.TrimPrefix(name, prefix)
		relPath = strings.TrimPrefix(relPath, "/")
		if relPath == "" || relPath == "." {
			if header.Typeflag == tar.TypeDir {
				root, err := getRoot(targetRoot)
				if err != nil {
					return err
				}
				hdrCopy := *header
				pendingDirs = append(pendingDirs, pendingDirMeta{
					root:    root,
					relPath: ".",
					header:  &hdrCopy,
				})
			}
			continue
		}
		relPath = path.Clean(relPath)
		if path.IsAbs(relPath) || relPath == ".." || strings.HasPrefix(relPath, "../") {
			return fmt.Errorf("unsafe relative path in slot: %s", relPath)
		}

		root, err := getRoot(targetRoot)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(relPath, header.FileInfo().Mode().Perm()); err != nil {
				return fmt.Errorf("mkdir in root %s: %w", relPath, err)
			}
			hdrCopy := *header
			pendingDirs = append(pendingDirs, pendingDirMeta{
				root:    root,
				relPath: relPath,
				header:  &hdrCopy,
			})
		case tar.TypeReg:
			parent := filepath.Dir(relPath)
			if parent != "." {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return fmt.Errorf("mkdir parent in root %s: %w", parent, err)
				}
			}
			// Atomic replacement with new inode to prevent symlink/hardlink overwrite
			tmpDir := filepath.Dir(relPath)
			baseName := filepath.Base(relPath)
			tmpName := fmt.Sprintf(".restore-tmp-%s-%d", baseName, time.Now().UnixNano())
			tmpRel := path.Join(tmpDir, tmpName)
			out, err := root.OpenFile(tmpRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, header.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("create temp in root %s: %w", tmpRel, err)
			}
			cleanup := func() {
				_ = out.Close()
				_ = root.Remove(tmpRel)
			}
			if _, err := io.Copy(out, io.LimitReader(reader, 10<<30)); err != nil { // #nosec G110 -- limit bounds decompression
				cleanup()
				return err
			}
			if err := out.Chmod(header.FileInfo().Mode().Perm()); err != nil {
				cleanup()
				return err
			}
			if err := preserveFileOwnership(out, header.FileInfo()); err != nil {
				cleanup()
				return err
			}
			if err := out.Sync(); err != nil {
				cleanup()
				return err
			}
			if err := out.Close(); err != nil {
				_ = root.Remove(tmpRel)
				return err
			}
			_ = root.Chtimes(tmpRel, header.ModTime, header.ModTime)
			// Atomic rename directly without prior remove
			if err := root.Rename(tmpRel, relPath); err != nil {
				_ = root.Remove(tmpRel)
				return fmt.Errorf("rename in root %s -> %s: %w", tmpRel, relPath, err)
			}
		case tar.TypeSymlink:
			linkTarget := header.Linkname
			if path.IsAbs(linkTarget) {
				return fmt.Errorf("absolute symlink forbidden: %s", linkTarget)
			}
			dest := path.Clean(path.Join(path.Dir(relPath), linkTarget)) // #nosec G305 -- verified relative below
			if dest == ".." || strings.HasPrefix(dest, "../") {
				return fmt.Errorf("escaping symlink forbidden: %s -> %s", relPath, linkTarget)
			}
			parent := filepath.Dir(relPath)
			if parent != "." {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return err
				}
			}
			_ = root.Remove(relPath)
			if err := root.Symlink(linkTarget, relPath); err != nil {
				return fmt.Errorf("symlink in root %s -> %s: %w", relPath, linkTarget, err)
			}
		case tar.TypeLink:
			targetArchive := path.Clean(strings.TrimPrefix(header.Linkname, "/"))
			targetRel := strings.TrimPrefix(targetArchive, prefix)
			targetRel = strings.TrimPrefix(targetRel, "/")
			targetRel = path.Clean(targetRel)
			if targetRel == "." || targetRel == ".." || strings.HasPrefix(targetRel, "../") {
				return fmt.Errorf("invalid hard link target %s in slot %s", header.Linkname, slotName)
			}
			parent := filepath.Dir(relPath)
			if parent != "." {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return fmt.Errorf("mkdir parent for link in root %s: %w", parent, err)
				}
			}
			_ = root.Remove(relPath)
			if err := root.Link(targetRel, relPath); err != nil {
				return fmt.Errorf("hardlink in root %s: %w", relPath, err)
			}
		default:
			return fmt.Errorf("unsupported archive member type %v for %s", header.Typeflag, name)
		}
	}

	// Apply directory metadata in reverse order so children don't alter parent mtimes
	for i := len(pendingDirs) - 1; i >= 0; i-- {
		pd := pendingDirs[i]
		if err := applyDirMetadata(pd.root, pd.relPath, pd.header); err != nil {
			return err
		}
	}

	return nil
}

func managedArchiveName(name string) bool {
	var cfg *config.Config
	if c, err := config.Load(); err == nil {
		cfg = c
	}
	_, _, _, ok := matchArchiveMemberToSlot("", name, nil, nil, cfg)
	return ok
}
