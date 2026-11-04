package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/maintenance"
)

type migrationVerifierFunc func(ctx context.Context, migrateURL string) error

func defaultMigrationVerifier(_ context.Context, migrateURL string) error {
	migrator, mErr := db.NewMigrator(migrateURL)
	if mErr != nil {
		return fmt.Errorf("database connection failed: %w", mErr)
	}
	defer func() { _ = migrator.Close() }()

	v, dirty, vErr := migrator.Version()
	if vErr != nil {
		return fmt.Errorf("failed to verify migration version: %w", vErr)
	}
	if dirty {
		return fmt.Errorf("database schema is dirty at version %d (fence retained)", v)
	}
	return nil
}

var activeMigrationVerifier migrationVerifierFunc = defaultMigrationVerifier

var activeReleaseRuntimeVerifier = agentd.VerifyReleaseRuntime

const upgradeUsage = "urmailctl upgrade — manage upgrade lock, write fence and drop-in guards\n" +
	"\n" +
	"Usage:\n" +
	"  urmailctl upgrade fence [--reason TEXT] [--timeout DURATION]\n" +
	"  urmailctl upgrade hold  [--reason TEXT] [--timeout DURATION]\n" +
	"  urmailctl upgrade readiness-record [--generation N] [--key PATH]\n" +
	"  urmailctl upgrade release [--timeout DURATION] [--skip-restart] [--break-glass --ack-risk]\n" +
	"  urmailctl upgrade run-installer --installer /trusted/absolute/install.sh -- [args...]\n" +
	"  urmailctl upgrade check-installer-lease\n" +
	"  urmailctl upgrade status [--json]\n" +
	"  urmailctl upgrade resume [--timeout DURATION]\n" +
	"  urmailctl upgrade rollback [--checkpoint DIR]\n"

func runUpgrade(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(upgradeUsage)
		return nil
	}
	switch args[0] {
	case "run-installer":
		return runUpgradeInstaller(ctx, args[1:])
	case "check-installer-lease":
		if len(args) != 1 {
			return errors.New("usage: upgrade check-installer-lease")
		}
		return maintenance.CheckInstallerLease(currentMaintenanceRoot())
	case "fence", "hold":
		return runUpgradeFence(ctx, args[1:])
	case "readiness-record":
		return runUpgradeReadinessRecord(ctx, args[1:])
	case "release":
		return runUpgradeRelease(ctx, args[1:])
	case "status":
		return runUpgradeStatus(ctx, args[1:])
	case "resume":
		return runUpgradeResume(ctx, args[1:])
	case "rollback":
		return runUpgradeRollback(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Print(upgradeUsage)
		return nil
	default:
		fmt.Print(upgradeUsage)
		return fmt.Errorf("unknown upgrade subcommand %q", args[0])
	}
}

var upgradeEffectiveUID = os.Geteuid

func checkUpgradePrivileges() error {
	if upgradeEffectiveUID() != 0 {
		return errors.New("upgrade commands must run as root")
	}
	return nil
}

