package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

// PrepareOffline runs only the configuration-writing portion of Apply. The root
// CLI must hold operation.lock, verify the upgrade fence, and stop all writers.
// It is intentionally not registered as a socket capability.
func PrepareOffline(ctx context.Context, cfg *config.Config, raw json.RawMessage) (ApplyStatus, error) {
	if os.Geteuid() != 0 {
		return ApplyStatus{}, errors.New("offline configuration preparation requires root")
	}
	if cfg == nil || cfg.DevMode || cfg.Quarantined || cfg.WorkersHeld || cfg.DNSSecondaryOnly {
		return ApplyStatus{}, errors.New("unsupported offline preparation configuration")
	}
	if err := ctx.Err(); err != nil {
		return ApplyStatus{}, err
	}
	// Never use NewApplyManager here: its recovery may restart protocol services.
	m := &ApplyManager{cfg: cfg, root: StateRoot, run: runCommand, fileWriter: writeFileAtomic, prepareOnly: true}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return ApplyStatus{}, err
	}
	if err := os.Chmod(m.root, 0700); err != nil {
		return ApplyStatus{}, err
	}
	if _, err := os.Lstat(filepath.Join(m.root, "pending.json")); err == nil {
		return ApplyStatus{}, errors.New("pending apply journal requires explicit recovery before offline preparation")
	} else if !os.IsNotExist(err) {
		return ApplyStatus{}, err
	}
	for _, path := range []string{filepath.Join(m.root, "manifest.json"), appliedManifestBackupPath} {
		data, err := os.ReadFile(path) // #nosec G304 -- fixed root-owned agent manifest paths, never supplied in parameters.
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ApplyStatus{}, err
		}
		var previous applyManifest
		if err := json.Unmarshal(data, &previous); err != nil {
			return ApplyStatus{}, fmt.Errorf("read previous generation: %w", err)
		}
		m.status, m.paths, m.domains, m.manifestFiles = previous.Status, previous.Paths, previous.Domains, previous.Files
		m.status.Ready = false
		break
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return ApplyStatus{}, err
	}
	m.pool = pool
	defer m.Close()
	m.versions = &reconcile.VersionedStore{StoreQuerier: store.New(pool), DB: pool.Pool}
	_, _, err = m.Apply(raw)
	return m.status, err
}
