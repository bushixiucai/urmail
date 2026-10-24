package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

type restoreQuarantineState struct {
	ID             string     `json:"id"`
	StartedAt      time.Time  `json:"started_at"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	ReleaseReason  string     `json:"release_reason,omitempty"`
	SourceIsolated bool       `json:"source_isolated,omitempty"`
}

func createRestoreQuarantine(path string) (restoreQuarantineState, error) {
	var state restoreQuarantineState
	if err := validatePrivateBackupDirectory(filepath.Dir(path)); err != nil {
		return state, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return state, err
	}
	state.ID = hex.EncodeToString(random)
	state.StartedAt = time.Now().UTC()
	if err := os.Mkdir(path, 0o700); err != nil {
		return state, fmt.Errorf("quarantine already exists or cannot be created; retain and inspect it before another restore: %w", err)
	}
	if err := writeQuarantineState(path, state); err != nil {
		return state, err
	}
	return state, syncQuarantineParent(path)
}

func writeQuarantineState(path string, state restoreQuarantineState) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	file, err := root.OpenFile("state.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	dir, err := os.Open(path) // #nosec G304 -- caller validates root-owned directory ancestry before this privileged fixed marker operation.
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

var restoreWriterUnits = []string{"urmail-cert-renew.timer", "urmail-usage-sync.timer", "urmail-cert-renew.service", "urmail-usage-sync.service", "urmail-prometheus.service", "urmail-alertmanager.service", "nginx.service", "php8.1-fpm.service", "php8.2-fpm.service", "php8.3-fpm.service", "php8.4-fpm.service", "php8.5-fpm.service", "php8.6-fpm.service", "urmaild.service", "postfix.service", "dovecot.service", "urmaild-agent.service"}

func stopRestoreWriters(ctx context.Context, stop func(context.Context, string) error) error {
	// Stop scheduling first. Never resume automatically, including on a partial failure.
	for _, unit := range restoreWriterUnits {
		if err := stop(ctx, unit); err != nil {
			return fmt.Errorf("stop restore writer %s (quarantine retained): %w", unit, err)
		}
	}
	return nil
}

func stopSystemdRestoreWriter(ctx context.Context, unit string) error {
	state, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--property=LoadState", "--value", unit).Output() // #nosec G204 G702 -- unit comes exclusively from the fixed restore writer allowlist; no shell.
	if strings.TrimSpace(string(state)) == "not-found" {
		switch unit {
		case "urmaild.service", "postfix.service", "dovecot.service", "urmaild-agent.service", "nginx.service":
			return errors.New("required writer unit is missing; cannot prove writers stopped")
		}
		return nil
	}
	if err != nil {
		return err
	}
	return exec.CommandContext(ctx, "/usr/bin/systemctl", "stop", unit).Run() // #nosec G204 G702 -- unit comes exclusively from stopRestoreWriters' fixed allowlist; no shell.
}

func runRestoreQuarantine(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("restore quarantine must run as root")
	}
	if len(args) == 0 {
		return errors.New("restore quarantine requires status or release")
	}
	path := config.RestoreQuarantineMarker
	if err := validatePrivateBackupDirectory(path); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile("state.json")
	if err != nil {
		return err
	}
	var state restoreQuarantineState
	if err = json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if args[0] == "status" {
		fmt.Printf("restore quarantine id=%s started=%s; writers must remain stopped until reconciliation and explicit release\n", state.ID, state.StartedAt.Format(time.RFC3339))
		return nil
	}
	if args[0] != "release" {
		return errors.New("unknown restore quarantine action")
	}
	operationLock, err := acquireRestoreOperation(path)
	if err != nil {
		return err
	}
	defer func() { _ = operationLock.Close() }()
	id, reason, confirmed, sourceIsolated := "", "", false, false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--restore-id":
			if i+1 < len(args) {
				i++
				id = args[i]
			}
		case "--reason":
			if i+1 < len(args) {
				i++
				reason = strings.TrimSpace(args[i])
			}
		case "--confirm-reconciled":
			confirmed = true
		case "--source-isolated":
			sourceIsolated = true
		default:
			return fmt.Errorf("unknown quarantine release argument %q", args[i])
		}
	}
	if len(state.ID) != 32 || id != state.ID || !confirmed || !sourceIsolated || len(reason) < 10 || len(reason) > 2000 {
		return errors.New("release requires matching --restore-id, --confirm-reconciled, --source-isolated and --reason (10-2000 bytes); reconcile queue, archive spool, marketing claims, financial state and telemetry generation first")
	}
	if err = verifyRestoreCompletion(root, state.ID); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.OpenPlatform(ctx, cfg.DatabaseURLPlatform)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = db.VerifyRestoreIdentity(ctx, cfg.DatabaseURLPlatform, state.ID); err != nil {
		return err
	}
	if err = configureRestoredRoundcube(ctx, operationLock); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]string{"actor_type": "root_operator", "restore_id": state.ID, "reason": reason, "source_isolated": "true"})
	_, err = store.New(pool.Pool).CreateAuditLog(ctx, store.CreateAuditLogParams{Action: "restore.quarantine_released", TargetType: pgtype.Text{String: "restore", Valid: true}, TargetID: pgtype.Text{String: state.ID, Valid: true}, Detail: detail})
	if err != nil {
		return err
	}
	// Preserve the marker's forensic record outside backup-controlled paths.
	now := time.Now().UTC()
	state.ReleasedAt = &now
	state.ReleaseReason = reason
	state.SourceIsolated = true
	released, _ := json.MarshalIndent(state, "", "  ")
	file, err := root.OpenFile("release.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(released); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if err = parent.Rename(filepath.Base(path), "urmail-restore-released-"+state.ID); err != nil {
		return err
	}
	if err = syncQuarantineParent(path); err != nil {
		return err
	}
	fmt.Println("quarantine released; no services started. Inspect and deliberately release the restored Postfix queue and restart selected services. URMAIL_QUARANTINED remains an independent gate.")
	return nil
}

func syncQuarantineParent(path string) error {
	parent, err := os.Open(filepath.Dir(path)) // #nosec G304 -- fixed quarantine parent validated root-owned before mutation.
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return parent.Sync()
}

type restoreCompletion struct {
	RestoreID   string    `json:"restore_id"`
	CompletedAt time.Time `json:"completed_at"`
}

func completeRestoreQuarantine(path, restoreID string) error {
	if err := validatePrivateBackupDirectory(path); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile("state.json")
	if err != nil {
		return err
	}
	var state restoreQuarantineState
	if json.Unmarshal(raw, &state) != nil || state.ID != restoreID {
		return errors.New("restore completion identity mismatch")
	}
	var input, restored restoreInputProof
	if err = readRestoreProof(path, "input.json", &input); err != nil {
		return err
	}
	if err = readRestoreProof(path, "database-restored.json", &restored); err != nil {
		return err
	}
	if input.RestoreID != restoreID || len(input.Fingerprint) != 64 || input != restored {
		return errors.New("database restoration proof does not match completion")
	}
	completed := restoreCompletion{RestoreID: restoreID, CompletedAt: time.Now().UTC()}
	raw, _ = json.Marshal(completed)
	if err = writeQuarantineSidecar(root, "completed.json", raw); err != nil {
		return err
	}
	directory, err := os.Open(path) // #nosec G304 -- private root-owned fixed marker directory validated above.
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
func writeQuarantineSidecar(root *os.Root, name string, raw []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func verifyRestoreCompletion(root *os.Root, id string) error {
	raw, err := root.ReadFile("completed.json")
	if err != nil {
		return errors.New("restore is incomplete; keep quarantine until schema, replay holds, files and runtime privileges finish successfully")
	}
	var completed restoreCompletion
	if json.Unmarshal(raw, &completed) != nil || completed.RestoreID != id || completed.CompletedAt.IsZero() {
		return errors.New("restore completion proof does not match quarantine")
	}
	return nil
}
