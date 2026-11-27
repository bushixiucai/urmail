// Package reversemigrate implements the Phase 4 E7 post-release SQLite-to-PG
// reverse migration and incremental merge engine with two-pass bidirectional
// reconciliation and fail-closed isolation checks.
package reversemigrate

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// LockState represents the mandatory three-state isolation lock status.
type LockState string

// LockState values enforce fail-closed isolation reporting.
const (
	LockStateLocked      LockState = "locked"
	LockStateUnlocked    LockState = "unlocked"
	LockStateUnconfirmed LockState = "unconfirmed"
)

// IsolationCheckInput supplies runtime observations for §3 pre-merge checks.
type IsolationCheckInput struct {
	HoldFilePath        string
	ExpectedGeneration  string
	ListenersStopped    *bool
	WritersStopped      *bool
	SQLiteWALClean      *bool
	PGActiveWritersZero *bool
}

// IsolationReport records the three-state lock verification outcome.
type IsolationReport struct {
	State            LockState `json:"state"`
	HoldPresent      bool      `json:"hold_present"`
	Generation       string    `json:"generation,omitempty"`
	ListenersStopped bool      `json:"listeners_stopped"`
	WritersStopped   bool      `json:"writers_stopped"`
	SQLiteWALClean   bool      `json:"sqlite_wal_clean"`
	PGActiveWriters0 bool      `json:"pg_active_writers_zero"`
	FailureReason    string    `json:"failure_reason,omitempty"`
	VerifiedAt       time.Time `json:"verified_at"`
}

// VerifyIsolation enforces §3 / §4.6 fail-closed three-state lock verification.
func VerifyIsolation(in IsolationCheckInput, now time.Time) (IsolationReport, error) {
	rep := IsolationReport{
		State:      LockStateUnconfirmed,
		VerifiedAt: now.UTC(),
	}
	if in.HoldFilePath == "" {
		rep.FailureReason = "hold file path is required"
		return rep, errors.New(rep.FailureReason)
	}
	raw, err := os.ReadFile(in.HoldFilePath) // #nosec G304 -- operator-configured hold file path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			rep.State = LockStateUnlocked
			rep.FailureReason = fmt.Sprintf("hold file missing at %s", in.HoldFilePath)
			return rep, errors.New(rep.FailureReason)
		}
		rep.State = LockStateUnconfirmed
		rep.FailureReason = fmt.Sprintf("cannot read hold file %s: %v", in.HoldFilePath, err)
		return rep, errors.New(rep.FailureReason)
	}
	rep.HoldPresent = true
	rep.Generation = strings.TrimSpace(string(raw))
	if in.ExpectedGeneration != "" && !strings.Contains(rep.Generation, in.ExpectedGeneration) {
		rep.State = LockStateUnconfirmed
		rep.FailureReason = fmt.Sprintf("hold generation mismatch: got %q, want %q", rep.Generation, in.ExpectedGeneration)
		return rep, errors.New(rep.FailureReason)
	}
	if in.ListenersStopped == nil || in.WritersStopped == nil || in.SQLiteWALClean == nil || in.PGActiveWritersZero == nil {
		rep.State = LockStateUnconfirmed
		rep.FailureReason = "incomplete writer/listener isolation observation"
		return rep, errors.New(rep.FailureReason)
	}
	rep.ListenersStopped = *in.ListenersStopped
	rep.WritersStopped = *in.WritersStopped
	rep.SQLiteWALClean = *in.SQLiteWALClean
	rep.PGActiveWriters0 = *in.PGActiveWritersZero

	if !rep.ListenersStopped || !rep.WritersStopped || !rep.SQLiteWALClean || !rep.PGActiveWriters0 {
		rep.State = LockStateUnlocked
		rep.FailureReason = fmt.Sprintf("active writers/listeners detected (listeners_stopped=%v writers_stopped=%v wal_clean=%v pg_writers_zero=%v)",
			rep.ListenersStopped, rep.WritersStopped, rep.SQLiteWALClean, rep.PGActiveWriters0)
		return rep, errors.New(rep.FailureReason)
	}
	rep.State = LockStateLocked
	return rep, nil
}

