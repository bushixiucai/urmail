package reversemigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type uidlistMeta struct {
	UIDValidity string
	NextUID     int64
}

// MergeMaildirAndIMAP executes §4.1.1 (Maildir incremental sync with quarantine)
// and §4.1.3 (dovecot-uidlist UIDVALIDITY split check, keywords, subscriptions,
// and user sieve/ sync with .svbin cleanup).
func MergeMaildirAndIMAP(srcDir, tgtDir, quarantineDir string, allowSharedVerified, dryRun bool) (MaildirReport, IMAPReport, error) {
	var mRep MaildirReport
	var iRep IMAPReport

	if srcDir == "" || tgtDir == "" {
		return mRep, iRep, errors.New("src and tgt maildir paths are required")
	}
	srcClean := filepath.Clean(srcDir)
	tgtClean := filepath.Clean(tgtDir)
	if srcClean == tgtClean {
		if !allowSharedVerified {
			return mRep, iRep, errors.New("shared Maildir volume requires pre-cutover & pre-merge readonly backup verification (--allow-shared-maildir-verified)")
		}
		mRep.SharedVolumeMode = true
		mRep.Actions = append(mRep.Actions, "shared Maildir volume verified; skipping physical self-copy")
		return mRep, iRep, nil
	}

	srcHashes, err := scanMaildirHashes(srcClean)
	if err != nil {
		return mRep, iRep, fmt.Errorf("scan source maildir: %w", err)
	}
	tgtHashes, err := scanMaildirHashes(tgtClean)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return mRep, iRep, fmt.Errorf("scan target maildir: %w", err)
	}

	// Check IMAP UIDVALIDITY split before mutating anything.
	uidChecks, err := checkAllUIDLists(srcClean, tgtClean)
	if err != nil {
		return mRep, iRep, err
	}
	iRep.MailboxesChecked = len(uidChecks)
	iRep.UIDValidityMatch = true

	for rel, srcSum := range srcHashes {
		if tgtSum, exists := tgtHashes[rel]; exists {
			if tgtSum != srcSum {
				return mRep, iRep, fmt.Errorf("maildir conflict at %s: source sha256=%s != target sha256=%s", rel, srcSum, tgtSum)
			}
			mRep.FilesVerified++
			continue
		}
		mRep.Actions = append(mRep.Actions, "copy "+rel)
		if !dryRun {
			if err := copyFileAtomic(filepath.Join(srcClean, rel), filepath.Join(tgtClean, rel), srcSum); err != nil {
				return mRep, iRep, fmt.Errorf("copy %s: %w", rel, err)
			}
		}
		mRep.FilesCopied++
	}

	// Deleted maildir files on source -> move target copy to quarantine (never raw rm).
	for rel := range tgtHashes {
		if _, exists := srcHashes[rel]; exists {
			continue
		}
		mRep.Actions = append(mRep.Actions, "quarantine "+rel)
		if !dryRun {
			if quarantineDir == "" {
				return mRep, iRep, fmt.Errorf("quarantine directory required to archive deleted maildir file %s", rel)
			}
			dstQ := filepath.Join(quarantineDir, rel)
			if err := os.MkdirAll(filepath.Dir(dstQ), 0o750); err != nil {
				return mRep, iRep, err
			}
			if err := os.Rename(filepath.Join(tgtClean, rel), dstQ); err != nil {
				return mRep, iRep, fmt.Errorf("quarantine %s: %w", rel, err)
			}
		}
		mRep.FilesQuarantined++
	}

	// Sync dovecot-uidlist, dovecot-keywords, subscriptions, and sieve/.
	sieveCount, svbinPurged, err := syncIMAPAndSieveFiles(srcClean, tgtClean, dryRun)
	if err != nil {
		return mRep, iRep, err
	}
	iRep.SieveSynced = sieveCount
	iRep.SvbinPurged = svbinPurged
	return mRep, iRep, nil
}

func scanMaildirHashes(root string) (map[string]string, error) {
	out := map[string]string{}
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		base := filepath.Base(rel)
		parent := filepath.Base(filepath.Dir(rel))
		if parent != "cur" && parent != "new" {
			return nil
		}
		if strings.HasPrefix(base, ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			return fmt.Errorf("corrupted 0-byte maildir message at %s", rel)
		}
		sum, err := sha256File(path)
		if err != nil {
			return err
		}
		out[rel] = sum
		return nil
	})
	return out, err
}