func runUpgradeFence(ctx context.Context, args []string) error {
	if err := checkUpgradePrivileges(); err != nil {
		return err
	}

	flags := flag.NewFlagSet("upgrade-fence", flag.ContinueOnError)
	reason := flags.String("reason", "system upgrade", "reason for establishing upgrade fence")
	timeout := flags.Duration("timeout", 30*time.Second, "timeout waiting for operation lock")
	rootDir := flags.String("root-dir", currentMaintenanceRoot(), "maintenance root directory")
	dropInDir := flags.String("drop-in-dir", maintenance.DefaultDropInDir, "systemd drop-in directory")
	if err := flags.Parse(args); err != nil {
		return err
	}

	// 1. Acquire operation.lock with timeout
	lock, err := maintenance.AcquireLockWithTimeout(ctx, *rootDir, maintenance.DefaultLockName, *timeout)
	if err != nil {
		return fmt.Errorf("acquire operation lock: %w", err)
	}
	defer func() { _ = maintenance.ReleaseLock(lock) }()

	// 2. Discover currently active writer units. Do NOT ignore inspection errors.
	activeUnits := []string{}
	for _, unit := range maintenance.DefaultWriterUnits {
		active, stateErr := activeBackupUnitAction(ctx, "state", unit)
		if stateErr != nil {
			return fmt.Errorf("inspect writer unit %s: %w", unit, stateErr)
		}
		if active {
			activeUnits = append(activeUnits, unit)
		}
	}

	// 3. Record the target schema generation (v2 fences bind it in the receipt).
	targetGeneration, genErr := latestEmbeddedMigrationVersion()
	if genErr != nil {
		return fmt.Errorf("determine target migration generation: %w", genErr)
	}

	// 4. Pre-install drop-in guards BEFORE writing hold to prevent any race condition
	holdFile := filepath.Join(*rootDir, maintenance.DefaultUpgradeHoldFile)
	installed, err := maintenance.InstallDropInGuards(*dropInDir, holdFile, maintenance.DropInGuardedUnits)
	if err != nil {
		return fmt.Errorf("install drop-in guards: %w", err)
	}

	// 5. Reload systemd daemon and check error
	if err = maintenance.SystemctlReload(ctx); err != nil {
		return fmt.Errorf("daemon-reload after installing guards: %w", err)
	}

	// 6. Create persistent upgrade fence marker and independent upgrade receipt
	meta := maintenance.UpgradeFenceMeta{
		Version:    2,
		CreatedAt:  time.Now().UTC(),
		Reason:     *reason,
		Operator:   fmt.Sprintf("uid-%d", os.Geteuid()),
		Generation: targetGeneration,
		Units:      activeUnits,
		DropIns:    maintenance.DropInGuardedUnits,
	}

	if err = maintenance.CreateUpgradeFence(*rootDir, meta); err != nil {
		if errors.Is(err, maintenance.ErrFenceAlreadyExists) {
			fmt.Println("Upgrade fence already exists; updating guards and stopping writers.")
		} else {
			return fmt.Errorf("create upgrade fence: %w", err)
		}
	}

	// 7. The discovery snapshot is ONLY the restart receipt, not the stop set.
	// A writer may have started between its discovery read and hold creation.
	// Reinspect every concrete unit after the guards/hold are effective.
	var stopErrs []error
	for _, unit := range maintenance.DefaultWriterUnits {
		active, stateErr := activeBackupUnitAction(ctx, "state", unit)
		if stateErr != nil {
			stopErrs = append(stopErrs, fmt.Errorf("inspect fenced writer %s: %w", unit, stateErr))
			continue
		}
		if !active {
			continue
		}
		if _, err = activeBackupUnitAction(ctx, "stop", unit); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("stop writer %s: %w", unit, err))
			continue
		}
		active, stateErr = activeBackupUnitAction(ctx, "state", unit)
		if stateErr != nil {
			stopErrs = append(stopErrs, fmt.Errorf("verify writer %s stopped: %w", unit, stateErr))
		} else if active {
			stopErrs = append(stopErrs, fmt.Errorf("writer %s still active after stop command", unit))
		}
	}
	// Independent final sweep catches activation induced while stopping another
	// unit; never report success based only on per-unit observations.
	for _, unit := range maintenance.DefaultWriterUnits {
		active, stateErr := activeBackupUnitAction(ctx, "state", unit)
		if stateErr != nil {
			stopErrs = append(stopErrs, fmt.Errorf("final writer inspection %s: %w", unit, stateErr))
		} else if active {
			stopErrs = append(stopErrs, fmt.Errorf("writer %s active in final fence sweep", unit))
		}
	}
	if len(stopErrs) > 0 {
		return fmt.Errorf("failed to stop writer units (fence retained): %w", errors.Join(stopErrs...))
	}

	fmt.Println("Upgrade fence established successfully.")
	fmt.Printf("Hold marker:    %s\n", holdFile)
	fmt.Printf("Guarded units:  %s\n", strings.Join(installed, ", "))
	fmt.Printf("Target schema generation: %d\n", targetGeneration)
	if len(activeUnits) > 0 {
		fmt.Printf("Originally active units: %s\n", strings.Join(activeUnits, ", "))
	} else {
		fmt.Println("Originally active units: none; full writer set verified stopped")
	}
	return nil
}

