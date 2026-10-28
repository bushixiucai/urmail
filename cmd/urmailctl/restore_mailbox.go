package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

type mailboxRestorePlan struct {
	TenantID      int64  `json:"tenant_id"`
	MailboxID     int64  `json:"mailbox_id"`
	Address       string `json:"address"`
	ArchiveHash   string `json:"archive_sha256"`
	ManifestHash  string `json:"manifest_sha256"`
	CurrentHash   string `json:"current_mailbox_sha256"`
	CurrentBytes  int64  `json:"current_bytes"`
	RestoreBytes  int64  `json:"restore_bytes"`
	Messages      int    `json:"messages"`
	Quota         int64  `json:"quota_bytes"`
	Folder        string `json:"recovered_folder"`
	ArchivePrefix string `json:"archive_prefix"`
	ReviewHash    string `json:"review_sha256,omitempty"`
}

// mailbox restore only appends a dedicated folder. It never replaces existing
// messages, Dovecot UID databases, or another mailbox's directory. A complete
// private rollback copy is durable before the single atomic directory rename.
func runMailboxRestore(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("restore mailbox", flag.ContinueOnError)
	manifest := flags.String("manifest", "", "complete maintenance manifest")
	archive := flags.String("mail", "", "decrypted Maildir tar.gz")
	tenant := flags.Int64("tenant-id", 0, "current tenant identity")
	mailbox := flags.Int64("mailbox-id", 0, "current mailbox identity")
	address := flags.String("address", "", "current immutable mailbox address")
	apply := flags.String("apply-review", "", "exact review_sha256 to apply")
	backup := flags.String("rollback-dir", "", "new private directory for rollback copy")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *tenant <= 0 || *mailbox <= 0 || *address == "" || *manifest == "" || *archive == "" {
		return errors.New("mailbox restore requires --tenant-id --mailbox-id --address --mail --manifest")
	}
	if os.Geteuid() != 0 {
		return errors.New("mailbox restore requires root")
	}
	maintenanceLock, lockErr := backupLock(backupMaintenanceRoot, "operation.lock")
	if lockErr != nil {
		return lockErr
	}
	defer func() { _ = maintenanceLock.Close() }()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	identity, err := store.New(tx).PlatformMailboxRestoreIdentity(ctx, store.PlatformMailboxRestoreIdentityParams{TenantID: *tenant, ID: *mailbox})
	if err != nil {
		return fmt.Errorf("current mailbox/storage identity: %w", err)
	}
	if identity.Address != *address || identity.Address != identity.LocalPart+"@"+identity.Fqdn {
		return errors.New("mailbox storage identity mismatch")
	}
	if *apply != "" {
		if *backup == "" {
			return errors.New("apply requires --rollback-dir")
		}
		// Includes schedulers, HTTP, LMTP and IMAP writers; missing required units
		// fail closed. Units stay stopped on both success and failure.
		if err = stopRestoreWriters(ctx, stopSystemdRestoreWriter); err != nil {
			return err
		}
	}
	root := filepath.Join(cfg.StorageRoot, "mail", "mailboxes", identity.Fqdn, identity.LocalPart)
	archivePrefix := path.Join(strings.TrimPrefix(cfg.StorageRoot, "/"), "mail", "mailboxes", identity.Fqdn, identity.LocalPart)
	plan, err := reviewMailboxRestore(*manifest, *archive, root, archivePrefix, identity)
	if err != nil {
		return err
	}
	if *apply != "" {
		if plan.ReviewHash != *apply {
			return errors.New("review is stale or does not match mailbox/archive; review again with writers stopped")
		}
		if err = applyMailboxRestore(plan, *archive, root, *backup); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

func reviewMailboxRestore(manifestPath, archive, target, archivePrefix string, id store.PlatformMailboxRestoreIdentityRow) (mailboxRestorePlan, error) {
	if filepath.Base(id.Fqdn) != id.Fqdn || filepath.Base(id.LocalPart) != id.LocalPart || id.LocalPart == "." || id.Fqdn == "." || id.Address != id.LocalPart+"@"+id.Fqdn {
		return mailboxRestorePlan{}, errors.New("invalid immutable storage identity")
	}
	p := mailboxRestorePlan{TenantID: id.TenantID, MailboxID: id.ID, Address: id.Address, Quota: id.QuotaBytes, ArchivePrefix: archivePrefix}
	manifest, err := backupauto.LoadManifest(manifestPath)
	if err != nil {
		return p, err
	}
	if manifest.Scope != backupauto.ScopeComplete || !manifest.Consistent {
		return p, errors.New("single mailbox restore requires complete maintenance backup")
	}
	p.ArchiveHash, err = fileSHA256(archive)
	if err != nil {
		return p, err
	}
	info, err := os.Lstat(archive)
	if err != nil {
		return p, err
	}
	found := false
	for _, a := range manifest.Artifacts {
		if a.Name == filepath.Base(archive) && a.SHA256 == p.ArchiveHash && a.Size == info.Size() {
			found = true
		}
	}
	if !found {
		return p, errors.New("mail artifact checksum/size not proved by manifest")
	}
	p.ManifestHash, err = fileSHA256(manifestPath)
	if err != nil {
		return p, err
	}
	if err = mailboxSafeAncestry(target); err != nil {
		return p, err
	}
	p.CurrentHash, p.CurrentBytes, err = mailboxTreeDigest(target)
	if err != nil {
		return p, err
	}
	prefix := archivePrefix
	err = walkMailboxArchive(archive, prefix, func(_ string, h *tar.Header, _ io.Reader) error {
		if p.Quota != -1 && h.Size > p.Quota-p.CurrentBytes-p.RestoreBytes {
			return errors.New("restored messages exceed current mailbox quota")
		}
		p.RestoreBytes += h.Size
		p.Messages++
		return nil
	})
	if err != nil {
		return p, err
	}
	if p.Messages == 0 {
		return p, errors.New("backup has no messages for the current immutable mailbox storage identity")
	}
	p.Folder = ".Recovered-" + p.ArchiveHash[:16]
	if _, err = os.Lstat(filepath.Join(target, p.Folder)); !errors.Is(err, fs.ErrNotExist) {
		return p, errors.New("recovered folder already exists; refusing duplicate import")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	digest := sha256.Sum256(raw)
	p.ReviewHash = hex.EncodeToString(digest[:])
	return p, nil
}

func mailboxSafeAncestry(target string) error {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("mailbox ancestry contains a non-directory or symlink")
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}

func mailboxTreeDigest(directory string) (string, int64, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = root.Close() }()
	hash := sha256.New()
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("mailbox tree contains links or special files")
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", name, info.Mode(), info.Size())
		if info.IsDir() {
			return nil
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		n, err := io.Copy(hash, file)
		total += n
		return err
	})
	return hex.EncodeToString(hash.Sum(nil)), total, err
}

func walkMailboxArchive(archive, prefix string, visit func(string, *tar.Header, io.Reader) error) error {
	file, err := os.Open(archive) // #nosec G304 -- explicit root-selected artifact already checked against the manifest.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(path.Clean(h.Name), "./")
		if path.IsAbs(h.Name) || name == ".." || strings.HasPrefix(name, "../") {
			return errors.New("unsafe mailbox archive path")
		}
		if !strings.HasPrefix(name, prefix+"/") {
			continue
		}
		relative := strings.TrimPrefix(name, prefix+"/")
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return errors.New("selected mailbox archive contains non-regular data")
		}
		parts := strings.Split(relative, "/")
		message := (len(parts) == 2 && (parts[0] == "cur" || parts[0] == "new")) || (len(parts) == 3 && strings.HasPrefix(parts[0], ".") && (parts[1] == "cur" || parts[1] == "new"))
		if !message {
			continue
		} // Never restore UID state, stale indexes, sieve or secrets.
		if seen[relative] {
			return errors.New("duplicate message archive path")
		}
		seen[relative] = true
		if err = visit(relative, h, tr); err != nil {
			return err
		}
	}
}

