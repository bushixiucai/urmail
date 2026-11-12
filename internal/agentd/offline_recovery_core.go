package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

const (
	maxOfflineRecoverySnapshotBytes        = 64 << 20
	maxOfflineRecoverySnapshotContentBytes = 32 << 20
)

type offlineRecoveryHooks struct {
	fileWriter     func(string, []byte, os.FileMode, []string) (string, error)
	manifestWriter func(*ApplyManager) error
	pendingRemover func(string) error
}

func recoverPendingJournalOfflineCore(ctx context.Context, stateRoot, targetRoot string, fileWriter func(string, []byte, os.FileMode, []string) (string, error)) (string, error) {
	return recoverPendingJournalOfflineCoreWithHooks(ctx, stateRoot, targetRoot, offlineRecoveryHooks{fileWriter: fileWriter})
}

func recoverPendingJournalOfflineCoreWithHooks(ctx context.Context, stateRoot, targetRoot string, hooks offlineRecoveryHooks) (string, error) {
	if stateRoot == "" {
		stateRoot = StateRoot
	}
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot {
		return "", errors.New("state root must be clean absolute")
	}
	info, err := os.Lstat(stateRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return "", errors.New("state root must be private real directory")
	}
	pendingPath := filepath.Join(stateRoot, "pending.json")
	f, raw, pendingSnapshot, err := readPrivatePending(pendingPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return recoverPendingJournalOfflineData(ctx, stateRoot, targetRoot, pendingPath, raw, pendingSnapshot, hooks)
}

func recoverPendingJournalOfflineData(ctx context.Context, stateRoot, targetRoot, pendingPath string, raw []byte, pendingSnapshot savedFile, hooks offlineRecoveryHooks) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var journal applyJournal
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&journal); err != nil {
		return "", errors.New("pending journal malformed")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("pending journal has trailing data")
	}
	if journal.Version != ApplyJournalVersion || len(journal.Files) == 0 {
		return "", errors.New("only non-empty current-version journals are supported")
	}
	if len(journal.DNS) != 0 {
		return "", errors.New("offline recovery refuses DNS journal requiring signing/serial work")
	}
	paths := make([]string, 0, len(journal.Files))
	total := int64(len(raw))
	for p, sf := range journal.Files {
		clean, _, err := resolveAllowedPath(p, allowedWriteRoots)
		if err != nil || clean != p || strings.HasPrefix(p, "/etc/nsd/") || p == "/etc/dovecot/urmail-spam/spam-before.sieve" {
			return "", errors.New("journal has unsupported path requiring external action")
		}
		if len(sf.Content) > maxJournalFileBytes || sf.Mode&^uint32(0777) != 0 || sf.UID < 0 || sf.GID < 0 {
			return "", errors.New("journal file metadata unsafe")
		}
		total += int64(len(sf.Content))
		if total > maxPendingJournalBytes {
			return "", errors.New("journal exceeds size limit")
		}
		paths = append(paths, p)
	}
	if hooks.fileWriter == nil {
		hooks.fileWriter = writeFileAtomic
	}
	manager := &ApplyManager{root: stateRoot, targetRoot: targetRoot, run: func(context.Context, string, ...string) error {
		return errors.New("external commands disabled during offline recovery")
	}, fileWriter: hooks.fileWriter, prepareOnly: true, offlineRecovery: true}
	if hooks.manifestWriter != nil {
		manager.offlineManifestWriter = func() error { return hooks.manifestWriter(manager) }
	}
	manager.offlinePendingRemover = hooks.pendingRemover
	if err := loadRecoveryManifest(manager); err != nil {
		return "", fmt.Errorf("load current manifest: %w", err)
	}
	backupDir, err := snapshotOfflineRecoveryState(manager, pendingPath, pendingSnapshot, paths)
	if err != nil {
		return "", fmt.Errorf("private pre-recovery snapshot: %w", err)
	}
	if err := manager.recover(); err != nil {
		var removalErr *pendingJournalRemovalError
		if errors.As(err, &removalErr) {
			matches, verifyErr := pendingJournalStillMatches(pendingPath, raw)
			if matches {
				return backupDir, fmt.Errorf("recovery manifest committed; pending journal remains unchanged; private snapshot retained: %w", err)
			}
			if verifyErr == nil {
				return backupDir, fmt.Errorf("recovery manifest committed; pending journal changed after deletion failure; private snapshot retained: %w", err)
			}
			return backupDir, fmt.Errorf("recovery manifest committed; pending deletion failed and journal retention could not be verified (%v); private snapshot retained: %w", verifyErr, err)
		}
		if rb := restoreOfflineRecoverySnapshot(manager, backupDir); rb != nil {
			return backupDir, fmt.Errorf("offline recovery failed; rollback incomplete; private snapshot retained: %v", rb)
		}
		return backupDir, fmt.Errorf("offline recovery failed; original files restored and journal retained: %w", err)
	}
	pendingInfo, pendingErr := os.Lstat(pendingPath)
	if pendingErr == nil {
		matches, verifyErr := pendingJournalStillMatches(pendingPath, raw)
		if matches {
			return backupDir, errors.New("recovery manifest committed; pending journal remains unchanged; private snapshot retained")
		}
		if verifyErr == nil {
			return backupDir, fmt.Errorf("recovery manifest committed; pending journal remains but its content changed; private snapshot retained (mode %s)", pendingInfo.Mode())
		}
		return backupDir, fmt.Errorf("recovery manifest committed; pending journal remains but retention could not be verified (%v); private snapshot retained", verifyErr)
	}
	if !errors.Is(pendingErr, os.ErrNotExist) {
		return backupDir, fmt.Errorf("recovery manifest committed; pending journal state cannot be verified: %w; private snapshot retained", pendingErr)
	}
	return backupDir, nil
}