func runUpgradeRelease(ctx context.Context, args []string) error {
	if err := checkUpgradePrivileges(); err != nil {
		return err
	}

	flags := flag.NewFlagSet("upgrade-release", flag.ContinueOnError)
	timeout := flags.Duration("timeout", 30*time.Second, "timeout waiting for operation lock")
	skipRestart := flags.Bool("skip-restart", false, "skip restarting originally active units")
	breakGlass := flags.Bool("break-glass", false, "explicit break-glass acknowledgment to release without the signed readiness receipt")
	ackRisk := flags.Bool("ack-risk", false, "acknowledge risk of manual release without full protocol readiness receipt")
	rootDir := flags.String("root-dir", currentMaintenanceRoot(), "maintenance root directory")
	dropInDir := flags.String("drop-in-dir", maintenance.DefaultDropInDir, "systemd drop-in directory")
	if err := flags.Parse(args); err != nil {
		return err
	}

	return releaseUpgradeInternal(ctx, *rootDir, *dropInDir, *timeout, *skipRestart, *breakGlass, *ackRisk)
}

func releaseUpgradeInternal(ctx context.Context, rootDir, dropInDir string, timeout time.Duration, skipRestart, breakGlass, ackRisk bool) error {
	// 1. Acquire operation.lock with timeout
	lock, err := maintenance.AcquireLockWithTimeout(ctx, rootDir, maintenance.DefaultLockName, timeout)
	if err != nil {
		return fmt.Errorf("acquire operation lock: %w", err)
	}
	defer func() { _ = maintenance.ReleaseLock(lock) }()

	// 2. Verify fence exists (idempotent if already released). Errors are NOT swallowed.
	held, holdErr := maintenance.UpgradeHoldState(rootDir)
	if holdErr != nil {
		return fmt.Errorf("cannot release upgrade fence: %w", holdErr)
	}
	if !held {
		if err = maintenance.RemoveDropInGuards(dropInDir, maintenance.DropInGuardedUnits); err != nil {
			return fmt.Errorf("no active upgrade fence, but drop-in guard cleanup failed: %w", err)
		}
		if err = maintenance.SystemctlReload(ctx); err != nil {
			return fmt.Errorf("no active upgrade fence, but daemon-reload after guard cleanup failed: %w", err)
		}
		fmt.Println("No active upgrade fence found; drop-in guards verified cleared.")
		return nil
	}

	// 3. Strict safety prerequisites verification — NO force bypass, NEVER bypass quarantine
	if config.RestoreQuarantined() {
		return errors.New("cannot release upgrade fence: system is under restore quarantine")
	}

	// 4. Read fence status and receipt before removing
	status, err := maintenance.CheckFenceStatus(rootDir)
	if err != nil {
		return fmt.Errorf("check fence status: %w", err)
	}
	fenceV2 := status.Meta != nil && status.Meta.Version >= 2
	glass := breakGlass && ackRisk

	// 5. Default release consumes the signed readiness receipt bound to the
	// live fence bytes, the live schema generation and the freshness window.
	var readiness *upgradeReadinessDoc
	if !glass {
		if skipRestart {
			return errors.New("default release requires service restart and protocol readiness; --skip-restart requires explicit break-glass")
		}
		if !fenceV2 {
			return errors.New("cannot release upgrade fence: legacy v1 fence cannot produce a signed readiness receipt; re-fence sequence: 'urmailctl upgrade fence' → apply migrations ('upgrade resume') → 'urmailctl upgrade readiness-record' → 'upgrade release'; or release explicitly with --break-glass --ack-risk (upgrade fence retained)")
		}
		if readiness, err = verifyReadinessForRelease(ctx, rootDir, releaseSigningKeyPath); err != nil {
			return err
		}
	}

	cfg, loadErr := config.Load()
	if loadErr != nil {
		return fmt.Errorf("cannot release upgrade fence: configuration error: %w", loadErr)
	}
	if cfg == nil {
		return errors.New("cannot release upgrade fence: configuration is nil")
	}
	migrateURL := cfg.DatabaseURLMigrate
	if migrateURL == "" {
		migrateURL = cfg.DatabaseURLPlatform
	}
	if migrateURL == "" {
		return errors.New("cannot release upgrade fence: database migration URL is required for safety verification (fence retained)")
	}
	if err = activeMigrationVerifier(ctx, migrateURL); err != nil {
		return fmt.Errorf("cannot release upgrade fence: %w (fence retained)", err)
	}

	restoreFenceOnFailure := func(cause error, attemptedUnits []string) error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		if status.Meta == nil && status.Receipt == nil {
			// Fail closed with an explicit refusal instead of rebuilding an
			// empty fence nobody can account for.
			return fmt.Errorf("%w (CRITICAL: cannot restore protective fence: no fence metadata or receipt available; restore manually from checkpoint)", cause)
		}

		var restoreErrs []error
		restoreErrs = append(restoreErrs, cause)

		meta := maintenance.UpgradeFenceMeta{
			Version:   2,
			CreatedAt: time.Now().UTC(),
			Reason:    "release failed, restored protective fence",
			Operator:  "urmailctl-release-failclosed",
		}
		if status.Meta != nil {
			meta = *status.Meta
		}
		if status.Receipt != nil && len(meta.Units) == 0 {
			meta.Units = status.Receipt.Units
		}
		if meta.Version < 1 {
			meta.Version = 2
		}

		// 1. Recreate the upgrade fence hold marker and receipt (skip when the
		// hold was never removed, e.g. guards failed before fence removal).
		switch {
		case maintenance.IsUpgradeHeld(rootDir):
			// Fence still held; nothing to recreate.
		default:
			if fenceErr := maintenance.CreateUpgradeFence(rootDir, meta); fenceErr != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("FATAL: failed to restore upgrade fence: %w", fenceErr))
			}
		}

		// 2. Re-install drop-in write guards
		if _, guardErr := maintenance.InstallDropInGuards(dropInDir, filepath.Join(rootDir, maintenance.DefaultUpgradeHoldFile), maintenance.DropInGuardedUnits); guardErr != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("FATAL: failed to reinstall drop-in guards: %w", guardErr))
		}

		// 3. Reload systemd daemon to enforce guards
		if reloadErr := maintenance.SystemctlReload(cleanupCtx); reloadErr != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("failed to reload daemon after restoring guards: %w", reloadErr))
		}

		// 4. Stop all attempted units to ensure no partial writers remain running and confirm stopped
		for _, u := range attemptedUnits {
			active, inspectErr := activeBackupUnitAction(cleanupCtx, "state", u)
			if inspectErr != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("inspect cleanup writer %s: %w", u, inspectErr))
				continue
			}
			mustStop := active
			if status.Receipt != nil {
				for _, original := range status.Receipt.Units {
					if original == u {
						mustStop = true
						break
					}
				}
			}
			if !mustStop {
				continue
			} // Skip absent optional units, never skip receipt cleanup.
			if _, stopErr := activeBackupUnitAction(cleanupCtx, "stop", u); stopErr != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("failed to stop unit %s during cleanup: %w", u, stopErr))
				continue
			}
			active, stateErr := activeBackupUnitAction(cleanupCtx, "state", u)
			if stateErr != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("failed to inspect state of unit %s during cleanup: %w", u, stateErr))
			} else if active {
				restoreErrs = append(restoreErrs, fmt.Errorf("unit %s still active after stop during cleanup", u))
			}
		}

		// Only claim RESTORED if fence, guards, daemon reload and all unit stops completely succeeded with 0 additional errors
		allJoined := errors.Join(restoreErrs...)
		if len(restoreErrs) == 1 && maintenance.IsUpgradeHeld(rootDir) {
			return fmt.Errorf("%w (protective fence and hold verified RESTORED)", allJoined)
		}
		return fmt.Errorf("%w (CRITICAL: protective fence RESTORATION INCOMPLETE)", allJoined)
	}

	// 6. Reload systemd daemon before tearing guards down
	if err = maintenance.SystemctlReload(ctx); err != nil {
		return restoreFenceOnFailure(fmt.Errorf("daemon-reload before release: %w", err), nil)
	}

	attemptedUnits := []string{}

	// 6b. Fail closed BEFORE touching anything if a writer unit outside the
	// receipt is active (guards and fence are still fully in place). This
	// applies to the default receipt path too: a non-receipt unit running
	// after release would be unguarded and outside the recorded scope.
	{
		receiptUnits := map[string]bool{}
		if status.Receipt != nil {
			for _, unit := range status.Receipt.Units {
				receiptUnits[unit] = true
			}
		}
		var nonReceipt []string
		for _, unit := range maintenance.DefaultWriterUnits {
			active, stateErr := activeBackupUnitAction(ctx, "state", unit)
			if stateErr != nil {
				return restoreFenceOnFailure(fmt.Errorf("inspect writer unit %s: %w", unit, stateErr), nil)
			}
			if active && !receiptUnits[unit] {
				nonReceipt = append(nonReceipt, unit)
			}
		}
		if len(nonReceipt) > 0 {
			sort.Strings(nonReceipt)
			if glass {
				fmt.Printf("BREAK-GLASS: refusing to restore non-receipt units: %s\n", strings.Join(nonReceipt, ", "))
			} else {
				fmt.Printf("RELEASE: refusing to restore non-receipt units outside the signed readiness receipt: %s\n", strings.Join(nonReceipt, ", "))
			}
			return restoreFenceOnFailure(fmt.Errorf("release refused: non-receipt units active outside upgrade receipt: %s", strings.Join(nonReceipt, ", ")), nil)
		}
	}

	if !glass {
		prepared, inspectErr := activeAgentConfigStatus(ctx)
		if inspectErr != nil {
			return fmt.Errorf("recheck offline release preparation: %w", inspectErr)
		}
		if !validPreparedEvidence(prepared) || prepared.Desired != readiness.AgentStatus.Desired || prepared.ManifestSHA256 != readiness.AgentStatus.ManifestSHA256 {
			return errors.New("offline preparation changed since signing; fence retained")
		}
	}

	// 7. Remove drop-in guards FIRST: guards are verified cleared while the
	// hold marker still exists, so any failure here restores a fully intact fence.
	if err = maintenance.RemoveDropInGuards(dropInDir, maintenance.DropInGuardedUnits); err != nil {
		return restoreFenceOnFailure(fmt.Errorf("remove drop-in guards: %w", err), attemptedUnits)
	}
	if err = maintenance.SystemctlReload(ctx); err != nil {
		return restoreFenceOnFailure(fmt.Errorf("daemon-reload after removing guards: %w", err), attemptedUnits)
	}
	guards, checkErr := maintenance.CheckDropInGuards(dropInDir, maintenance.DropInGuardedUnits)
	if checkErr != nil {
		return restoreFenceOnFailure(fmt.Errorf("verify drop-in guards removed: %w", checkErr), attemptedUnits)
	}
	var remainingGuards []string
	for unit, present := range guards {
		if present {
			remainingGuards = append(remainingGuards, unit)
		}
	}
	if len(remainingGuards) > 0 {
		sort.Strings(remainingGuards)
		return restoreFenceOnFailure(fmt.Errorf("drop-in guards still present after removal: %s", strings.Join(remainingGuards, ", ")), attemptedUnits)
	}

	// 8. Only now remove the upgrade hold marker
	if err = maintenance.RemoveUpgradeFence(rootDir); err != nil {
		return restoreFenceOnFailure(fmt.Errorf("remove upgrade fence: %w", err), attemptedUnits)
	}

	// 9. Resume originally active units, re-checking each unit is ACTIVE after
	// start (mirrors the stop-side verification in upgrade fence).
	resumedUnits := []string{}
	var resumeErrs []error
	if !skipRestart && status.Receipt != nil && len(status.Receipt.Units) > 0 {
		for _, unit := range status.Receipt.Units {
			attemptedUnits = append(attemptedUnits, unit)
			if _, startErr := activeBackupUnitAction(ctx, "start", unit); startErr != nil {
				resumeErrs = append(resumeErrs, fmt.Errorf("resume %s: %w", unit, startErr))
				continue
			}
			active, stateErr := activeBackupUnitAction(ctx, "state", unit)
			switch {
			case stateErr != nil:
				resumeErrs = append(resumeErrs, fmt.Errorf("verify %s active after start: %w", unit, stateErr))
			case !active:
				resumeErrs = append(resumeErrs, fmt.Errorf("unit %s is not active after start command", unit))
			default:
				resumedUnits = append(resumedUnits, unit)
			}
		}
	}

	if len(resumeErrs) > 0 {
		return restoreFenceOnFailure(fmt.Errorf("failed to resume some units: %w", errors.Join(resumeErrs...)), maintenance.DefaultWriterUnits)
	}

	if !glass {
		probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		probeErr := activeReleaseRuntimeVerifier(probeCtx, cfg, readiness.AgentStatus.Desired)
		cancel()
		if probeErr != nil {
			return restoreFenceOnFailure(fmt.Errorf("release protocol readiness failed: %w", probeErr), maintenance.DefaultWriterUnits)
		}
	}
	fmt.Println("Upgrade fence released successfully.")
	fmt.Println("Drop-in guards removed and services unlocked.")
	if len(resumedUnits) > 0 {
		fmt.Printf("Restored units: %s\n", strings.Join(resumedUnits, ", "))
	}
	return nil
}

