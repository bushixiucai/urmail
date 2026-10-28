// Package recall describes bounded, explicitly reviewed local-localCopy cleanup.
package recall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MaxCopies bounds one review to a safe, inspectable localCopy set.
const MaxCopies = 100

// ErrInvalidSnapshot rejects ambiguous or incomplete review identities.
var ErrInvalidSnapshot = errors.New("invalid recall snapshot")

// MessageGUIDPattern accepts bounded opaque Maildir identities, not shell syntax or wildcard selectors.
var MessageGUIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._,+:=@-]{0,511}$`)

var guidPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Copy identifies one immutable local IMAP generation and UID, not a search pattern.
type Copy struct {
	Folder      string `json:"folder"`
	GUID        string `json:"guid"`
	MessageGUID string `json:"message_guid"`
	UIDValidity uint32 `json:"uid_validity"`
	UID         uint32 `json:"uid"`
}

// Key is the only client-selectable localCopy handle. No raw folder path is accepted.
func (c Copy) Key() string {
	return fmt.Sprintf("%s:%d:%d", c.GUID, c.UIDValidity, c.UID)
}

// Source binds a related archived localCopy and its current retention/hold facts.
type Source struct {
	EntryID        int64  `json:"entry_id"`
	MailboxID      int64  `json:"mailbox_id"`
	OriginalSHA256 string `json:"original_sha256"`
	Held           bool   `json:"held"`
	Deleted        bool   `json:"deleted"`
}

// Snapshot is the complete bounded review context.
type Snapshot struct {
	Version        int      `json:"version"`
	TenantID       int64    `json:"tenant_id"`
	ActorID        int64    `json:"actor_id"`
	EntryID        int64    `json:"entry_id"`
	MailboxID      int64    `json:"mailbox_id"`
	Address        string   `json:"address"`
	MessageID      string   `json:"message_id"`
	OriginalSHA256 string   `json:"original_sha256"`
	Held           bool     `json:"held"`
	HoldConflict   bool     `json:"hold_conflict"`
	Deleted        bool     `json:"deleted"`
	TenantStatus   string   `json:"tenant_status"`
	MailboxStatus  string   `json:"mailbox_status"`
	DomainStatus   string   `json:"domain_status"`
	Sources        []Source `json:"sources"`
	Copies         []Copy   `json:"copies"`
}

// Canonical returns a stable, bounded representation and rejects ambiguous IDs.
func (s Snapshot) Canonical() ([]byte, error) {
	if s.Version != 1 || s.TenantID <= 0 || s.ActorID <= 0 || s.EntryID <= 0 || s.MailboxID <= 0 || s.Address == "" || s.MessageID == "" || len(s.Copies) > MaxCopies || len(s.Sources) > MaxCopies {
		return nil, ErrInvalidSnapshot
	}
	s.Sources = append([]Source{}, s.Sources...)
	sort.Slice(s.Sources, func(i, j int) bool { return s.Sources[i].EntryID < s.Sources[j].EntryID })
	s.Copies = append([]Copy{}, s.Copies...)
	sort.Slice(s.Copies, func(i, j int) bool { return s.Copies[i].Key() < s.Copies[j].Key() })
	seen := map[string]bool{}
	for _, localCopy := range s.Copies {
		key := localCopy.Key()
		if !guidPattern.MatchString(localCopy.GUID) || !MessageGUIDPattern.MatchString(localCopy.MessageGUID) || localCopy.UIDValidity == 0 || localCopy.UID == 0 || localCopy.Folder == "" || len(localCopy.Folder) > 1024 || strings.ContainsAny(localCopy.Folder, "\x00\r\n") || seen[key] {
			return nil, ErrInvalidSnapshot
		}
		seen[key] = true
	}
	return json.Marshal(s)
}

// Digest is the review proof for the exact snapshot, never an authorization alone.
func (s Snapshot) Digest() (string, error) {
	raw, err := s.Canonical()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// Select validates explicit localCopy keys against the reviewed set, preserving no wildcard.
func (s Snapshot) Select(keys []string) ([]Copy, error) {
	if len(keys) == 0 || len(keys) > MaxCopies {
		return nil, ErrInvalidSnapshot
	}
	available := map[string]Copy{}
	for _, localCopy := range s.Copies {
		available[localCopy.Key()] = localCopy
	}
	seen := map[string]bool{}
	selected := make([]Copy, 0, len(keys))
	for _, key := range keys {
		localCopy, ok := available[key]
		if !ok || seen[key] {
			return nil, ErrInvalidSnapshot
		}
		seen[key] = true
		selected = append(selected, localCopy)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Key() < selected[j].Key() })
	return selected, nil
}

// Refusal explains why no local destructive action may be offered.
func (s Snapshot) Refusal() string {
	switch {
	case len(s.OriginalSHA256) != 64:
		return "archive_source_unverified"
	case s.Held:
		return "legal_hold"
	case s.HoldConflict:
		return "legal_hold_conflict"
	case s.Deleted:
		return "archive_source_deleted"
	case s.TenantStatus != "active" || s.MailboxStatus != "active" || s.DomainStatus != "active":
		return "local_target_unavailable"
	case len(s.Copies) == 0:
		return "local_message_not_found"
	default:
		return ""
	}
}

// ItemResult states only facts observed for one reviewed localCopy.
type ItemResult struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Result preserves partial and ambiguous outcomes; a failed acknowledgement is never success.
type Result struct {
	Items []ItemResult `json:"items"`
}