// Config configures one E7 reverse-migration / dry-run execution.
type Config struct {
	DryRun              bool
	Isolation           IsolationCheckInput
	SQLitePath          string
	TargetPGURL         string
	BaselinePKs         map[string]map[string]bool
	SrcMaildir          string
	TgtMaildir          string
	QuarantineDir       string
	AllowSharedMaildir  bool
	SrcQueueItems       []QueueItem
	TgtQueueItems       []QueueItem
	DeliveredMIMEHashes map[string]bool
	OutQueueDir         string
	SrcTLSCert          string
	SrcTLSKey           string
	TgtTLSCert          string
	TgtTLSKey           string
	TLSBackupDir        string
	ZoneFiles           []string
	ManagedZoneChanged  bool
	Now                 time.Time
}

// Report is the structured JSON outcome of E7 reverse migration or dry-run.
type Report struct {
	Mode            string                `json:"mode"`
	DryRun          bool                  `json:"dry_run"`
	NoSLACommitment bool                  `json:"no_sla_commitment"`
	RetentionNote   string                `json:"retention_note"`
	Isolation       IsolationReport       `json:"isolation"`
	DB              *DBReport             `json:"db,omitempty"`
	Maildir         *MaildirReport        `json:"maildir,omitempty"`
	IMAP            *IMAPReport           `json:"imap,omitempty"`
	Queue           *QueueReport          `json:"queue,omitempty"`
	TLS             *TLSReport            `json:"tls,omitempty"`
	DNS             *DNSReport            `json:"dns,omitempty"`
	Reconciliation  *ReconciliationReport `json:"reconciliation,omitempty"`
	PlanActions     []string              `json:"plan_actions"`
}

// DBReport summarizes SQLite -> PG reverse table migration and hard-delete sync.
type DBReport struct {
	TablesProcessed int      `json:"tables_processed"`
	RowsUpserted    int      `json:"rows_upserted"`
	RowsDeleted     int      `json:"rows_deleted"`
	SequencesReset  int      `json:"sequences_reset"`
	ZeroDiff        bool     `json:"zero_diff"`
	Actions         []string `json:"actions,omitempty"`
}

// MaildirReport summarizes Maildir incremental file sync.
type MaildirReport struct {
	FilesCopied      int      `json:"files_copied"`
	FilesQuarantined int      `json:"files_quarantined"`
	FilesVerified    int      `json:"files_verified"`
	SharedVolumeMode bool     `json:"shared_volume_mode"`
	Actions          []string `json:"actions,omitempty"`
}

// IMAPReport summarizes IMAP UIDVALIDITY, uidlist, keywords, subscriptions, and sieve sync.
type IMAPReport struct {
	MailboxesChecked int      `json:"mailboxes_checked"`
	UIDValidityMatch bool     `json:"uid_validity_match"`
	SieveSynced      int      `json:"sieve_synced"`
	SvbinPurged      int      `json:"svbin_purged"`
	Actions          []string `json:"actions,omitempty"`
}

// QueueReport summarizes Postfix queue compound-key handoff and deduplication.
type QueueReport struct {
	SourceItems      int      `json:"source_items"`
	SkippedDelivered int      `json:"skipped_delivered"`
	SkippedTargetDup int      `json:"skipped_target_dup"`
	HandoffQueued    int      `json:"handoff_queued"`
	Actions          []string `json:"actions,omitempty"`
}

// TLSReport summarizes algorithm-agnostic TLS pubkey verification and atomic sync.
type TLSReport struct {
	Verified     bool     `json:"verified"`
	PubkeySHA256 string   `json:"pubkey_sha256,omitempty"`
	Updated      bool     `json:"updated"`
	Algorithm    string   `json:"algorithm,omitempty"`
	NotAfter     string   `json:"not_after,omitempty"`
	Actions      []string `json:"actions,omitempty"`
}

// DNSReport summarizes Q-S2 zero-DNS-cutover and managed zone syntax checks.
type DNSReport struct {
	ZeroDNSCutover bool     `json:"zero_dns_cutover"`
	ZonesChecked   int      `json:"zones_checked"`
	Verified       bool     `json:"verified"`
	Actions        []string `json:"actions,omitempty"`
}

// ReconciliationReport summarizes the mandatory pass-2 bidirectional checks.
type ReconciliationReport struct {
	Passed       bool     `json:"passed"`
	Checks       []string `json:"checks"`
	FailureCause string   `json:"failure_cause,omitempty"`
}
