package webmail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strconv"
	"strings"
)

// ListMessages SELECTs the folder, UIDs newest-first, then FETCHes a page.
func (c *imapConn) ListMessages(ctx context.Context, folder string, opts ListOpts) (ListResult, error) {
	opts = ClampListOpts(opts)
	if err := c.selectFolder(ctx, folder); err != nil {
		return ListResult{}, err
	}
	uids, err := c.uidSearchAll(ctx)
	if err != nil {
		return ListResult{}, err
	}
	total := len(uids)
	// Newest first.
	for i, j := 0, len(uids)-1; i < j; i, j = i+1, j-1 {
		uids[i], uids[j] = uids[j], uids[i]
	}
	if opts.Offset >= len(uids) {
		return ListResult{Messages: []MessageSummary{}, Total: total}, nil
	}
	end := opts.Offset + opts.Limit
	if end > len(uids) {
		end = len(uids)
	}
	page := uids[opts.Offset:end]
	if len(page) == 0 {
		return ListResult{Messages: []MessageSummary{}, Total: total}, nil
	}
	summaries, err := c.uidFetchSummaries(ctx, page)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Messages: summaries, Total: total}, nil
}

// GetMessage FETCHes one UID and prefers text/plain + text/html body parts.
func (c *imapConn) GetMessage(ctx context.Context, folder string, uid uint32) (*Message, error) {
	if uid == 0 {
		return nil, fmt.Errorf("invalid uid")
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return nil, err
	}
	summaries, err := c.uidFetchSummaries(ctx, []uint32{uid})
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, fmt.Errorf("message not found")
	}
	rawBody, err := c.uidFetchBody(ctx, uid)
	if err != nil {
		return nil, err
	}
	msg := &Message{MessageSummary: summaries[0], Attachments: []AttachmentMeta{}}
	if len(rawBody) > 0 {
		text, html, atts := parseMIMEBody(rawBody)
		msg.Text = text
		msg.HTML = html
		msg.Attachments = atts
	}
	return msg, nil
}

func (c *imapConn) uidFetchBody(ctx context.Context, uid uint32) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tag := c.nextTag()
	cmd := fmt.Sprintf("%s UID FETCH %d (BODY.PEEK[])\r\n", tag, uid)
	if _, err := io.WriteString(c.conn, cmd); err != nil {
		return nil, err
	}
	var body []byte
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, tag+" OK") {
			return body, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, fmt.Errorf("%s", line)
		}
		if !strings.HasPrefix(line, "* ") || !strings.Contains(line, " FETCH ") {
			continue
		}
		litSize, hasLit := trailingLiteralSize(line)
		if !hasLit {
			continue
		}
		buf := make([]byte, litSize)
		if _, err = io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		body = buf
		// Drain trailing bytes until the FETCH list closes and the next line.
		for {
			cont, err := c.r.ReadString('\n')
			if err != nil {
				return nil, err
			}
			cont = strings.TrimRight(cont, "\r\n")
			if strings.Contains(cont, ")") || cont == ")" {
				break
			}
			if strings.HasPrefix(cont, tag+" ") {
				if strings.HasPrefix(cont, tag+" OK") {
					return body, nil
				}
				return nil, fmt.Errorf("%s", cont)
			}
		}
	}
}

// SetFlags adds and/or removes IMAP flags for the given UIDs.
func (c *imapConn) SetFlags(ctx context.Context, folder string, uids []uint32, add, remove []string) error {
	if len(uids) == 0 {
		return nil
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return err
	}
	uidSet := formatUIDSet(uids)
	if len(add) > 0 {
		if err := c.command(ctx, "UID STORE", uidSet, "+FLAGS.SILENT", "("+strings.Join(add, " ")+")"); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if err := c.command(ctx, "UID STORE", uidSet, "-FLAGS.SILENT", "("+strings.Join(remove, " ")+")"); err != nil {
			return err
		}
	}
	return nil
}

// MoveMessages uses UID MOVE when available.
func (c *imapConn) MoveMessages(ctx context.Context, folder string, uids []uint32, dest string) error {
	if len(uids) == 0 || dest == "" {
		return fmt.Errorf("move requires uids and destination")
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return err
	}
	return c.command(ctx, "UID MOVE", formatUIDSet(uids), quoteIMAP(dest))
}

// DeleteMessages marks UIDs \Deleted and optionally expunges them.
func (c *imapConn) DeleteMessages(ctx context.Context, folder string, uids []uint32, expunge bool) error {
	if len(uids) == 0 {
		return nil
	}
	if err := c.SetFlags(ctx, folder, uids, []string{`\Deleted`}, nil); err != nil {
		return err
	}
	if !expunge {
		return nil
	}
	uidSet := formatUIDSet(uids)
	if err := c.command(ctx, "UID EXPUNGE", uidSet); err != nil {
		// Fallback for servers without UIDPLUS.
		return c.command(ctx, "EXPUNGE")
	}
	return nil
}

func (c *imapConn) selectFolder(ctx context.Context, folder string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if folder == "" {
		return fmt.Errorf("empty folder")
	}
	return c.command(ctx, "SELECT", quoteIMAP(folder))
}

func (c *imapConn) uidSearchAll(ctx context.Context) ([]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tag := c.nextTag()
	if _, err := io.WriteString(c.conn, tag+" UID SEARCH ALL\r\n"); err != nil {
		return nil, err
	}
	var lines []string
	for {
		line, err := c.readLogicalLine()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "* ") {
			lines = append(lines, line)
			continue
		}
		if strings.HasPrefix(line, tag+" OK") {
			return parseUIDSearch(lines), nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, fmt.Errorf("%s", line)
		}
	}
}

