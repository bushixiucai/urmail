package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultUpgradeAuditFile is the append-only JSONL audit trail for upgrade
// releases that bypass the default signed-readiness authorization.
const DefaultUpgradeAuditFile = "upgrade-audit.jsonl"

// UpgradeAuditEntry is one immutable audit record for a non-default release
// authorization (break-glass) or a completed release of any mode. It never
// records credentials, only their shape.
type UpgradeAuditEntry struct {
	Time            time.Time `json:"time"`
	Operator        string    `json:"operator"`
	Action          string    `json:"action"`
	Mode            string    `json:"mode"`
	SkipRestart     bool      `json:"skip_restart,omitempty"`
	Generation      uint64    `json:"generation,omitempty"`
	FenceReason     string    `json:"fence_reason,omitempty"`
	HoldSHA256      string    `json:"hold_sha256,omitempty"`
	ReceiptSHA256   string    `json:"receipt_sha256,omitempty"`
	ReadinessSHA256 string    `json:"readiness_sha256,omitempty"`
	Guards          []string  `json:"guards,omitempty"`
	Detail          string    `json:"detail,omitempty"`
}

// AppendUpgradeAudit appends one JSON line to the host-local upgrade audit
// trail and fsyncs it, so an exception release always leaves a durable record.
func AppendUpgradeAudit(rootDir string, entry UpgradeAuditEntry) error {
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode upgrade audit entry: %w", err)
	}
	path := filepath.Join(rootDir, DefaultUpgradeAuditFile)
	info, err := os.Lstat(path)
	if err == nil && info.IsDir() {
		return errors.New("upgrade audit path is not a regular file")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) // #nosec G304 -- path is the fixed maintenance root joined with a constant file name.
	if err != nil {
		return fmt.Errorf("open upgrade audit trail: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err = file.Chmod(0o600); err != nil {
		return fmt.Errorf("harden upgrade audit trail permissions: %w", err)
	}
	if _, err = file.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("append upgrade audit entry: %w", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("sync upgrade audit entry: %w", err)
	}
	return nil
}

// ReadUpgradeAuditTail returns up to limit most recent audit entries.
func ReadUpgradeAuditTail(rootDir string, limit int) ([]UpgradeAuditEntry, error) {
	payload, err := os.ReadFile(filepath.Join(rootDir, DefaultUpgradeAuditFile)) // #nosec G304 -- path is the fixed maintenance root joined with a constant file name.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	entries := []UpgradeAuditEntry{}
	start := 0
	for end := 0; end <= len(payload); end++ {
		if end == len(payload) || payload[end] == '\n' {
			if end > start {
				var entry UpgradeAuditEntry
				if json.Unmarshal(payload[start:end], &entry) == nil {
					entries = append(entries, entry)
				}
			}
			start = end + 1
		}
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}
