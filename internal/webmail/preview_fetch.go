package webmail

// Batched preview fetch (v0.6.10 L3).
//
// One IMAP command per page downloads the snippet bytes of every message:
//
//	TAG UID FETCH 1,2,3 (BODY.PEEK[]<0.2048>)
//
// The partial fetch returns at most the requested byte count per message in
// a single round trip, and BODY.PEEK keeps the \Seen flag untouched. The
// per-message budget is derived from the page size so a full page never
// downloads more than previewPageBytes. A failed or truncated snippet simply
// stays empty; a listing never fails because of a snippet.

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// attachPreviews fills the preview snippet of every summary with one batched
// IMAP fetch. A failed snippet fetch is never fatal: the listing still
// returns and the previews stay empty.
func (c *imapConn) attachPreviews(ctx context.Context, summaries []MessageSummary) {
	if len(summaries) == 0 {
		return
	}
	uids := make([]uint32, 0, len(summaries))
	for _, summary := range summaries {
		if summary.UID != 0 {
			uids = append(uids, summary.UID)
		}
	}
	previews, err := c.uidFetchPreviews(ctx, uids)
	if err != nil {
		return
	}
	for i := range summaries {
		if preview, ok := previews[summaries[i].UID]; ok {
			summaries[i].Preview = preview
		}
	}
}

// uidFetchPreviews runs the single batched snippet command for the given
// UIDs and maps every UID to its extracted preview text.
func (c *imapConn) uidFetchPreviews(ctx context.Context, uids []uint32) (map[uint32]string, error) {
	if len(uids) == 0 {
		return map[uint32]string{}, nil
	}
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	perMessage := previewFetchBytes
	if budget := previewPageBytes / len(uids); budget < perMessage {
		perMessage = max(budget, previewMinFetchBytes)
	}
	tag := c.nextTag()
	command := fmt.Sprintf("%s UID FETCH %s (BODY.PEEK[]<0.%d>)\r\n", tag, formatUIDSet(uids), perMessage)
	if _, err := io.WriteString(c.conn, command); err != nil {
		return nil, err
	}
	previews := make(map[uint32]string, len(uids))
	pending := ""
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, tag+" OK") {
			return previews, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, fmt.Errorf("preview fetch: %s", strings.TrimSpace(line))
		}
		if !strings.HasPrefix(line, "* ") {
			continue
		}
		if idx := strings.Index(line, " FETCH "); idx >= 0 {
			pending = line[idx+len(" FETCH "):]
		} else if pending != "" {
			pending += " " + line
		} else {
			continue
		}
		literalSize, hasLiteral := trailingLiteralSize(pending)
		if !hasLiteral {
			continue // e.g. a response without a body literal
		}
		raw, err := c.readBoundedLiteral(literalSize, perMessage)
		if err != nil {
			return nil, err
		}
		if tailErr := c.drainFetchTail(); tailErr != nil {
			return nil, tailErr
		}
		if uid, ok := parseFetchUID(pending); ok {
			previews[uid] = previewFromRaw(raw)
		}
		pending = ""
	}
}

// readBoundedLiteral reads one literal of the declared size while keeping at
// most keep bytes; the remainder is discarded so a server that ignores the
// partial fetch cannot inflate memory.
func (c *imapConn) readBoundedLiteral(size, keep int) ([]byte, error) {
	if size <= 0 {
		return nil, nil
	}
	if keep > size {
		keep = size
	}
	buf := make([]byte, keep)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return nil, err
	}
	if rest := int64(size - keep); rest > 0 {
		if _, err := io.CopyN(io.Discard, c.r, rest); err != nil {
			return nil, err
		}
	}
	return buf, nil
}

// drainFetchTail consumes the tail of a FETCH response after a literal body
// (usually just the closing paren of the attribute list).
func (c *imapConn) drainFetchTail() error {
	for attempt := 0; attempt < 8; attempt++ {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.Contains(line, ")") {
			return nil
		}
	}
	return nil
}

// parseFetchUID extracts the UID attribute of a FETCH response line.
func parseFetchUID(line string) (uint32, bool) {
	match := fetchUIDPattern.FindStringSubmatch(line)
	if match == nil {
		return 0, false
	}
	uid, err := strconv.ParseUint(match[1], 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(uid), true
}