func (c *imapConn) uidFetchSummaries(ctx context.Context, uids []uint32) ([]MessageSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tag := c.nextTag()
	cmd := fmt.Sprintf("%s UID FETCH %s (UID FLAGS RFC822.SIZE ENVELOPE)\r\n", tag, formatUIDSet(uids))
	if _, err := io.WriteString(c.conn, cmd); err != nil {
		return nil, err
	}
	byUID := make(map[uint32]MessageSummary)
	for {
		line, err := c.readLogicalLine()
		if err != nil {
			return nil, err
		}
		if attrsPayload, ok := extractFetchPayload(line); ok {
			attrs := parseFetchAttrs(attrsPayload)
			sum, sumErr := summaryFromAttrs(attrs)
			if sumErr == nil {
				byUID[sum.UID] = sum
			}
			continue
		}
		if strings.HasPrefix(line, tag+" OK") {
			break
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, fmt.Errorf("%s", line)
		}
	}
	out := make([]MessageSummary, 0, len(uids))
	for _, uid := range uids {
		if sum, ok := byUID[uid]; ok {
			out = append(out, sum)
		}
	}
	return out, nil
}

// readLogicalLine reads one IMAP response line, inlining `{n}` literals.
func (c *imapConn) readLogicalLine() (string, error) {
	var b strings.Builder
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		litSize, hasLit := trailingLiteralSize(line)
		if !hasLit {
			b.WriteString(line)
			return b.String(), nil
		}
		// Drop the `{n}` suffix and append the literal as a quoted string so
		// ENVELOPE/attr parsers keep token boundaries.
		cut := strings.LastIndex(line, "{")
		b.WriteString(line[:cut])
		buf := make([]byte, litSize)
		if _, err = io.ReadFull(c.r, buf); err != nil {
			return "", err
		}
		b.WriteString(quoteIMAP(string(buf)))
		// Consume the CRLF that follows the literal, then continue if the
		// FETCH list is still open (more tokens on subsequent physical lines).
		if peek, err := c.r.Peek(1); err == nil && (peek[0] == '\r' || peek[0] == '\n') {
			_, _ = c.r.ReadByte()
			if peek[0] == '\r' {
				if peek2, err2 := c.r.Peek(1); err2 == nil && peek2[0] == '\n' {
					_, _ = c.r.ReadByte()
				}
			}
		}
		// After a literal mid-FETCH, the server continues the same response on
		// the next physical line (often just `)`). Keep reading into b.
		cont, err := c.r.ReadString('\n')
		if err != nil {
			return "", err
		}
		cont = strings.TrimRight(cont, "\r\n")
		if litSize2, hasLit2 := trailingLiteralSize(cont); hasLit2 {
			cut2 := strings.LastIndex(cont, "{")
			b.WriteString(cont[:cut2])
			buf2 := make([]byte, litSize2)
			if _, err = io.ReadFull(c.r, buf2); err != nil {
				return "", err
			}
			b.WriteString(quoteIMAP(string(buf2)))
			if peek, err := c.r.Peek(1); err == nil && (peek[0] == '\r' || peek[0] == '\n') {
				_, _ = c.r.ReadByte()
				if peek[0] == '\r' {
					if peek2, err2 := c.r.Peek(1); err2 == nil && peek2[0] == '\n' {
						_, _ = c.r.ReadByte()
					}
				}
			}
			continue
		}
		b.WriteString(cont)
		return b.String(), nil
	}
}

func trailingLiteralSize(line string) (int, bool) {
	i := strings.LastIndex(line, "{")
	if i < 0 || !strings.HasSuffix(line, "}") {
		return 0, false
	}
	n, err := strconv.Atoi(line[i+1 : len(line)-1])
	if err != nil || n < 0 || n > 64<<20 {
		return 0, false
	}
	return n, true
}

func parseMIMEBody(raw []byte) (text, html string, atts []AttachmentMeta) {
	atts = []AttachmentMeta{}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		// Treat as plain body if headers are missing.
		return string(raw), "", atts
	}
	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	media, params, err := mime.ParseMediaType(ct)
	if err != nil {
		body, _ := io.ReadAll(io.LimitReader(msg.Body, 8<<20))
		return string(body), "", atts
	}
	if strings.HasPrefix(media, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		partID := 0
		for {
			part, partErr := mr.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				break
			}
			partID++
			body, _ := io.ReadAll(io.LimitReader(part, 8<<20))
			pct := part.Header.Get("Content-Type")
			if pct == "" {
				pct = "application/octet-stream"
			}
			pmedia, pparams, _ := mime.ParseMediaType(pct)
			filename := part.FileName()
			if filename == "" {
				filename = pparams["name"]
			}
			disp := part.Header.Get("Content-Disposition")
			isAttach := strings.HasPrefix(strings.ToLower(disp), "attachment") || filename != ""
			switch {
			case pmedia == "text/plain" && text == "" && !isAttach:
				text = string(body)
			case pmedia == "text/html" && html == "" && !isAttach:
				html = string(body)
			case isAttach || (!strings.HasPrefix(pmedia, "text/") && pmedia != "multipart/alternative" && pmedia != "multipart/related"):
				atts = append(atts, AttachmentMeta{
					PartID:      strconv.Itoa(partID),
					Filename:    filename,
					ContentType: pmedia,
					Size:        int64(len(body)),
				})
			}
		}
		return text, html, atts
	}
	body, _ := io.ReadAll(io.LimitReader(msg.Body, 8<<20))
	switch media {
	case "text/html":
		return "", string(body), atts
	default:
		return string(body), "", atts
	}
}
