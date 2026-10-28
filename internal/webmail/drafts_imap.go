package webmail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var (
	// ErrDraftChanged prevents replacing or submitting a different mailbox generation.
	ErrDraftChanged = errors.New("draft mailbox identity changed")
	// ErrDraftUncertain preserves the old draft when APPEND identity is unconfirmed.
	ErrDraftUncertain = errors.New("draft append outcome uncertain")
)

// EnsureDraftsFolder resolves or creates the drafts mailbox.
func (c *imapConn) EnsureDraftsFolder(ctx context.Context) (string, error) {
	folders, err := c.ListFolders(ctx)
	if err != nil {
		return "", err
	}
	if name := ResolveDraftsFolder(folders); name != "" {
		return name, nil
	}
	if err = c.createMailbox(ctx, DraftsFolderName); err != nil {
		return "", err
	}
	folders, err = c.ListFolders(ctx)
	if err != nil {
		return DraftsFolderName, nil
	}
	if name := ResolveDraftsFolder(folders); name != "" {
		return name, nil
	}
	return DraftsFolderName, nil
}

func (c *imapConn) createMailbox(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := c.command(ctx, "CREATE", quoteIMAP(encodeMailboxWire(name)))
	if err == nil {
		return nil
	}
	// Already exists / race: treat as success so Ensure stays idempotent.
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "already") || strings.Contains(low, "exists") {
		return nil
	}
	return err
}

// SaveDraft APPENDs a draft MIME message, optionally replacing a prior UID.
func (c *imapConn) SaveDraft(ctx context.Context, from string, msg Outgoing, replaceUID, replaceUIDValidity uint32) (DraftSaveResult, error) {
	folder, err := c.EnsureDraftsFolder(ctx)
	if err != nil {
		return DraftSaveResult{}, err
	}
	if replaceUID != 0 {
		actual, validityErr := c.FolderUIDValidity(ctx, folder)
		if validityErr != nil {
			return DraftSaveResult{}, validityErr
		}
		if replaceUIDValidity == 0 || actual != replaceUIDValidity {
			return DraftSaveResult{}, ErrDraftChanged
		}
	}
	raw := BuildDraftMIME(from, msg)
	validity, uid, err := c.appendLiteral(ctx, folder, []string{`\Draft`}, []byte(raw))
	if err != nil {
		return DraftSaveResult{}, err
	}
	result := DraftSaveResult{Folder: folder, NameEnc: EncodeFolderName(folder), UID: uid, UIDValidity: validity}
	if replaceUID != 0 {
		if validity != replaceUIDValidity {
			return result, ErrDraftChanged
		}
		if err := c.DeleteMessages(ctx, folder, []uint32{replaceUID}, true); err != nil {
			result.CleanupPending = true
		}
	}
	return result, nil
}

// appendLiteral runs APPEND mailbox (flags) {n} + literal, returning APPENDUID when present.
func (c *imapConn) appendLiteral(ctx context.Context, folder string, flags []string, raw []byte) (uint32, uint32, error) {
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if folder == "" {
		return 0, 0, fmt.Errorf("empty folder")
	}
	if len(raw) == 0 {
		return 0, 0, fmt.Errorf("empty message")
	}
	if len(raw) > 32<<20 {
		return 0, 0, fmt.Errorf("message too large")
	}
	tag := c.nextTag()
	var b strings.Builder
	b.WriteString(tag)
	b.WriteString(" APPEND ")
	b.WriteString(quoteIMAP(encodeMailboxWire(folder)))
	if len(flags) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(flags, " "))
		b.WriteByte(')')
	}
	_, _ = fmt.Fprintf(&b, " {%d}\r\n", len(raw))
	if _, err := io.WriteString(c.conn, b.String()); err != nil {
		return 0, 0, err
	}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return 0, 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "+") {
			break
		}
		if strings.HasPrefix(line, tag+" ") {
			return 0, 0, fmt.Errorf("%s", line)
		}
	}
	// Literal size was advertised as len(raw); send exactly those octets (RFC 3501).
	if _, err := c.conn.Write(raw); err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrDraftUncertain, err)
	}
	if _, err := io.WriteString(c.conn, "\r\n"); err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrDraftUncertain, err)
	}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return 0, 0, fmt.Errorf("%w: %w", ErrDraftUncertain, err)
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "* ") {
			continue
		}
		if strings.HasPrefix(line, tag+" OK") {
			validity, uid := parseAPPENDUID(line)
			if validity == 0 || uid == 0 {
				return 0, 0, ErrDraftUncertain
			}
			return validity, uid, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return 0, 0, fmt.Errorf("%s", line)
		}
	}
}

// parseAPPENDUID extracts the UID from: A003 OK [APPENDUID <uidvalidity> <uid>] ...
func parseAPPENDUID(line string) (uint32, uint32) {
	i := strings.Index(strings.ToUpper(line), "[APPENDUID ")
	if i < 0 {
		return 0, 0
	}
	rest := line[i+len("[APPENDUID "):]
	end := strings.IndexByte(rest, ']')
	if end >= 0 {
		rest = rest[:end]
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return 0, 0
	}
	validity, validityErr := strconv.ParseUint(fields[0], 10, 32)
	n, err := strconv.ParseUint(fields[len(fields)-1], 10, 32)
	if err != nil || validityErr != nil || validity == 0 || n == 0 {
		return 0, 0
	}
	return uint32(validity), uint32(n)
}