func checkAllUIDLists(srcRoot, tgtRoot string) (map[string]uidlistMeta, error) {
	res := map[string]uidlistMeta{}
	err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "dovecot-uidlist" {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		srcMeta, err := parseUIDList(path)
		if err != nil {
			return fmt.Errorf("parse source %s: %w", rel, err)
		}
		res[rel] = srcMeta
		tgtPath := filepath.Join(tgtRoot, rel)
		if _, statErr := os.Stat(tgtPath); statErr == nil {
			tgtMeta, err := parseUIDList(tgtPath)
			if err != nil {
				return fmt.Errorf("parse target %s: %w", rel, err)
			}
			if tgtMeta.UIDValidity != "" && srcMeta.UIDValidity != "" && tgtMeta.UIDValidity != srcMeta.UIDValidity && tgtMeta.NextUID > 1 {
				return fmt.Errorf("UIDVALIDITY split at %s: source=%s target=%s (target next_uid=%d)", rel, srcMeta.UIDValidity, tgtMeta.UIDValidity, tgtMeta.NextUID)
			}
		}
		return nil
	})
	return res, err
}

func parseUIDList(path string) (uidlistMeta, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- walked inside configured maildir root
	if err != nil {
		return uidlistMeta{}, err
	}
	var meta uidlistMeta
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Dovecot uidlist v3 header: "3 V<uidvalidity> N<next_uid> ..."
		if strings.HasPrefix(line, "3 ") {
			for _, tok := range strings.Fields(line) {
				if strings.HasPrefix(tok, "V") {
					meta.UIDValidity = strings.TrimPrefix(tok, "V")
				} else if strings.HasPrefix(tok, "N") {
					n, err := strconv.ParseInt(strings.TrimPrefix(tok, "N"), 10, 64)
					if err == nil {
						meta.NextUID = n
					}
				}
			}
			return meta, nil
		}
	}
	return meta, fmt.Errorf("missing v3 header in %s", path)
}

func syncIMAPAndSieveFiles(srcRoot, tgtRoot string, dryRun bool) (int, int, error) {
	sieveSynced := 0
	svbinPurged := 0
	err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		name := d.Name()
		isIMAPMeta := name == "dovecot-uidlist" || name == "dovecot-keywords" || name == "subscriptions"
		isSieve := strings.Contains(filepath.ToSlash(rel), "/sieve/") || strings.HasSuffix(name, ".sieve")
		if strings.HasSuffix(name, ".svbin") {
			return nil
		}
		if !isIMAPMeta && !isSieve {
			return nil
		}
		sum, err := sha256File(path)
		if err != nil {
			return err
		}
		if !dryRun {
			if err := copyFileAtomic(path, filepath.Join(tgtRoot, rel), sum); err != nil {
				return err
			}
		}
		if isSieve {
			sieveSynced++
		}
		return nil
	})
	if err != nil {
		return sieveSynced, svbinPurged, err
	}

	// Purge stale .svbin compiled scripts on target so Dovecot recompiles cleanly.
	if _, statErr := os.Stat(tgtRoot); statErr == nil {
		var staleSvbin []string
		_ = filepath.WalkDir(tgtRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.HasSuffix(d.Name(), ".svbin") {
				staleSvbin = append(staleSvbin, path)
			}
			return nil
		})
		for _, p := range staleSvbin {
			if !dryRun {
				_ = os.Remove(p)
			}
			svbinPurged++
		}
	}
	return sieveSynced, svbinPurged, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- validated internal maildir path
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

func copyFileAtomic(src, dst, wantSHA256 string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	in, err := os.Open(src) // #nosec G304 -- validated maildir copy source
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".e7-copy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	gotSHA256 := hex.EncodeToString(h.Sum(nil))
	if wantSHA256 != "" && gotSHA256 != wantSHA256 {
		_ = os.Remove(tmpName)
		return fmt.Errorf("sha256 mismatch after copy: got %s, want %s", gotSHA256, wantSHA256)
	}
	return os.Rename(tmpName, dst)
}