func pendingJournalStillMatches(path string, want []byte) (bool, error) {
	f, raw, _, err := readPrivatePending(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	return bytes.Equal(raw, want), nil
}

func readPrivatePending(path string) (*os.File, []byte, savedFile, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, savedFile{}, fmt.Errorf("open pending journal: %w", err)
	}
	f := os.NewFile(uintptr(fd), filepath.Base(path))
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, savedFile{}, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() <= 0 || st.Size() > maxPendingJournalBytes {
		_ = f.Close()
		return nil, nil, savedFile{}, errors.New("pending journal must be private regular file within size limit")
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || owner.Nlink != 1 {
		_ = f.Close()
		return nil, nil, savedFile{}, errors.New("pending journal owner/link count unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxPendingJournalBytes+1))
	if err != nil || int64(len(raw)) != st.Size() {
		_ = f.Close()
		return nil, nil, savedFile{}, errors.New("pending journal read incomplete")
	}
	return f, raw, savedFile{Content: raw, Mode: uint32(st.Mode().Perm()), Exists: true, UID: int(owner.Uid), GID: int(owner.Gid)}, nil
}

func loadRecoveryManifest(m *ApplyManager) error {
	for _, p := range []string{filepath.Join(m.stateDir(), "manifest.json"), m.target(appliedManifestBackupPath)} {
		v, err := saveFileBounded(p, maxPendingJournalBytes)
		if err != nil {
			return err
		}
		if !v.Exists {
			continue
		}
		var manifest applyManifest
		if err := json.Unmarshal(v.Content, &manifest); err != nil {
			return errors.New("existing manifest malformed")
		}
		m.status, m.paths, m.domains, m.manifestFiles = manifest.Status, manifest.Paths, manifest.Domains, manifest.Files
		return nil
	}
	return nil
}

func snapshotOfflineRecoveryState(m *ApplyManager, pendingPath string, pending savedFile, paths []string) (string, error) {
	root := filepath.Join(m.stateDir(), "pending-recovery-backups")
	st, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(root, 0700); err != nil {
			return "", err
		}
		st, err = os.Lstat(root)
	}
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0700 {
		return "", errors.New("recovery backup root invalid")
	}
	if owner, ok := st.Sys().(*syscall.Stat_t); !ok || owner.Uid != uint32(os.Geteuid()) {
		return "", errors.New("recovery backup root owner invalid")
	}
	dir, err := os.MkdirTemp(root, "offline-")
	if err != nil {
		return "", err
	}
	snapshot := offlineRecoverySnapshot{Version: 1, CreatedAt: time.Now().UTC(), Files: map[string]savedFile{pendingPath: pending}}
	contentBytes := int64(len(pending.Content))
	if contentBytes > maxOfflineRecoverySnapshotContentBytes {
		return dir, errors.New("recovery snapshot content exceeds cumulative size limit")
	}
	addSnapshotFile := func(snapshotPath, diskPath string) error {
		remaining := maxOfflineRecoverySnapshotContentBytes - contentBytes
		v, err := saveFileBounded(diskPath, remaining)
		if err != nil {
			return err
		}
		contentBytes += int64(len(v.Content))
		if contentBytes > maxOfflineRecoverySnapshotContentBytes {
			return errors.New("recovery snapshot content exceeds cumulative size limit")
		}
		snapshot.Files[snapshotPath] = v
		return nil
	}
	for _, p := range paths {
		if err := addSnapshotFile(p, m.target(p)); err != nil {
			return dir, err
		}
	}
	manifestPath := filepath.Join(m.stateDir(), "manifest.json")
	if err := addSnapshotFile(manifestPath, manifestPath); err != nil {
		return dir, err
	}
	appliedTarget := m.target(appliedManifestBackupPath)
	if err := addSnapshotFile(appliedManifestBackupPath, appliedTarget); err != nil {
		return dir, err
	}
	raw, err := marshalOfflineRecoverySnapshot(snapshot, maxOfflineRecoverySnapshotBytes)
	if err != nil {
		return dir, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "snapshot.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return dir, err
	}
	_, werr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(werr, syncErr, closeErr); err != nil {
		return dir, err
	}
	return dir, nil
}

func marshalOfflineRecoverySnapshot(snapshot offlineRecoverySnapshot, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, errors.New("invalid recovery snapshot size limit")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, errors.New("serialized recovery snapshot exceeds rollback reader limit")
	}
	return raw, nil
}

func saveFileBounded(path string, limit int64) (savedFile, error) {
	if limit < 0 {
		return savedFile{}, errors.New("recovery snapshot content exceeds cumulative size limit")
	}
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return savedFile{}, nil
	}
	if err != nil {
		return savedFile{}, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > limit {
		return savedFile{}, fmt.Errorf("snapshot file %s exceeds remaining content budget or is unsafe", filepath.Base(path))
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return savedFile{}, err
	}
	f := os.NewFile(uintptr(fd), filepath.Base(path))
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return savedFile{}, err
	}
	beforeStat, beforeOK := before.Sys().(*syscall.Stat_t)
	openedStat, openedOK := opened.Sys().(*syscall.Stat_t)
	if !opened.Mode().IsRegular() || !beforeOK || !openedOK || beforeStat.Dev != openedStat.Dev || beforeStat.Ino != openedStat.Ino || before.Size() != opened.Size() || opened.Size() > limit {
		return savedFile{}, errors.New("snapshot file changed during bounded open")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return savedFile{}, err
	}
	after, err := f.Stat()
	if err != nil {
		return savedFile{}, err
	}
	if int64(len(raw)) > limit || int64(len(raw)) != opened.Size() || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return savedFile{}, errors.New("snapshot file changed or exceeded bounded read")
	}
	uid, gid := fileOwner(opened)
	return savedFile{Content: raw, Mode: uint32(opened.Mode().Perm()), Exists: true, UID: uid, GID: gid}, nil
}

