package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Typed read-only telemetry operations accept no caller-controlled paths.
const (
	OpMailLogRead     = "mail.log.read"
	OpPostfixQueue    = "postfix.queue.read"
	maxTelemetryBytes = 256 << 10
)

// MailLogReadParams selects a bounded cursor, never a caller-controlled path.
type MailLogReadParams struct {
	Identity string `json:"identity"`
	Offset   int64  `json:"offset"`
}

// MailLogBatch is a complete-line tail read from one stable source generation.
type MailLogBatch struct {
	Identity      string    `json:"identity"`
	StartOffset   int64     `json:"start_offset"`
	EndOffset     int64     `json:"end_offset"`
	Data          string    `json:"data"`
	Reset         bool      `json:"reset"`
	GapReason     string    `json:"gap_reason,omitempty"`
	MoreAvailable bool      `json:"more_available"`
	ObservedAt    time.Time `json:"observed_at"`
}

func decodeTelemetryParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid telemetry parameters", ErrRejected)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing telemetry parameters", ErrRejected)
	}
	return nil
}

// ReadMailLog exposes only the primary mail log and its first renamed
// rotation. Missing generations produce a gap instead of a healthy empty log.
func ReadMailLog(raw json.RawMessage) (string, string, error) {
	var params MailLogReadParams
	if err := decodeTelemetryParams(raw, &params); err != nil {
		return "", "", err
	}
	if params.Offset < 0 || len(params.Identity) > 160 {
		return "", "", fmt.Errorf("%w: invalid log cursor", ErrRejected)
	}
	batch, err := readMailLogFiles("/var/log/mail.log", "/var/log/mail.log.1", params)
	if err != nil {
		return "", "", err
	}
	encoded, err := json.Marshal(batch)
	return string(encoded), "", err
}

func logFileIdentity(file *os.File) (string, int64, error) {
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errors.New("mail log is not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, errors.New("mail log identity unavailable")
	}
	// The first complete line remains stable on append, and differentiates an
	// inode reused after rotation. Empty logs are not checkpointed by readers.
	head := make([]byte, 1024)
	n, readErr := file.ReadAt(head, 0)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", 0, readErr
	}
	head = head[:n]
	if len(head) == 0 {
		return "", info.Size(), errors.New("mail log has no complete first line yet")
	}
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	} else if n < 1024 {
		return "", info.Size(), errors.New("mail log has no complete first line yet")
	}
	digest := sha256.Sum256(head)
	return fmt.Sprintf("%d:%d:%s", stat.Dev, stat.Ino, hex.EncodeToString(digest[:16])), info.Size(), nil
}