func runUpgradeStatus(ctx context.Context, args []string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	flags := flag.NewFlagSet("upgrade-status", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "format output as JSON")
	rootDir := flags.String("root-dir", currentMaintenanceRoot(), "maintenance root directory")
	dropInDir := flags.String("drop-in-dir", maintenance.DefaultDropInDir, "systemd drop-in directory")
	if err := flags.Parse(args); err != nil {
		return err
	}

	status, err := maintenance.CheckFenceStatus(*rootDir)
	if err != nil {
		return fmt.Errorf("check fence status: %w", err)
	}
	guards, _ := maintenance.CheckDropInGuards(*dropInDir, maintenance.DropInGuardedUnits)
	status.DropIns = guards

	if *asJSON {
		raw, mErr := json.MarshalIndent(status, "", "  ")
		if mErr != nil {
			return mErr
		}
		fmt.Println(string(raw))
		return nil
	}

	fmt.Println("Upgrade Fence Status:")
	fmt.Printf("  Active:             %t\n", status.Active)
	fmt.Printf("  Hold Marker:        %s\n", status.HoldPath)
	if status.Active && status.Meta != nil {
		fmt.Printf("  Version:            %d\n", status.Meta.Version)
		if status.Meta.Generation > 0 {
			fmt.Printf("  Target Generation:  %d\n", status.Meta.Generation)
		}
		fmt.Printf("  Reason:             %s\n", status.Meta.Reason)
		fmt.Printf("  Created At:         %s\n", status.Meta.CreatedAt.Format(time.RFC3339))
		fmt.Printf("  Operator:           %s\n", status.Meta.Operator)
	}
	if status.Active {
		if _, _, rawErr := maintenance.ReadUpgradeFenceRaw(*rootDir); rawErr == nil {
			if _, statErr := os.Stat(filepath.Join(maintenance.Root(*rootDir), DefaultReadinessRecordName)); statErr == nil {
				fmt.Printf("  Readiness Record:   present (%s)\n", DefaultReadinessRecordName)
			} else {
				fmt.Println("  Readiness Record:   missing (run 'urmailctl upgrade readiness-record')")
			}
		}
	}
	fmt.Printf("  Operation Lock:     %s\n", formatLockState(status.OperationLock))

	guardedUnits := []string{}
	for unit, present := range guards {
		if present {
			guardedUnits = append(guardedUnits, unit)
		}
	}
	if len(guardedUnits) > 0 {
		fmt.Printf("  Drop-in Guards:     active (%s)\n", strings.Join(guardedUnits, ", "))
	} else {
		fmt.Println("  Drop-in Guards:     inactive")
	}

	if status.Receipt != nil && len(status.Receipt.Units) > 0 {
		fmt.Printf("  Original Writers:   %s\n", strings.Join(status.Receipt.Units, ", "))
	}

	return nil
}