func restoreOfflineRecoverySnapshot(m *ApplyManager, dir string) error {
	p := filepath.Join(dir, "snapshot.json")
	fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "snapshot.json")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 0 || info.Size() > maxOfflineRecoverySnapshotBytes {
		return errors.New("rollback snapshot metadata or size invalid")
	}
	var snapshot offlineRecoverySnapshot
	if err := json.NewDecoder(io.LimitReader(f, maxOfflineRecoverySnapshotBytes)).Decode(&snapshot); err != nil {
		return errors.New("rollback snapshot malformed")
	}
	keys := make([]string, 0, len(snapshot.Files))
	for k := range snapshot.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pending := filepath.Join(m.stateDir(), "pending.json")
	sort.SliceStable(keys, func(i, j int) bool { return keys[i] != pending && keys[j] == pending })
	var errs []error
	for _, p := range keys {
		v := snapshot.Files[p]
		var re error
		if p == filepath.Join(m.stateDir(), "manifest.json") || p == pending {
			re = restorePrivateStateFile(p, v, m.stateDir(), m.targetRoot)
		} else if p == appliedManifestBackupPath {
			re = restorePrivateStateFile(m.target(p), v, m.stateDir(), m.targetRoot)
		} else {
			clean, _, e := resolveAllowedPath(p, allowedWriteRoots)
			target := m.target(p)
			if e != nil || clean != p {
				re = errors.New("rollback path unsupported")
			} else {
				roots := allowedWriteRoots
				if m.targetRoot != "" {
					roots = []string{m.targetRoot}
				}
				if v.Exists {
					_, re = writeFileAtomic(target, v.Content, os.FileMode(v.Mode), roots)
					if re == nil && os.Geteuid() == 0 {
						re = os.Chown(target, v.UID, v.GID)
					} else if re == nil && (v.UID != os.Getuid() || v.GID != os.Getgid()) {
						re = errors.New("cannot restore original owner without root")
					}
				} else if st, e := os.Lstat(target); errors.Is(e, os.ErrNotExist) {
					re = nil
				} else if e != nil {
					re = e
				} else if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
					re = errors.New("rollback target unsafe")
				} else {
					re = os.Remove(target)
				}
			}
		}
		if re != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", filepath.Base(p), re))
		}
	}
	return errors.Join(errs...)
}

func restorePrivateStateFile(path string, v savedFile, stateRoot, targetRoot string) error {
	if !v.Exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	parent := filepath.Dir(path)
	allowedApplied := filepath.Join(maintenance.DefaultMaintenanceRoot, "applied_manifest.json")
	if targetRoot != "" {
		allowedApplied = filepath.Join(targetRoot, strings.TrimPrefix(allowedApplied, string(filepath.Separator)))
	}
	if path != filepath.Join(stateRoot, "manifest.json") && path != allowedApplied && path != filepath.Join(stateRoot, "pending.json") {
		return errors.New("private state path not allowed")
	}
	st, err := os.Lstat(parent)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("private state parent unsafe")
	}
	tmp, err := os.OpenFile(filepath.Join(parent, ".offline-recovery-restore.tmp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, w := tmp.Write(v.Content)
	c := tmp.Chmod(os.FileMode(v.Mode))
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(w, c, syncErr, closeErr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(tmp.Name(), v.UID, v.GID); err != nil {
			_ = os.Remove(tmp.Name())
			return err
		}
	} else if v.UID != os.Getuid() || v.GID != os.Getgid() {
		_ = os.Remove(tmp.Name())
		return errors.New("cannot restore original private-state owner without root")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return syncDir(parent)
}
