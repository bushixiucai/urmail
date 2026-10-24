package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/config"
)

type restoreInputProof struct{ RestoreID, Fingerprint string }

// restoreInputFingerprint binds content, artifact roles and target identity,
// excluding passwords. It is private bounded recovery proof, not business data.
func restoreInputFingerprint(databaseURL string, files []string) (string, error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return "", errors.New("invalid restore target")
	}
	parts := []any{cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.RuntimeParams["role"]}
	for _, path := range files {
		hash := ""
		if path != "" {
			hash, err = fileSHA256(path)
			if err != nil {
				return "", err
			}
		}
		parts = append(parts, hash)
	}
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func writeRestoreProof(path, name string, value any) error {
	if err := validatePrivateBackupDirectory(path); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = writeQuarantineSidecar(root, name, raw); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
func readRestoreProof(path, name string, value any) error {
	if err := validatePrivateBackupDirectory(path); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("restore proof is not a private regular file")
	}
	raw, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
func resumeRestoreQuarantine(path, id, fingerprint string) (restoreQuarantineState, bool, error) {
	var state restoreQuarantineState
	if err := readRestoreProof(path, "state.json", &state); err != nil {
		return state, false, err
	}
	if len(id) != 32 || state.ID != id {
		return state, false, errors.New("restore resume identity mismatch")
	}
	var input restoreInputProof
	if err := readRestoreProof(path, "input.json", &input); err != nil {
		return state, false, err
	}
	if input.RestoreID != id || input.Fingerprint != fingerprint {
		return state, false, errors.New("restore resume artifacts or target do not match")
	}
	var completed restoreCompletion
	if err := readRestoreProof(path, "completed.json", &completed); err == nil {
		return state, false, errors.New("restore already completed; reconcile and explicitly release it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return state, false, err
	}
	var restored restoreInputProof
	err := readRestoreProof(path, "database-restored.json", &restored)
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	if restored != input {
		return state, false, fmt.Errorf("restored database stage proof mismatch")
	}
	return state, true, nil
}
func restoreResumeCredentials(path, id string) (map[string]string, error) {
	var state restoreQuarantineState
	if err := readRestoreProof(path, "state.json", &state); err != nil {
		return nil, err
	}
	if state.ID != id {
		return nil, errors.New("restore resume identity mismatch")
	}
	var values map[string]string
	err := readRestoreProof(path, "target-credentials.json", &values)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	return values, err
}

// acquireRestoreOperation serializes initial/resumed restores and release.
// The descriptor is inherited only by fixed restore tools, so a killed parent
// cannot let a second restore race a still-running pg_restore or tar child.
func acquireRestoreOperation(path string) (*os.File, error) {
	if err := validatePrivateBackupDirectory(path); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile("operation.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = file.Close()
		return nil, errors.New("invalid restore operation lock")
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another restore or release operation is still running")
	}
	return file, nil
}

// configureRestoredRoundcube preserves the target-private provisioning state.
// It never installs packages, starts services or takes paths from a backup.
func configureRestoredRoundcube(ctx context.Context, lock *os.File) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.RoundcubeEnabled {
		return nil
	}
	info, err := os.Lstat("/usr/share/urmail/roundcube/manage.sh")
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("target Roundcube runtime is missing; keep quarantine and provision the target runtime")
	}
	command := exec.CommandContext(ctx, "/usr/share/urmail/roundcube/manage.sh", "configure")
	command.ExtraFiles = append(command.ExtraFiles, lock)
	// The helper validates fixed loopback/same-database credentials. Suppress
	// subprocess output here so DSNs cannot escape through a diagnostic failure.
	if err = command.Run(); err != nil {
		return errors.New("target Roundcube configuration failed; quarantine retained")
	}
	return nil
}