func formatLockState(held bool) string {
	if held {
		return "HELD (locked)"
	}
	return "free"
}

func runUpgradeResume(ctx context.Context, args []string) error {
	if err := checkUpgradePrivileges(); err != nil {
		return err
	}

	flags := flag.NewFlagSet("upgrade-resume", flag.ContinueOnError)
	timeout := flags.Duration("timeout", 30*time.Second, "timeout waiting for operation lock")
	releaseFlag := flags.Bool("release", false, "unsupported; automatic release is strictly forbidden")
	rootDir := flags.String("root-dir", currentMaintenanceRoot(), "maintenance root directory")
	if err := flags.Parse(args); err != nil {
		return err
	}

	// Reject --release immediately without touching locks, database or fence
	if *releaseFlag {
		return errors.New("cannot use --release with resume: automatic fence release is strictly prohibited to prevent unverified writes; complete migrations, record readiness, and run 'urmailctl upgrade readiness-record' then 'urmailctl upgrade release' (upgrade fence retained)")
	}

	// 1. Initial pre-lock fence check
	status, err := maintenance.CheckFenceStatus(*rootDir)
	if err != nil {
		return err
	}
	if !status.Active {
		return errors.New("no active upgrade fence found; nothing to resume")
	}

	// 2. Acquire operation.lock
	lock, err := maintenance.AcquireLockWithTimeout(ctx, *rootDir, maintenance.DefaultLockName, *timeout)
	if err != nil {
		return fmt.Errorf("acquire operation lock: %w", err)
	}
	defer func() { _ = maintenance.ReleaseLock(lock) }()

	// 3. Post-lock double-check fence existence
	if !maintenance.IsUpgradeHeld(*rootDir) {
		return errors.New("upgrade fence was cleared before acquiring lock; aborting resume")
	}

	// 4. Verify that all writers remain strictly stopped. Do NOT start or stop units.
	var activeWriterErrs []error
	for _, unit := range maintenance.DefaultWriterUnits {
		active, stateErr := activeBackupUnitAction(ctx, "state", unit)
		if stateErr != nil {
			activeWriterErrs = append(activeWriterErrs, fmt.Errorf("inspect writer unit %s: %w", unit, stateErr))
		} else if active {
			activeWriterErrs = append(activeWriterErrs, fmt.Errorf("writer unit %s is active; concurrent writes detected", unit))
		}
	}
	if len(activeWriterErrs) > 0 {
		return fmt.Errorf("cannot resume migrations while writers are running or uninspected: %w (upgrade fence retained)", errors.Join(activeWriterErrs...))
	}

	// 5. Config validation
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w (upgrade fence retained)", err)
	}
	if cfg == nil {
		return errors.New("configuration is nil (upgrade fence retained)")
	}

	migrateURL := cfg.DatabaseURLMigrate
	if migrateURL == "" {
		migrateURL = cfg.DatabaseURLPlatform
	}
	if migrateURL == "" {
		return errors.New("no database migration URL configured (upgrade fence retained)")
	}

	migrator, err := db.NewMigrator(migrateURL)
	if err != nil {
		return fmt.Errorf("database connection failed: %w (upgrade fence retained)", err)
	}
	defer func() { _ = migrator.Close() }()

	fmt.Println("Applying pending database migrations under upgrade fence...")
	if err = migrator.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w (upgrade fence retained)", err)
	}

	v, dirty, vErr := migrator.Version()
	if vErr != nil {
		return fmt.Errorf("verify migration version: %w (upgrade fence retained)", vErr)
	}
	if dirty {
		return fmt.Errorf("database schema is in dirty state at version %d (upgrade fence retained)", v)
	}

	fmt.Printf("Database migrations successfully verified at version %d (clean).\n", v)
	fmt.Println("Pending migrations completed under protective fence.")
	fmt.Println("Run 'urmailctl upgrade readiness-record' then 'urmailctl upgrade release' to complete Phase B release.")
	return nil
}

