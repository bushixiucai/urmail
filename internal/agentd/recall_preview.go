package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	recall "github.com/bushixiucai/urmail/internal/domain/recall"
	"github.com/bushixiucai/urmail/internal/store"
)

// RecallPreviewParams contains database identities only, never user-selected
// addresses, Message-IDs, folder paths, GUIDs, or executable commands.
type RecallPreviewParams struct {
	TenantID int64 `json:"tenant_id"`
	EntryID  int64 `json:"entry_id"`
	ActorID  int64 `json:"actor_id"`
}

// PreviewRecall reads a role-authorized source and exact current local copies.
// It acquires the same tenant/hold serialization fence as the mutation path.
func (m *ApplyManager) PreviewRecall(raw json.RawMessage) (string, string, error) {
	var params RecallPreviewParams
	if err := strictParams(raw, &params); err != nil {
		return "", "", err
	}
	if params.TenantID <= 0 || params.EntryID <= 0 || params.ActorID <= 0 || m.pool == nil {
		return "", "", ErrRejected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return "", "", ErrFailed
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	if err = q.PlatformLockRecallTenant(ctx, params.TenantID); err != nil {
		return "", "", ErrFailed
	}
	snapshot, err := m.recallSnapshot(ctx, q, params)
	if err != nil {
		return "", "", err
	}
	result, err := snapshot.Canonical()
	if err != nil {
		return "", "", ErrRejected
	}
	return string(result), "", nil
}
func (m *ApplyManager) recallSnapshot(ctx context.Context, q *store.Queries, params RecallPreviewParams) (recall.Snapshot, error) {
	source, err := q.PlatformRecallPreviewSource(ctx, store.PlatformRecallPreviewSourceParams{TenantID: params.TenantID, EntryID: params.EntryID, ActorID: params.ActorID})
	if err != nil {
		return recall.Snapshot{}, ErrRejected
	}
	if !source.MailboxID.Valid || !source.Address.Valid || !safeKickAddress(source.Address.String) || len(source.MessageID) < 3 || len(source.MessageID) > 998 || strings.ContainsAny(source.MessageID, "\r\n\x00") || !strings.HasPrefix(source.MessageID, "<") || !strings.HasSuffix(source.MessageID, ">") {
		return recall.Snapshot{}, ErrRejected
	}
	snapshot := recall.Snapshot{Version: 1, TenantID: source.TenantID, ActorID: params.ActorID, EntryID: source.ID, MailboxID: source.MailboxID.Int64, Address: source.Address.String, MessageID: source.MessageID, OriginalSHA256: source.OriginalSha256, Held: source.LegalHold, HoldConflict: source.HoldConflict, Deleted: source.DeletedAt.Valid, TenantStatus: source.TenantStatus, MailboxStatus: source.MailboxStatus.String, DomainStatus: source.DomainStatus.String}
	if err = json.Unmarshal(source.Sources, &snapshot.Sources); err != nil || len(snapshot.Sources) > recall.MaxCopies {
		return recall.Snapshot{}, ErrRejected
	}
	if snapshot.Held || snapshot.HoldConflict || snapshot.Deleted || snapshot.TenantStatus != "active" || snapshot.MailboxStatus != "active" || snapshot.DomainStatus != "active" {
		return snapshot, nil
	}
	snapshot.Copies, err = m.recallExactCopies(ctx, snapshot.Address, snapshot.MessageID)
	return snapshot, err
}

func (m *ApplyManager) recallExactCopies(ctx context.Context, address, messageID string) ([]recall.Copy, error) {
	output := m.output
	if output == nil {
		output = runDoveadmOutput
	}
	raw, err := output(ctx, "-f", "json", "search", "-u", address, "HEADER", "Message-ID", messageID)
	if err != nil {
		return nil, err
	}
	var candidates []struct {
		GUID string          `json:"mailbox-guid"`
		UID  json.RawMessage `json:"uid"`
	}
	if err = json.Unmarshal(raw, &candidates); err != nil || len(candidates) > recall.MaxCopies {
		return nil, ErrRejected
	}
	statusRaw, err := output(ctx, "-f", "json", "mailbox", "status", "-u", address, "guid uidvalidity", "*")
	if err != nil {
		return nil, err
	}
	var statuses []struct {
		Folder   string          `json:"mailbox"`
		GUID     string          `json:"guid"`
		Validity json.RawMessage `json:"uidvalidity"`
	}
	if err = json.Unmarshal(statusRaw, &statuses); err != nil || len(statuses) > 10000 {
		return nil, ErrRejected
	}
	byGUID := map[string]recall.Copy{}
	for _, status := range statuses {
		validity, e := recallUint(status.Validity)
		if e != nil || status.Folder == "" || !mailboxGUID.MatchString(status.GUID) {
			continue
		}
		guid := strings.ToLower(status.GUID)
		if _, exists := byGUID[guid]; exists {
			return nil, ErrRejected
		}
		byGUID[guid] = recall.Copy{Folder: status.Folder, GUID: guid, UIDValidity: validity}
	}
	result := make([]recall.Copy, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		uid, e := recallUint(candidate.UID)
		localCopy, exists := byGUID[strings.ToLower(candidate.GUID)]
		if e != nil || !exists || !mailboxGUID.MatchString(candidate.GUID) {
			return nil, ErrRejected
		}
		localCopy.UID = uid
		header, e := output(ctx, "-f", "json", "fetch", "-u", address, "guid hdr.message-id", "mailbox-guid", localCopy.GUID, "uid", strconv.FormatUint(uint64(uid), 10))
		if e != nil {
			return nil, e
		}
		var records []map[string]json.RawMessage
		if json.Unmarshal(header, &records) != nil || len(records) > 1 {
			return nil, ErrRejected
		}
		if len(records) == 0 {
			continue
		}
		var messageGUID string
		if json.Unmarshal(records[0]["guid"], &messageGUID) != nil || !recall.MessageGUIDPattern.MatchString(messageGUID) {
			return nil, ErrRejected
		}
		localCopy.MessageGUID = messageGUID
		var exact string
		if json.Unmarshal(records[0]["hdr.message-id"], &exact) != nil {
			return nil, ErrRejected
		}
		if strings.TrimSpace(exact) != messageID {
			continue
		}
		if seen[localCopy.Key()] {
			return nil, ErrRejected
		}
		seen[localCopy.Key()] = true
		result = append(result, localCopy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key() < result[j].Key() })
	return result, nil
}
func recallUint(raw json.RawMessage) (uint32, error) {
	value := strings.Trim(string(raw), "\"")
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("invalid recall IMAP identifier")
	}
	return uint32(parsed), nil
}
