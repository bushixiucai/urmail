package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// parseQuotaStorage reads Dovecot's canonical KiB quota accounting.
func parseQuotaStorage(raw []byte) (int64, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return 0, err
	}
	for _, row := range rows {
		var kind string
		_ = json.Unmarshal(row["type"], &kind)
		if !strings.EqualFold(kind, "STORAGE") {
			continue
		}
		text := strings.Trim(string(row["value"]), `"`)
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n < 0 || n > (1<<63-1)/1024 {
			return 0, fmt.Errorf("invalid Dovecot storage value")
		}
		return n * 1024, nil
	}
	return 0, fmt.Errorf("dovecot did not return a storage measurement")
}
func runUsageSync(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "sync" {
		return fmt.Errorf("usage requires sync")
	}
	if os.Geteuid() != 0 && os.Getenv("URMAIL_ALLOW_TEST_USER") == "" {
		return fmt.Errorf("usage sync must run as root")
	}
	if backupWritersHeld() {
		return errors.New("usage sync refused during restore or upgrade hold")
	}
	lock, err := backupLock(currentMaintenanceRoot(), "operation.lock")
	if err != nil {
		return fmt.Errorf("usage sync lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if backupWritersHeld() {
		return errors.New("usage sync refused during restore or upgrade hold")
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
	q := store.New(pool.Pool)
	rows, err := q.PlatformUsageMailboxes(ctx)
	if err != nil {
		return err
	}
	failures := 0
	for _, row := range rows {
		// The generated userdb allows the internal doveadm service to measure
		// retained disabled/deleted Maildirs without enabling passdb auth.
		cmd := exec.CommandContext(ctx, "/usr/bin/doveadm", "-f", "json", "quota", "get", "-u", row.Address) // #nosec G204 -- fixed binary and flags; mailbox is a separate validated database argument.
		raw, measureErr := cmd.Output()
		var used int64
		if measureErr == nil {
			used, measureErr = parseQuotaStorage(raw)
			if measureErr == nil {
				var exact int64
				exact, measureErr = readMaildirQuotaBytes(cfg.StorageRoot, row.Address)
				if measureErr == nil && quotaDisplayKiB(exact) != used/1024 {
					measureErr = fmt.Errorf("quota changed during measurement")
				}
				used = exact
			}
		}
		if measureErr != nil {
			failures++
			if err = q.PlatformRecordMailboxUsageError(ctx, store.PlatformRecordMailboxUsageErrorParams{TenantID: row.TenantID, ID: row.ID}); err != nil {
				return err
			}
			continue
		}
		if err = q.PlatformRecordMailboxUsage(ctx, store.PlatformRecordMailboxUsageParams{TenantID: row.TenantID, ID: row.ID, BytesUsed: used}); err != nil {
			return err
		}
	}
	if failures > 0 {
		return fmt.Errorf("storage measurements failed for %d mailboxes; usage remains unknown", failures)
	}
	fmt.Printf("storage measurements updated for %d mailboxes\n", len(rows))
	return nil
}

// parseMaildirQuotaBytes reads the exact byte ledger used by quota=maildir.
// quota get rounds up to KiB; the ledger preserves exact byte accounting.
func parseMaildirQuotaBytes(r io.Reader) (int64, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return 0, fmt.Errorf("maildir quota header missing")
	}
	var total int64
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return 0, fmt.Errorf("invalid maildir quota ledger")
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, err
		}
		if (n > 0 && total > (1<<63-1)-n) || (n < 0 && total < (-1<<63)-n) {
			return 0, fmt.Errorf("quota ledger overflow")
		}
		total += n
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if total < 0 {
		return 0, fmt.Errorf("negative quota ledger")
	}
	return total, nil
}
func readMaildirQuotaBytes(root, address string) (int64, error) {
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" || !validation.IsValidHostname(domain) || local == "." || local == ".." || strings.ContainsAny(local+domain, "/\\") {
		return 0, fmt.Errorf("unsafe mailbox storage identity")
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, err
	}
	filename := filepath.Join(base, "mail", "mailboxes", domain, local, "maildirsize")
	actual, err := filepath.EvalSymlinks(filename)
	if err != nil || actual != filename {
		return 0, fmt.Errorf("quota ledger path contains a symbolic link")
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("quota ledger is not a regular file")
	}
	file, err := os.Open(filename) // #nosec G304 -- validated mailbox identity, resolved storage root, and regular-file checks constrain this path.
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return 0, fmt.Errorf("quota ledger changed while opening")
	}
	return parseMaildirQuotaBytes(file)
}

func quotaDisplayKiB(bytes int64) int64 {
	value := bytes / 1024
	if bytes%1024 != 0 {
		value++
	}
	return value
}