func runUpgradeRollback(ctx context.Context, args []string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := checkUpgradePrivileges(); err != nil {
		return err
	}

	flags := flag.NewFlagSet("upgrade-rollback", flag.ContinueOnError)
	checkpoint := flags.String("checkpoint", "", "path to pre-upgrade backup checkpoint directory")
	rootDir := flags.String("root-dir", currentMaintenanceRoot(), "maintenance root directory")
	if err := flags.Parse(args); err != nil {
		return err
	}

	status, err := maintenance.CheckFenceStatus(*rootDir)
	if err != nil {
		return err
	}
	if !status.Active {
		return errors.New("no active upgrade fence found for rollback")
	}

	fmt.Println("====================================================================")
	fmt.Println("UPGRADE ROLLBACK WORKFLOW")
	fmt.Println("Upgrade fence remains HELD to prevent dirty database and mail writes.")
	fmt.Println("====================================================================")

	if *checkpoint != "" {
		if err := activeCheckpointVerifier(ctx, *checkpoint); err != nil {
			return err
		}
		info, sErr := os.Stat(*checkpoint)
		if sErr != nil || !info.IsDir() {
			return fmt.Errorf("invalid checkpoint directory: %s", *checkpoint)
		}
		dumpFile := filepath.Join(*checkpoint, "database.dump")
		if _, err = os.Stat(dumpFile); err != nil {
			return fmt.Errorf("checkpoint missing database.dump: %w", err)
		}
		fmt.Printf("Integrity-verified checkpoint at %s; NO restore has been performed.\n", *checkpoint)
		fmt.Println("To restore database dump, execute:")
		fmt.Printf("  runuser -u postgres -- pg_restore --clean --if-exists -d urmail %s\n", dumpFile)
		envFile := filepath.Join(*checkpoint, "urmaild.env")
		if _, err = os.Stat(envFile); err == nil {
			fmt.Printf("  install -o root -g mailinabox -m 0640 %s /etc/urmail/urmaild.env\n", envFile)
		}
	} else {
		fmt.Println("Locate your pre-upgrade checkpoint under /root/urmail-upgrade-backups/")
		fmt.Println("Inspect database.dump and configuration files before restoring.")
	}

	fmt.Println()
	fmt.Println("AFTER restoring database and configs to the previous verified state, either:")
	fmt.Println("  urmailctl upgrade readiness-record   # re-record signed readiness, then 'urmailctl upgrade release'")
	fmt.Println("or, only with explicit acknowledgment of the risk:")
	fmt.Println("  urmailctl upgrade release --break-glass --ack-risk")
	fmt.Println("to remove the fence and restart services.")
	return nil
}
