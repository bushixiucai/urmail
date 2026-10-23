package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

// OpMailboxExpunge performs bounded local cleanup for a persisted recall request.
const OpMailboxExpunge = "mailbox.expunge"

// RecallParams identifies an existing request; message and mailbox identities
// are resolved again by the privileged process from the authoritative database.
type RecallParams struct {
	TenantID int64 `json:"tenant_id"`
	RecallID int64 `json:"recall_id"`
}

// ExpungeRecall serializes with legal-hold changes and records the durable
// local result and its audit event in the same PostgreSQL transaction.
func (m *ApplyManager) ExpungeRecall(raw json.RawMessage) (string, string, error) {
	var params RecallParams
	if err := strictParams(raw, &params); err != nil {
		return "", "", err
	}
	if params.TenantID <= 0 || params.RecallID <= 0 {
		return "", "", ErrRejected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	target, err := q.PlatformLockRecallForExpunge(ctx, store.PlatformLockRecallForExpungeParams{TenantID: params.TenantID, ID: params.RecallID})
	if err != nil {
		return "", "", ErrRejected
	}
	if target.Status != "pending" {
		return target.Status, "", nil
	}
	status, reason := "failed", "local_target_unavailable"
	switch {
	case !target.MailboxID.Valid:
		reason = "remote_delivery_not_recallable"
	case target.LegalHold:
		reason = "legal_hold"
	case target.MailboxID.Valid && target.ArchiveMailboxID.Valid && target.MailboxID.Int64 == target.ArchiveMailboxID.Int64 && target.Address.Valid && safeKickAddress(target.Address.String) && !target.ArchiveDeletedAt.Valid && target.ArchiveMessageID == target.MessageID && target.TenantStatus == "active" && target.MailboxStatus.String == "active" && target.DomainStatus.String == "active":
		found, expungeErr := m.expungeExact(ctx, target.Address.String, target.MessageID)
		switch {
		case expungeErr != nil:
			reason = "local_cleanup_failed"
		case !found:
			reason = "local_message_not_found"
		default:
			status = "local_recalled"
			reason = "local_mailbox_only"
		}
	}
	if _, err = q.PlatformCompleteRecall(ctx, store.PlatformCompleteRecallParams{TenantID: params.TenantID, ID: params.RecallID, Status: status, Reason: pgtype.Text{String: reason, Valid: true}}); err != nil {
		return "", "", ErrFailed
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", ErrFailed
	}
	return status, "", nil
}

var mailboxGUID = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)

func (m *ApplyManager) expungeExact(ctx context.Context, address, messageID string) (bool, error) {
	if !safeKickAddress(address) || len(messageID) < 3 || len(messageID) > 998 || strings.ContainsAny(messageID, "\r\n\x00") || !strings.HasPrefix(messageID, "<") || !strings.HasSuffix(messageID, ">") {
		return false, ErrRejected
	}
	output := m.output
	if output == nil {
		output = runDoveadmOutput
	}
	// HEADER is a substring search. It only discovers candidates; each full
	// header must equal the archived identity before a GUID/UID can be deleted.
	raw, err := output(ctx, "-f", "json", "search", "-u", address, "HEADER", "Message-ID", messageID)
	if err != nil {
		return false, err
	}
	var candidates []struct {
		MailboxGUID string      `json:"mailbox-guid"`
		UID         json.Number `json:"uid"`
	}
	if err = json.Unmarshal(raw, &candidates); err != nil {
		return false, err
	}
	if len(candidates) > 100 {
		return false, fmt.Errorf("too many recall candidates")
	}
	found := false
	for _, candidate := range candidates {
		uid, parseErr := strconv.ParseUint(candidate.UID.String(), 10, 32)
		if !mailboxGUID.MatchString(candidate.MailboxGUID) || parseErr != nil || uid == 0 {
			return false, ErrRejected
		}
		args := []string{"-f", "json", "fetch", "-u", address, "hdr.message-id", "mailbox-guid", candidate.MailboxGUID, "uid", strconv.FormatUint(uid, 10)}
		header, fetchErr := output(ctx, args...)
		if fetchErr != nil {
			return false, fetchErr
		}
		var records []map[string]json.RawMessage
		if err = json.Unmarshal(header, &records); err != nil {
			return false, err
		}
		if len(records) != 1 {
			continue
		}
		var exact string
		if err = json.Unmarshal(records[0]["hdr.message-id"], &exact); err != nil {
			return false, err
		}
		if strings.TrimSpace(exact) != messageID {
			continue
		}
		if err = m.run(ctx, "/usr/bin/doveadm", "expunge", "-u", address, "mailbox-guid", candidate.MailboxGUID, "uid", strconv.FormatUint(uid, 10)); err != nil {
			return false, err
		}
		found = true
	}
	return found, nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}
func runDoveadmOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/doveadm", args...) // #nosec G204 -- fixed doveadm subcommands with checked database identities and GUID/UID selectors.
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "HOME=/root"}
	var output boundedOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("doveadm query failed: %w", err)
	}
	return output.Bytes(), nil
}
