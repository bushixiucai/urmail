package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
)

// PreparedReleaseStatus is offline evidence, never runtime readiness. The digest
// binds the exact manifest whose managed files have just been verified.
type PreparedReleaseStatus struct {
	Status         ApplyStatus
	ManifestSHA256 string
}

func loadReleaseManager(cfg *config.Config, root, targetRoot string) (*ApplyManager, string, error) {
	if cfg == nil || cfg.DevMode || cfg.Quarantined || cfg.WorkersHeld || cfg.DNSSecondaryOnly {
		return nil, "", errors.New("unsupported release configuration")
	}
	if _, err := os.Lstat(filepath.Join(root, "pending.json")); !os.IsNotExist(err) {
		return nil, "", errors.New("release refused: pending or uninspectable apply journal")
	}
	path := filepath.Join(root, "manifest.json")
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !ok || (os.Geteuid() == 0 && owner.Uid != 0) || info.Size() > 16<<20 {
		return nil, "", errors.New("unsafe release manifest")
	}
	raw := make([]byte, info.Size())
	if _, err = file.ReadAt(raw, 0); err != nil {
		return nil, "", err
	}
	var manifest applyManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return nil, "", err
	}
	if manifest.Status.Desired == "" || manifest.Status.Degraded || manifest.Status.Error != "" {
		return nil, "", errors.New("release manifest has no healthy desired generation")
	}
	m := &ApplyManager{cfg: cfg, root: root, targetRoot: targetRoot, status: manifest.Status, paths: manifest.Paths, domains: manifest.Domains, manifestFiles: manifest.Files, run: runCommand}
	if drift, reason := m.detectDiskDrift(); drift {
		return nil, "", fmt.Errorf("release configuration drift: %s", reason)
	}
	sum := sha256.Sum256(raw)
	return m, hex.EncodeToString(sum[:]), nil
}

// InspectPreparedRelease reads and verifies installed state without starting an
// agent, recovering a journal, opening a socket or claiming protocols are ready.
func InspectPreparedRelease(cfg *config.Config) (PreparedReleaseStatus, error) {
	m, digest, err := loadReleaseManager(cfg, StateRoot, "")
	if err != nil {
		return PreparedReleaseStatus{}, err
	}
	if m.status.Step != "prepared" || m.status.Ready {
		return PreparedReleaseStatus{}, errors.New("release requires offline config prepare under the current fence")
	}
	return PreparedReleaseStatus{Status: m.status, ManifestSHA256: digest}, nil
}

// VerifyReleaseRuntime is read-only: it never constructs NewApplyManager (whose
// startup recovery can mutate services). Called after controlled service start;
// failure must cause the CLI to restore guards and stop all writers.
func VerifyReleaseRuntime(ctx context.Context, cfg *config.Config, desired string) error {
	m, _, err := loadReleaseManager(cfg, StateRoot, "")
	if err != nil {
		return err
	}
	if m.status.Desired != desired {
		return errors.New("release desired generation changed")
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	m.pool = pool
	return m.checkRuntimeReadiness(ctx)
}
