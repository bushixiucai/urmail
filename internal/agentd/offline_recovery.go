package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/maintenance"
)

const (
	maxPendingJournalBytes = 16 << 20
	maxJournalFileBytes    = 4 << 20
)

type offlineRecoverySnapshot struct {
	Version   int                  `json:"version"`
	CreatedAt time.Time            `json:"created_at_utc"`
	Files     map[string]savedFile `json:"files"`
}

// RecoverPendingJournalOffline restores one valid pending apply journal while
// keeping an intact v2 upgrade hold. It requires all guarded writers stopped,
// validates the hold receipt and drop-in guards, takes operation.lock, and
// preserves a private pre-recovery snapshot. It never constructs
// NewApplyManager or starts/reloads protocol services. Journals requiring DNS
// signing or spam-Sieve compilation fail closed.
func RecoverPendingJournalOffline(ctx context.Context, maintenanceRoot string) (string, error) {
	if os.Geteuid() != 0 {
		return "", errors.New("offline pending recovery requires root")
	}
	if os.Getenv(maintenance.InstallerLeaseEnv) != "" {
		return "", errors.New("offline pending recovery refuses inherited installer lease")
	}
	stateRoot := StateRoot
	stateInfo, stateErr := os.Lstat(stateRoot)
	if stateErr != nil || !stateInfo.IsDir() || stateInfo.Mode()&os.ModeSymlink != 0 || stateInfo.Mode().Perm() != 0700 {
		return "", errors.New("agent state root must be a private real directory")
	}
	if owner, ok := stateInfo.Sys().(*syscall.Stat_t); !ok || owner.Uid != 0 {
		return "", errors.New("agent state root must be root-owned")
	}
	root := maintenance.Root(maintenanceRoot)
	holdHash, err := verifyOfflineFence(root, maintenance.DefaultDropInDir)
	if err != nil {
		return "", err
	}
	if err := verifyOfflineWritersForRecovery(ctx, root, holdHash, StateRoot, false); err != nil {
		return "", err
	}
	lock, err := acquireOfflineRecoveryLock(ctx, root)
	if err != nil {
		return "", fmt.Errorf("acquire operation lock: %w", err)
	}
	defer func() { _ = maintenance.ReleaseLock(lock) }()
	holdHashAfterLock, err := verifyOfflineFence(root, maintenance.DefaultDropInDir)
	if err != nil {
		return "", err
	}
	if holdHashAfterLock != holdHash {
		return "", errors.New("upgrade hold changed while acquiring operation lock")
	}
	if err := verifyOfflineWritersForRecovery(ctx, root, holdHash, StateRoot, true); err != nil {
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(root, maintenance.BackupReceiptName)); err == nil {
		return "", errors.New("offline pending recovery refuses active backup-maintenance receipt")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect backup-maintenance receipt: %w", err)
	}
	return recoverPendingJournalOfflineCore(ctx, stateRoot, "", writeFileAtomic)
}

func acquireOfflineRecoveryLock(ctx context.Context, root string) (*maintenance.LockHandle, error) {
	return maintenance.AcquireLockWithTimeout(ctx, root, maintenance.DefaultLockName, 0)
}

func verifyOfflineFence(root, dropInDir string) (string, error) {
	status, err := maintenance.CheckFenceStatus(root)
	if err != nil || status == nil || !status.Active || status.Meta == nil || status.Receipt == nil {
		return "", errors.New("offline pending recovery requires a readable active upgrade fence and receipt")
	}
	holdRaw, receiptRaw, err := maintenance.ReadUpgradeFenceRaw(root)
	if err != nil {
		return "", errors.New("offline pending recovery cannot read fence evidence")
	}
	var meta maintenance.UpgradeFenceMeta
	var receipt maintenance.UpgradeReceipt
	if json.Unmarshal(holdRaw, &meta) != nil || json.Unmarshal(receiptRaw, &receipt) != nil {
		return "", errors.New("offline pending recovery fence metadata malformed")
	}
	sum := sha256.Sum256(holdRaw)
	holdHash := hex.EncodeToString(sum[:])
	if meta.Version != 2 || receipt.Version != 2 || !receipt.UpgradeHeld || receipt.HoldSHA256 != holdHash || receipt.Generation != meta.Generation {
		return "", errors.New("offline pending recovery requires valid v2 receipt bound to hold")
	}
	hi, err := os.Lstat(status.HoldPath)
	if err != nil || !hi.Mode().IsRegular() || hi.Mode().Perm() != 0600 || hi.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("offline pending recovery hold file metadata unsafe")
	}
	holdStat, ok := hi.Sys().(*syscall.Stat_t)
	if !ok || holdStat.Uid != uint32(os.Geteuid()) || holdStat.Nlink != 1 {
		return "", errors.New("offline pending recovery hold owner/link count unsafe")
	}
	guards, err := maintenance.CheckDropInGuards(dropInDir, maintenance.DropInGuardedUnits)
	if err != nil {
		return "", fmt.Errorf("inspect upgrade drop-in guards: %w", err)
	}
	var templateFingerprint string
	for _, unit := range maintenance.DropInGuardedUnits {
		if !guards[unit] {
			return "", fmt.Errorf("offline pending recovery requires guard for %s", unit)
		}
		var checkErr error
		if unit == "postfix@.service" {
			templateFingerprint, checkErr = verifyPostfixTemplateFenceDropIn(unit, status.HoldPath)
		} else {
			checkErr = verifyEffectiveFenceDropIn(unit, status.HoldPath)
		}
		if checkErr != nil {
			return "", checkErr
		}
	}
	return fmt.Sprintf("%s:%d:%d:%s", holdHash, holdStat.Ino, hi.ModTime().UnixNano(), templateFingerprint), nil
}

func verifyEffectiveFenceDropIn(unit, holdPath string) error {
	path := filepath.Join(maintenance.DefaultDropInDir, unit+".d", maintenance.DropInFileName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("fence drop-in for %s missing/unsafe", unit)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
		return fmt.Errorf("fence drop-in for %s not root-owned", unit)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read fence drop-in for %s: %w", unit, err)
	}
	if !bytes.Contains(content, []byte("ConditionPathExists=!"+holdPath)) {
		return fmt.Errorf("fence condition absent in drop-in for %s", unit)
	}
	if strings.HasSuffix(unit, ".service") && !bytes.Contains(content, []byte("ExecStartPre=/usr/bin/test ! -f "+holdPath)) {
		return fmt.Errorf("fence ExecStartPre absent in drop-in for %s", unit)
	}
	cmd := exec.Command("/usr/bin/systemctl", "show", "--property=LoadState", "--property=DropInPaths", unit)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/root"}
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("inspect effective fence for %s: %w", unit, err)
	}
	text := string(out)
	if strings.Contains(text, "LoadState=not-found") {
		return nil
	}
	if !strings.Contains(text, "LoadState=loaded") || !strings.Contains(text, path) {
		return fmt.Errorf("fence drop-in not loaded by systemd for %s", unit)
	}
	return nil
}
