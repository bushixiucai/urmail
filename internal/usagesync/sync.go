package usagesync

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
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/maintenance"
	"github.com/bushixiucai/urmail/internal/store"
)

// Options configures one mailbox storage usage synchronization pass.
type Options struct {
	Pool            *db.Pool
	StorageRoot     string
	MaintenanceRoot string
	SkipHoldCheck   bool
	MeasureMailbox  func(ctx context.Context, storageRoot, address string) (int64, error)
}

// ParseQuotaStorage reads Dovecot's canonical KiB quota accounting.
func ParseQuotaStorage(raw []byte) (int64, error) {
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

// ParseMaildirQuotaBytes reads the exact byte ledger used by quota=maildir.
func ParseMaildirQuotaBytes(r io.Reader) (int64, error) {
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

// ReadMaildirQuotaBytes reads maildirsize directly from a shared Maildir volume
// without requiring root privileges or systemd.
func ReadMaildirQuotaBytes(root, address string) (int64, error) {
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
	return ParseMaildirQuotaBytes(file)
}

// QuotaDisplayKiB rounds exact bytes up to KiB to match Dovecot quota display.
func QuotaDisplayKiB(bytes int64) int64 {
	value := bytes / 1024
	if bytes%1024 != 0 {
		value++
	}
	return value
}

// DefaultMeasureMailbox uses doveadm when present in the container/host, and
// falls back to reading maildirsize directly from the shared Maildir volume
// when doveadm is not installed (for example inside urmaild/urmaild-agent).
func DefaultMeasureMailbox(ctx context.Context, storageRoot, address string) (int64, error) {
	if _, statErr := os.Stat("/usr/bin/doveadm"); statErr == nil {
		cmd := exec.CommandContext(ctx, "/usr/bin/doveadm", "-f", "json", "quota", "get", "-u", address) // #nosec G204 -- fixed binary and flags; mailbox is a separate validated database argument.
		raw, err := cmd.Output()
		if err != nil {
			return 0, err
		}
		used, err := ParseQuotaStorage(raw)
		if err != nil {
			return 0, err
		}
		exact, err := ReadMaildirQuotaBytes(storageRoot, address)
		if err != nil {
			return 0, err
		}
		if QuotaDisplayKiB(exact) != used/1024 {
			return 0, fmt.Errorf("quota changed during measurement")
		}
		return exact, nil
	}
	return ReadMaildirQuotaBytes(storageRoot, address)
}

// Sync measures active mailboxes and records their usage in the platform store.
func Sync(ctx context.Context, opts Options) (int, error) {
	if opts.Pool == nil {
		return 0, errors.New("usage sync requires a platform database pool")
	}
	if !opts.SkipHoldCheck && opts.MaintenanceRoot != "" {
		if maintenance.IsUpgradeHeld(opts.MaintenanceRoot) {
			return 0, errors.New("usage sync refused during restore or upgrade hold")
		}
		lock, err := maintenance.AcquireLockWithTimeout(ctx, opts.MaintenanceRoot, maintenance.DefaultLockName, 30*time.Second)
		if err != nil {
			return 0, fmt.Errorf("usage sync lock: %w", err)
		}
		defer func() { _ = maintenance.ReleaseLock(lock) }()
		if maintenance.IsUpgradeHeld(opts.MaintenanceRoot) {
			return 0, errors.New("usage sync refused during restore or upgrade hold")
		}
	}

	measure := opts.MeasureMailbox
	if measure == nil {
		measure = DefaultMeasureMailbox
	}

	q := db.Queries(opts.Pool.Pool)
	rows, err := q.PlatformUsageMailboxes(ctx)
	if err != nil {
		return 0, err
	}
	failures := 0
	for _, row := range rows {
		used, measureErr := measure(ctx, opts.StorageRoot, row.Address)
		if measureErr != nil {
			failures++
			if err = q.PlatformRecordMailboxUsageError(ctx, store.PlatformRecordMailboxUsageErrorParams{
				TenantID: row.TenantID,
				ID:       row.ID,
			}); err != nil {
				return len(rows), err
			}
			continue
		}
		if err = q.PlatformRecordMailboxUsage(ctx, store.PlatformRecordMailboxUsageParams{
			TenantID:  row.TenantID,
			ID:        row.ID,
			BytesUsed: used,
		}); err != nil {
			return len(rows), err
		}
	}
	if failures > 0 {
		return len(rows), fmt.Errorf("storage measurements failed for %d mailboxes; usage remains unknown", failures)
	}
	return len(rows), nil
}