func readMailLogFiles(current, rotated string, p MailLogReadParams) (MailLogBatch, error) {
	batch := MailLogBatch{ObservedAt: time.Now().UTC()}
	// The production caller supplies only these two fixed paths. O_NOFOLLOW
	// rejects replaced log symlinks even if a log directory is misconfigured.
	file, err := os.OpenFile(current, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- private helper; production supplies a fixed log path only; O_NOFOLLOW rejects symlinks.
	if err != nil {
		return batch, fmt.Errorf("read fixed mail log: %w", err)
	}
	defer func() { _ = file.Close() }()
	identity, size, err := logFileIdentity(file)
	if err != nil {
		return batch, err
	}
	start := p.Offset
	readingRotation := false
	if p.Identity != "" && p.Identity != identity {
		old, openErr := os.OpenFile(rotated, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- private helper; production supplies the fixed first rotation only; O_NOFOLLOW rejects symlinks.
		if openErr == nil {
			oldIdentity, oldSize, identityErr := logFileIdentity(old)
			if identityErr == nil && oldIdentity == p.Identity && p.Offset < oldSize {
				_ = file.Close()
				file = old
				readingRotation = true
				identity = oldIdentity
				size = oldSize
				defer func() { _ = old.Close() }()
			} else {
				_ = old.Close()
				start = 0
				batch.Reset = true
				if identityErr != nil || oldIdentity != p.Identity {
					batch.GapReason = "rotation_generation_missing"
				}
			}
		} else {
			start = 0
			batch.Reset = true
			batch.GapReason = "rotation_generation_missing"
		}
	} else if p.Identity == "" {
		start = 0
		batch.Reset = true
	}
	if start > size {
		start = 0
		batch.Reset = true
		batch.GapReason = "source_truncated"
	}
	if _, err = file.Seek(start, io.SeekStart); err != nil {
		return batch, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxTelemetryBytes+1))
	if err != nil {
		return batch, err
	}
	if len(raw) > maxTelemetryBytes {
		raw = raw[:maxTelemetryBytes]
	}
	complete := bytes.LastIndexByte(raw, '\n') + 1
	if complete == 0 && len(raw) == maxTelemetryBytes {
		return batch, errors.New("mail log line exceeds bounded batch; collection gap requires operator review")
	}
	if readingRotation && complete == 0 && len(raw) != 0 {
		return batch, errors.New("rotated mail log has an incomplete final line; operator review required")
	}
	batch.Identity = identity
	batch.StartOffset = start
	batch.EndOffset = start + int64(complete)
	batch.Data = string(raw[:complete])
	batch.MoreAvailable = batch.EndOffset < size
	return batch, nil
}

// QueueEntry contains only queue envelope metadata, never message bodies.
type QueueEntry struct {
	QueueID     string           `json:"queue_id"`
	QueueName   string           `json:"queue_name"`
	ArrivalTime int64            `json:"arrival_time"`
	MessageSize int64            `json:"message_size"`
	Sender      string           `json:"sender"`
	Recipients  []QueueRecipient `json:"recipients"`
}

// QueueRecipient is a queued envelope recipient and its last delay reason.
type QueueRecipient struct {
	Address     string `json:"address"`
	DelayReason string `json:"delay_reason,omitempty"`
}

// QueueSnapshot distinguishes a successful bounded observation from failure.
type QueueSnapshot struct {
	Entries    []QueueEntry `json:"entries"`
	Truncated  bool         `json:"truncated"`
	ObservedAt time.Time    `json:"observed_at"`
}

type boundedTelemetryOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedTelemetryOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxTelemetryBytes {
		b.overflow = true
		return 0, errors.New("telemetry output exceeds limit")
	}
	return b.Buffer.Write(p)
}

// ReadPostfixQueue runs one fixed read-only command with no supplied argv.
func ReadPostfixQueue(raw json.RawMessage) (string, string, error) {
	var p struct{}
	if err := decodeTelemetryParams(raw, &p); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/sbin/postqueue", "-j")
	var output boundedTelemetryOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return "", "", fmt.Errorf("%w: fixed queue snapshot unavailable", ErrFailed)
	}
	if output.overflow {
		return "", "", fmt.Errorf("%w: queue output too large", ErrFailed)
	}
	snapshot, err := parseQueueSnapshot(output.Bytes())
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid queue snapshot", ErrFailed)
	}
	encoded, err := json.Marshal(snapshot)
	return string(encoded), "", err
}

func parseQueueSnapshot(raw []byte) (QueueSnapshot, error) {
	snapshot := QueueSnapshot{Entries: []QueueEntry{}, ObservedAt: time.Now().UTC()}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var entry QueueEntry
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			return snapshot, nil
		}
		if err != nil {
			return snapshot, err
		}
		if entry.QueueID == "" || len(entry.QueueID) > 128 || strings.ContainsAny(entry.QueueID, "/\\\r\n\x00") || entry.ArrivalTime < 0 || entry.MessageSize < 0 {
			return snapshot, errors.New("invalid queue envelope")
		}
		if len(snapshot.Entries) >= 1000 {
			snapshot.Truncated = true
			return snapshot, nil
		}
		snapshot.Entries = append(snapshot.Entries, entry)
	}
}
