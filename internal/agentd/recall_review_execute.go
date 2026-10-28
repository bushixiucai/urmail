package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	recall "github.com/bushixiucai/urmail/internal/domain/recall"
	"github.com/bushixiucai/urmail/internal/store"
)

func (m *ApplyManager) executeReviewedRecall(ctx context.Context, q *store.Queries, target store.PlatformLockRecallForExpungeRow) (string, string, []byte, error) {
	result := recall.Result{Items: []recall.ItemResult{}}
	finish := func(state, reason string) (string, string, []byte, error) {
		raw, err := json.Marshal(result)
		return state, reason, raw, err
	}
	if !target.ConfirmedAt.Valid || !target.PreviewExpiresAt.Valid || !target.PreviewExpiresAt.Time.After(time.Now()) {
		return finish("expired", "reviewed_preview_expired")
	}
	var reviewed recall.Snapshot
	var keys []string
	if json.Unmarshal(target.PreviewSnapshot, &reviewed) != nil || json.Unmarshal(target.SelectedCopyKeys, &keys) != nil {
		return finish("failed", "reviewed_preview_invalid")
	}
	digest, err := reviewed.Digest()
	if err != nil || digest != target.PreviewDigest.String || (!target.PreviewActorID.Valid || reviewed.ActorID != target.PreviewActorID.Int64) || reviewed.TenantID != target.TenantID || reviewed.EntryID != target.ArchiveEntryID.Int64 || reviewed.MailboxID != target.MailboxID.Int64 || reviewed.MessageID != target.MessageID {
		return finish("failed", "reviewed_preview_invalid")
	}
	selected, err := reviewed.Select(keys)
	if err != nil {
		return finish("failed", "reviewed_copy_selection_invalid")
	}
	if _, err = q.PlatformLockRecallAuthority(ctx, store.PlatformLockRecallAuthorityParams{TenantID: target.TenantID, UserID: target.PreviewActorID.Int64}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return finish("failed", "recall_authority_revoked")
		}
		return "", "", nil, ErrFailed
	}
	if _, err = q.PlatformLockRecallMailbox(ctx, store.PlatformLockRecallMailboxParams{TenantID: target.TenantID, ID: target.MailboxID.Int64}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return finish("failed", "local_target_unavailable")
		}
		return "", "", nil, ErrFailed
	}
	current, err := m.recallSnapshot(ctx, q, RecallPreviewParams{TenantID: target.TenantID, EntryID: target.ArchiveEntryID.Int64, ActorID: target.PreviewActorID.Int64})
	if err != nil {
		return finish("failed", "local_preview_unavailable")
	}
	if reason := current.Refusal(); reason != "" && reason != "local_message_not_found" {
		return finish("failed", reason)
	}
	nowDigest, err := current.Digest()
	if err != nil {
		return finish("failed", "local_preview_unavailable")
	}
	if nowDigest != digest {
		present := map[string]bool{}
		for _, localCopy := range current.Copies {
			present[localCopy.Key()] = true
		}
		unknown := false
		for _, localCopy := range selected {
			status := "failed"
			reason := "reviewed_copy_set_changed"
			if !present[localCopy.Key()] {
				status = "unknown"
				unknown = true
				reason = "reviewed_copy_no_longer_observed"
			}
			result.Items = append(result.Items, recall.ItemResult{Key: localCopy.Key(), Status: status, Reason: reason})
		}
		if unknown {
			return finish("uncertain", "reviewed_copy_set_changed")
		}
		return finish("failed", "reviewed_copy_set_changed")
	}
	confirmed, failed, unknown := 0, 0, 0
	for _, localCopy := range selected {
		item := recall.ItemResult{Key: localCopy.Key(), Status: "unknown", Reason: "local_outcome_unknown"}
		exists, verifyErr := m.recallCopyPresent(ctx, reviewed.Address, reviewed.MessageID, localCopy)
		switch {
		case verifyErr != nil:
			item.Reason = "reviewed_generation_or_copy_changed"
		case !exists:
			item.Status = "confirmed_absent"
			item.Reason = "local_copy_already_absent"
		default:
			commandErr := m.run(ctx, "/usr/bin/doveadm", "expunge", "-u", reviewed.Address, "mailbox-guid", localCopy.GUID, "uid", strconv.FormatUint(uint64(localCopy.UID), 10), "guid", localCopy.MessageGUID)
			remains, checkErr := m.recallCopyPresent(ctx, reviewed.Address, reviewed.MessageID, localCopy)
			switch {
			case checkErr == nil && !remains:
				item.Status = "confirmed_absent"
				item.Reason = "local_copy_absent"
			case checkErr == nil && remains:
				item.Status = "failed"
				item.Reason = "local_cleanup_failed"
			case commandErr != nil:
				item.Reason = "local_cleanup_acknowledgement_unknown"
			}
		}
		switch item.Status {
		case "confirmed_absent":
			confirmed++
		case "failed":
			failed++
		default:
			unknown++
		}
		result.Items = append(result.Items, item)
	}
	switch {
	case unknown > 0:
		return finish("uncertain", "local_outcome_unknown")
	case failed > 0 && confirmed > 0:
		return finish("failed", "local_partial_result")
	case failed > 0:
		return finish("failed", "local_cleanup_failed")
	default:
		return finish("local_recalled", "local_snapshot_absent")
	}
}

// recallCopyPresent rechecks the namespace generation and exact header before
// and after each fixed GUID/UID action. It never repeats a Message-ID wildcard expunge.
func (m *ApplyManager) recallCopyPresent(ctx context.Context, address, messageID string, localCopy recall.Copy) (bool, error) {
	if !safeKickAddress(address) || !mailboxGUID.MatchString(localCopy.GUID) || localCopy.UID == 0 || localCopy.UIDValidity == 0 || !recall.MessageGUIDPattern.MatchString(localCopy.MessageGUID) {
		return false, ErrRejected
	}
	output := m.output
	if output == nil {
		output = runDoveadmOutput
	}
	raw, err := output(ctx, "-f", "json", "mailbox", "status", "-u", address, "--", "guid uidvalidity", localCopy.Folder)
	if err != nil {
		return false, err
	}
	var namespaces []struct {
		GUID     string          `json:"guid"`
		Validity json.RawMessage `json:"uidvalidity"`
	}
	if json.Unmarshal(raw, &namespaces) != nil || len(namespaces) > 10000 {
		return false, ErrRejected
	}
	valid := false
	for _, namespace := range namespaces {
		if strings.EqualFold(namespace.GUID, localCopy.GUID) {
			v, e := recallUint(namespace.Validity)
			if e != nil || v != localCopy.UIDValidity || valid {
				return false, ErrRejected
			}
			valid = true
		}
	}
	if !valid {
		return false, ErrRejected
	}
	raw, err = output(ctx, "-f", "json", "fetch", "-u", address, "guid hdr.message-id", "mailbox-guid", localCopy.GUID, "uid", strconv.FormatUint(uint64(localCopy.UID), 10))
	if err != nil {
		return false, err
	}
	var records []map[string]json.RawMessage
	if json.Unmarshal(raw, &records) != nil || len(records) > 1 {
		return false, ErrRejected
	}
	if len(records) == 0 {
		return false, nil
	}
	var messageGUID string
	if json.Unmarshal(records[0]["guid"], &messageGUID) != nil || messageGUID != localCopy.MessageGUID {
		return false, ErrRejected
	}
	var header string
	if json.Unmarshal(records[0]["hdr.message-id"], &header) != nil || strings.TrimSpace(header) != messageID {
		return false, ErrRejected
	}
	return true, nil
}
