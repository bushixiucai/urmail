package webmail

// Batched preview fetch (v0.6.10 L3, split fetch per v0.6.10-hotfix-2).
//
// One IMAP command per page downloads the snippet bytes of every message:
//
//	TAG UID FETCH 1,2,3 (BODY.PEEK[HEADER.FIELDS (CONTENT-TYPE
//	     CONTENT-TRANSFER-ENCODING)] BODY.PEEK[TEXT]<0.2048>)
//
// Production messages routinely carry multi-kilobyte header chains (DKIM
// signatures, mailing-list stamps), so a head-only BODY.PEEK[]<0.N> partial
// fetch can spend its entire byte budget on headers and return zero body
// bytes — every preview in the listing stayed empty (DEF-1). The split
// fetch instead downloads only the two headers the snippet parser needs
// plus the first TEXT bytes, so the budget is spent on body text no matter
// how large the headers are. The partial fetch returns at most the
// requested byte count per message in a single round trip, and BODY.PEEK
// keeps the \Seen flag untouched. A failed or truncated snippet simply
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
// UIDs and maps every UID to its extracted preview text. Each FETCH
// response carries two literals: the requested header fields and the start
// of the body text.
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
	command := fmt.Sprintf("%s UID FETCH %s (BODY.PEEK[HEADER.FIELDS (CONTENT-TYPE CONTENT-TRANSFER-ENCODING)] BODY.PEEK[TEXT]<0.%d>)\r\n",
		tag, formatUIDSet(uids), perMessage)
	if _, err := io.WriteString(c.conn, command); err != nil {
		return nil, err
	}
	previews := make(map[uint32]string, len(uids))
	var (
		uid      uint32
		haveUID  bool
		header   []byte
		wantText bool // false: awaiting the header-fields literal, true: the text literal
	)
	// complete turns the two literals of one message into its preview and
	// resets the accumulator for the next FETCH response.
	complete := func(text []byte) {
		if haveUID {
			previews[uid] = previewFromRaw(previewHead(header, text))
		}
		uid, header = 0, nil
		haveUID, wantText = false, false
	}
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
		if !wantText {
			if !strings.HasPrefix(line, "* ") {
				continue
			}
			if idx := strings.Index(line, " FETCH "); idx >= 0 {
				complete(nil) // the previous message ended; guard against interleaving
				pending = line[idx+len(" FETCH "):]
			} else {
				continue
			}
		} else {
			// continuation line after the header-fields literal
			pending = line
		}
		if id, ok := parseFetchUID(pending); ok {
			uid, haveUID = id, true
		}
		literalSize, hasLiteral := trailingLiteralSize(pending)
		if !hasLiteral {
			if wantText && strings.Contains(line, ")") {
				complete(nil) // no text literal (empty body or NIL)
				// the closing paren has already been consumed
			}
			continue
		}
		literal, err := c.readBoundedLiteral(literalSize, perMessage)
		if err != nil {
			return nil, err
		}
		if !wantText {
			header = literal
			wantText = true // the TEXT literal continues on the next wire line
			continue
		}
		complete(literal)
		// The remaining tail of the response (usually just the closing
		// paren) must be consumed before the next FETCH line.
		if err := c.drainFetchTail(); err != nil {
			return nil, err
		}
	}
}

// previewHead joins the fetched header-fields literal and the text literal
// into the head-of-message shape previewFromRaw expects.
func previewHead(headerFields, text []byte) []byte {
	raw := make([]byte, 0, len(headerFields)+4+len(text))
	raw = append(raw, headerFields...)
	raw = append(raw, '\r', '\n', '\r', '\n')
	raw = append(raw, text...)
	return raw
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
