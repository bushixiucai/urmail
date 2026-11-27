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
)

// PrepareOffline runs only the configuration-writing portion of Apply. The root
// CLI must hold operation.lock, verify the upgrade fence, and stop all writers.
// It is intentionally not registered as a socket capability.
func PrepareOffline(ctx context.Context, cfg *config.Config, raw json.RawMessage) (ApplyStatus, error) {
	// The fenced upgrade preparation stays root-only; only the first-boot entry
	// may render as the unprivileged container identity into its own volume.
	if os.Geteuid() != 0 {
		return ApplyStatus{}, errors.New("offline configuration preparation requires root")
	}
	if err := offlinePreparationGuard(ctx, cfg); err != nil {
		return ApplyStatus{}, err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return ApplyStatus{}, err
	}
	return prepareOffline(ctx, cfg, pool, raw, true, false)
}

// PrepareOfflineWithPool runs the fenced upgrade preparation against an
// already-opened platform pool so both PostgreSQL and SQLite engines share
// the exact same configuration rendering contract.
func PrepareOfflineWithPool(ctx context.Context, cfg *config.Config, pool *db.Pool, raw json.RawMessage) (ApplyStatus, error) {
	if os.Geteuid() != 0 {
		return ApplyStatus{}, errors.New("offline configuration preparation requires root")
	}
	if pool == nil {
		return ApplyStatus{}, errors.New("offline configuration preparation requires a platform pool")
	}
	if err := offlinePreparationGuard(ctx, cfg); err != nil {
		return ApplyStatus{}, err
	}
	return prepareOffline(ctx, cfg, pool, raw, false, false)
}

// PrepareOfflineFirstBoot renders a fresh installation from an empty root.
//
// It has the same offline contract as PrepareOffline, with two differences
// that exist only for the first boot of an empty database:
//   - the caller supplies the platform pool, so the single-engine SQLite store
//     works exactly like PostgreSQL;
//   - a recorded render generation is a hard refusal, so a repeat invocation
//     can never overwrite live configuration.
//
// It does not relax the upgrade path: PrepareOffline still requires the active
// fence plus a fresh backup preflight, and neither rule is evaluated here.
func PrepareOfflineFirstBoot(ctx context.Context, cfg *config.Config, pool *db.Pool, raw json.RawMessage) (ApplyStatus, error) {
	if pool == nil {
		return ApplyStatus{}, errors.New("first-boot configuration preparation requires a platform pool")
	}
	if err := offlinePreparationGuard(ctx, cfg); err != nil {
		return ApplyStatus{}, err
	}
	return prepareOffline(ctx, cfg, pool, raw, false, true)
}

// offlineStateRoot, offlineTargetRoot and offlineCommand are the live
// defaults. Tests point them at an isolated tree and a stubbed command runner
// so a first-boot proof never touches the host.
var (
	offlineStateRoot  = StateRoot
	offlineTargetRoot string
	offlineCommand    = runCommand
)

// offlinePreparationGuard keeps every refusal shared by both entries.
func offlinePreparationGuard(ctx context.Context, cfg *config.Config) error {
	if cfg == nil || cfg.DevMode || cfg.Quarantined || cfg.WorkersHeld || cfg.DNSSecondaryOnly {
		return errors.New("unsupported offline preparation configuration")
	}
	return ctx.Err()
}

func prepareOffline(_ context.Context, cfg *config.Config, pool *db.Pool, raw json.RawMessage, ownsPool, firstBoot bool) (ApplyStatus, error) {
	stateRoot := offlineStateRoot
	if stateRoot == StateRoot {
		var err error
		stateRoot, err = resolveStateRoot(cfg)
		if err != nil {
			return ApplyStatus{}, err
		}
	}
	// Never use NewApplyManager here: its recovery may restart protocol services.
	m := &ApplyManager{cfg: cfg, root: stateRoot, targetRoot: offlineTargetRoot, run: offlineCommand, fileWriter: writeFileAtomic, prepareOnly: true}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return ApplyStatus{}, fmt.Errorf("agent state root %s: %w", m.root, err)
	}
	if err := os.Chmod(m.root, 0700); err != nil {
		return ApplyStatus{}, fmt.Errorf("agent state root %s: %w", m.root, err)
	}
	if _, err := os.Lstat(filepath.Join(m.root, "pending.json")); err == nil {
		return ApplyStatus{}, errors.New("pending apply journal requires explicit recovery before offline preparation")
	} else if !os.IsNotExist(err) {
		return ApplyStatus{}, err
	}
	if firstBoot {
		// A first boot can only render onto a clean root. The manifest records
		// every generated path, so its presence means this is an upgrade.
		for _, path := range []string{filepath.Join(m.root, "manifest.json"), m.target(m.appliedManifestBackupPath())} {
			if _, err := os.Lstat(path); err == nil {
				return ApplyStatus{}, fmt.Errorf("%w: an existing configuration generation blocks first-boot preparation", ErrRejected)
			} else if !os.IsNotExist(err) {
				return ApplyStatus{}, err
			}
		}
	} else {
		for _, path := range []string{filepath.Join(m.root, "manifest.json"), m.target(m.appliedManifestBackupPath())} {
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
	}
	m.pool = pool
	if ownsPool {
		defer m.Close()
	}
	// The SQLite pool has no pgxpool; the pool itself is the DBTX.
	m.versions = &reconcile.VersionedStore{StoreQuerier: db.Queries(pool), DB: pool}
	_, _, err := m.Apply(raw)
	return m.status, err
}
