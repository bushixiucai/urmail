// Package supportdiag builds redacted support diagnostic bundles.
//
// Everything here is pure and deterministic: the same Input (including the
// generated-at timestamp) produces byte-identical output. The bundle is an
// allowlist, not a log dump - a field exists only if this package names it,
// and every free-form-looking value is either an enumerated status or a
// pseudonymized digest. Message bodies, credentials and raw addresses are
// structurally impossible to include because no input field carries them.
//
// Sharing is consent-gated: Build refuses to run without the tenant's
// explicit ShareWithSupport flag, so a bundle can only come from a tenant
// who saw what would be shared.
package supportdiag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// BundleVersion is bumped whenever the deterministic field list changes.
const BundleVersion = "1"

// Limits bound the bundle. A support bundle is a summary, not an archive.
const (
	MaxBounceCodes = 50
	MaxAddresses   = 100
)

// ErrConsentRequired is returned when the tenant has not consented to share
// diagnostics with the platform support team.
var ErrConsentRequired = errors.New("diagnostic sharing requires explicit tenant consent")

// ErrTooMany is returned when an input list exceeds its bundle limit.
var ErrTooMany = errors.New("diagnostic input exceeds bundle limits")

// DeliveryCounters summarizes tenant delivery health over the last 24h.
type DeliveryCounters struct {
	Sent      int64
	Deferred  int64
	Failed    int64
	QueuedNow int64
}

// BounceCode is one observed SMTP bounce status and how often it fired.
type BounceCode struct {
	Code       string
	Count      int64
	LastSeenAt time.Time
}

// MailboxFlags counts mailboxes by status; no identifiers are included.
type MailboxFlags struct {
	Total     int
	Active    int
	Suspended int
	OverQuota int
}

// ConfigStatus compares desired and applied configuration generations.
type ConfigStatus struct {
	DesiredGeneration int64
	AppliedGeneration int64
}

// Drifted reports whether the applied generation lags the desired one.
func (c ConfigStatus) Drifted() bool { return c.AppliedGeneration != c.DesiredGeneration }

// BackupFreshness summarizes backup recency without paths or locations.
type BackupFreshness struct {
	LastSuccessAt *time.Time
	LastStatus    string
}

// AgeHours is the hours since the last successful backup, or -1 when the
// tenant has never had one.
func (b BackupFreshness) AgeHours(now time.Time) float64 {
	if b.LastSuccessAt == nil {
		return -1
	}
	return now.Sub(*b.LastSuccessAt).Hours()
}

// Input is the tenant-scoped set of facts a bundle may be built from.
type Input struct {
	TenantID         int64
	ClientVersion    string
	ShareWithSupport bool
	GeneratedAt      time.Time
	Delivery         DeliveryCounters
	Bounces          []BounceCode
	Mailboxes        MailboxFlags
	Config           ConfigStatus
	Backup           BackupFreshness
	// Addresses are sample addresses related to the reported problem.
	// They are pseudonymized before entering the bundle.
	Addresses []string
}

// Field is one deterministic key/value pair of the bundle.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Bundle is the redacted artifact a tenant previews and support receives.
type Bundle struct {
	Version     string  `json:"bundle_version"`
	TenantID    int64   `json:"tenant_id"`
	GeneratedAt string  `json:"generated_at"`
	Fields      []Field `json:"fields"`
}

// Pseudonymize maps an address to a stable, non-reversible token. The tenant
// identifier is never hashed; it is support's primary lookup key.
func Pseudonymize(address string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(address))))
	return "user_" + hex.EncodeToString(sum[:8])
}

// Build deterministically renders the redacted bundle.
func Build(in Input) (Bundle, error) {
	if !in.ShareWithSupport {
		return Bundle{}, ErrConsentRequired
	}
	if len(in.Bounces) > MaxBounceCodes {
		return Bundle{}, fmt.Errorf("%w: %d bounce codes > %d", ErrTooMany, len(in.Bounces), MaxBounceCodes)
	}
	if len(in.Addresses) > MaxAddresses {
		return Bundle{}, fmt.Errorf("%w: %d addresses > %d", ErrTooMany, len(in.Addresses), MaxAddresses)
	}

	bounces := append([]BounceCode(nil), in.Bounces...)
	sort.Slice(bounces, func(i, j int) bool { return bounces[i].Code < bounces[j].Code })

	addresses := append([]string(nil), in.Addresses...)
	sort.Strings(addresses)
	pseudonyms := make([]string, 0, len(addresses))
	for _, a := range addresses {
		if strings.TrimSpace(a) == "" {
			continue
		}
		pseudonyms = append(pseudonyms, Pseudonymize(a))
	}

	backupAge := fmt.Sprintf("%.1f", in.Backup.AgeHours(in.GeneratedAt))
	if in.Backup.AgeHours(in.GeneratedAt) < 0 {
		backupAge = "never"
	}
	backupStatus := in.Backup.LastStatus
	if backupStatus == "" {
		backupStatus = "unknown"
	}
	client := in.ClientVersion
	if client == "" {
		client = "unspecified"
	}

	fields := []Field{
		{"delivery.sent_24h", fmt.Sprint(in.Delivery.Sent)},
		{"delivery.deferred_24h", fmt.Sprint(in.Delivery.Deferred)},
		{"delivery.failed_24h", fmt.Sprint(in.Delivery.Failed)},
		{"delivery.queued_now", fmt.Sprint(in.Delivery.QueuedNow)},
		{"mailboxes.total", fmt.Sprint(in.Mailboxes.Total)},
		{"mailboxes.active", fmt.Sprint(in.Mailboxes.Active)},
		{"mailboxes.suspended", fmt.Sprint(in.Mailboxes.Suspended)},
		{"mailboxes.over_quota", fmt.Sprint(in.Mailboxes.OverQuota)},
		{"config.desired_generation", fmt.Sprint(in.Config.DesiredGeneration)},
		{"config.applied_generation", fmt.Sprint(in.Config.AppliedGeneration)},
		{"config.drifted", fmt.Sprint(in.Config.Drifted())},
		{"backup.age_hours", backupAge},
		{"backup.last_status", backupStatus},
		{"client.version", client},
	}
	for _, b := range bounces {
		fields = append(fields, Field{"bounce." + sanitizeCode(b.Code), fmt.Sprintf("count=%d last_seen=%s", b.Count, b.LastSeenAt.UTC().Format(time.RFC3339))})
	}
	for _, p := range pseudonyms {
		fields = append(fields, Field{"affected.address", p})
	}
	return Bundle{
		Version:     BundleVersion,
		TenantID:    in.TenantID,
		GeneratedAt: in.GeneratedAt.UTC().Format(time.RFC3339),
		Fields:      fields,
	}, nil
}

// sanitizeCode keeps bounce codes to digits separated by dashes so the
// field namespace cannot smuggle arbitrary keys or secret-shaped text.
func sanitizeCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(code)) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// JSON renders the bundle canonically (sorted keys, compact) so that the
// same bundle hashes identically wherever it is checked.
func (b Bundle) JSON() (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	var ordered map[string]interface{}
	if err := json.Unmarshal(raw, &ordered); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(ordered)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}