func applyMailboxRestore(p mailboxRestorePlan, archive, target, rollback string) error {
	return applyMailboxRestoreFiles(p, archive, target, rollback, validatePrivateBackupDirectory)
}

func applyMailboxRestoreFiles(p mailboxRestorePlan, archive, target, rollback string, validateRollback func(string) error) error {
	if err := mailboxSafeAncestry(target); err != nil {
		return err
	}
	if err := validateRollback(filepath.Dir(rollback)); err != nil {
		return err
	}
	rollbackAbs, err := filepath.Abs(rollback)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if rollbackAbs == targetAbs || strings.HasPrefix(rollbackAbs, targetAbs+string(filepath.Separator)) {
		return errors.New("rollback copy must be outside the target mailbox")
	}
	if err = os.Mkdir(rollback, 0o700); err != nil {
		return fmt.Errorf("create fresh rollback directory: %w", err)
	}
	// The durable receipt also identifies which exact snapshot the rollback
	// directory contains. Existing files are copied, never renamed or removed.
	if err = copyMailboxRollback(target, filepath.Join(rollback, "mailbox")); err != nil {
		return err
	}
	if err = syncMailboxTreeDirectories(filepath.Join(rollback, "mailbox")); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err = writeSyncedExclusive(filepath.Join(rollback, "review.json"), strings.NewReader(string(raw)), 0o600); err != nil {
		return err
	}
	current, _, err := mailboxTreeDigest(target)
	if err != nil {
		return err
	}
	if current != p.CurrentHash {
		return errors.New("mailbox changed during rollback backup; writers are not isolated")
	}
	// Work under the same filesystem so rename is atomic and never needs a
	// copy-over-existing fallback. The destination must not exist.
	stage, err := os.MkdirTemp(target, ".restore-stage-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	owner, err := os.Stat(target)
	if err != nil {
		return err
	}
	stat, ok := owner.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("mailbox ownership unavailable")
	}
	for _, dir := range []string{"cur", "new", "tmp"} {
		if err = os.Mkdir(filepath.Join(stage, dir), 0o700); err != nil {
			return err
		}
	}
	prefix := p.ArchivePrefix
	err = walkMailboxArchive(archive, prefix, func(relative string, h *tar.Header, r io.Reader) error {
		digest := sha256.Sum256([]byte(relative))
		name := hex.EncodeToString(digest[:]) + ",S=" + fmt.Sprint(h.Size)
		return writeSyncedExclusive(filepath.Join(stage, "new", name), io.LimitReader(r, h.Size), 0o600)
	})
	if err != nil {
		return err
	}
	stageRoot, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	var stageEntries []string
	err = fs.WalkDir(stageRoot.FS(), ".", func(name string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		stageEntries = append(stageEntries, name)
		return nil
	})
	if err == nil {
		// Change directory ownership last; the stage stays root-private while
		// message ownership is assigned. File descriptor Chown cannot escape.
		for i := len(stageEntries) - 1; i >= 0; i-- {
			file, openErr := stageRoot.Open(stageEntries[i])
			if openErr != nil {
				err = openErr
				break
			}
			changeErr := file.Chown(int(stat.Uid), int(stat.Gid))
			closeFileErr := file.Close()
			if err = errors.Join(changeErr, closeFileErr); err != nil {
				break
			}
		}
	}

	closeErr := stageRoot.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}

	// Recheck the source archive after staging to detect concurrent replacement.
	hash, err := fileSHA256(archive)
	if err != nil || hash != p.ArchiveHash {
		return errors.New("archive changed during import")
	}
	if err = syncMailboxTreeDirectories(stage); err != nil {
		return err
	}
	destination := filepath.Join(target, p.Folder)
	if _, err = os.Lstat(destination); !errors.Is(err, fs.ErrNotExist) {
		return errors.New("recovered folder destination already exists")
	}
	if err = os.Rename(stage, destination); err != nil {
		return err
	}
	if err = syncMailboxDirectory(target); err != nil {
		return err
	}
	return syncMailboxDirectory(rollback)
}

func copyMailboxRollback(source, destination string) error {
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = sourceRoot.Close() }()
	if err = os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = destinationRoot.Close() }()
	return fs.WalkDir(sourceRoot.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return destinationRoot.Mkdir(name, 0o700)
		}
		if !info.Mode().IsRegular() {
			return errors.New("rollback refuses links and special files")
		}
		file, err := sourceRoot.Open(name)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		target, err := destinationRoot.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(target, file)
		syncErr := target.Sync()
		closeErr := target.Close()
		return errors.Join(copyErr, syncErr, closeErr)
	})
}

func writeSyncedExclusive(name string, r io.Reader, mode fs.FileMode) error {
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode) // #nosec G304 -- exclusive files under the private root-controlled staging/rollback directory.
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, r)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}
func syncMailboxDirectory(name string) error {
	directory, err := os.Open(name) // #nosec G304 -- fsync only the already selected staging/rollback/mailbox directory.
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func syncMailboxTreeDirectories(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, name)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncMailboxDirectory(directories[i]); err != nil {
			return err
		}
	}
	return nil
}
