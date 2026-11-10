package webmail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/mail"
	"strconv"
	"strings"

	"github.com/bushixiucai/urmail/internal/mailauth"
)

// ListMessages SELECTs the folder, computes the ordered UID sequence, then
// FETCHes one cursor-anchored page.
//
// Ordering: SortBy "" keeps the historical arrival order (UID descending,
// newest first). A non-empty SortBy uses UID SORT when the server advertises
// SORT; otherwise the fallback fetches up to MaxSortScan summaries and sorts
// them locally (SortSummaries) — ServerSorted=false tells the client which
// path ran. When opts.Query is set, UID SEARCH criteria from
// BuildSearchCriteria(q) replace ALL; see BuildSearchCriteria for semantics.
//
// Pagination: see the ListOpts contract — CursorUID anchors the page inside
// the ordered sequence, so new arrivals cannot duplicate or skip existing
// messages across pages.
//
// Threading: opts.Thread stamps thread_id/thread_count. With
// THREAD=REFERENCES advertised the server computes the grouping and the
// thread sizes (ThreadMode "server", ThreadScope "server" — the counts come
// from the THREAD response and cover every member of the thread, so they are
// folder-wide and identical on every page). Otherwise the client path
// groups over a bounded summary scan of the filtered set (MaxSortScan
// summaries, ThreadMode "client"): the counts cover the whole filtered set
// (ThreadScope "folder") when it fits the bound and only the scanned prefix
// (ThreadScope "bounded") when it does not.
func (c *imapConn) ListMessages(ctx context.Context, folder string, opts ListOpts) (ListResult, error) {
	opts = ClampListOpts(opts)
	criteria, hasAttachment, err := buildSearchPlan(opts.Query)
	if err != nil {
		return ListResult{}, err
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return ListResult{}, err
	}

	var ordered []uint32
	serverSorted := false
	// allSummaries caches summaries fetched for sort/thread fallbacks.
	var allSummaries []MessageSummary
	// matched counts the UIDs of the whole filtered set, independently of
	// the listing bound; it tells the thread path whether the scan covered
	// the folder or only a prefix of it.
	matched := 0
	if opts.SortBy != "" {
		if uids, sortErr := c.uidSort(ctx, criteria, imapSortKey(opts.SortBy, opts.SortDesc)); sortErr == nil {
			if hasAttachment {
				uids, err = c.filterAttachmentUIDs(ctx, uids)
				if err != nil {
					return ListResult{}, err
				}
			}
			ordered, serverSorted = uids, true
			matched = len(uids)
		}
	}
	if ordered == nil {
		uids, err := c.uidSearch(ctx, criteria)
		if err != nil {
			return ListResult{}, err
		}
		if hasAttachment {
			uids, err = c.filterAttachmentUIDs(ctx, uids)
			if err != nil {
				return ListResult{}, err
			}
		}
		// Newest first for arrival order; SortSummaries reorders as needed.
		for i, j := 0, len(uids)-1; i < j; i, j = i+1, j-1 {
			uids[i], uids[j] = uids[j], uids[i]
		}
		ordered = uids
		matched = len(uids)
		if opts.SortBy != "" {
			// Client-side sort fallback: the ordering is computed locally, so
			// it is bounded by MaxSortScan.
			if len(ordered) > MaxSortScan {
				ordered = ordered[:MaxSortScan]
			}
			if allSummaries, err = c.uidFetchSummaries(ctx, ordered); err != nil {
				return ListResult{}, err
			}
			SortSummaries(allSummaries, opts.SortBy, opts.SortDesc)
			ordered = ordered[:0]
			for _, s := range allSummaries {
				ordered = append(ordered, s.UID)
			}
		}
	}

	total := len(ordered)
	var page []uint32
	var next uint32
	if opts.CursorUID != 0 {
		page, next = PageAfterCursor(ordered, opts.CursorUID, opts.Limit)
	} else {
		if opts.Offset >= len(ordered) {
			page = nil
		} else {
			end := opts.Offset + opts.Limit
			if end > len(ordered) {
				end = len(ordered)
			}
			page = ordered[opts.Offset:end]
			if end < len(ordered) {
				next = ordered[end-1]
			}
		}
	}
	if len(page) == 0 {
		return ListResult{UIDValidity: c.selectedUIDValidity, Messages: []MessageSummary{}, Total: total, Sort: opts.SortBy, ServerSorted: serverSorted}, nil
	}
	summaries, err := c.uidFetchSummaries(ctx, page)
	if err != nil {
		return ListResult{}, err
	}
	if opts.Preview {
		// One batched command downloads the page's snippets; failures only
		// leave the previews empty (see preview_fetch.go).
		c.attachPreviews(ctx, summaries)
	}

	threadMode := ""
	threadScope := ""
	if opts.Thread {
		threadMode = "client"
		threadScope = ThreadScopeFolder
		if roots, counts, threadErr := c.uidThread(ctx, criteria); threadErr == nil && len(roots) > 0 {
			unverified, err := c.refineSystemThreads(ctx, summaries, roots, counts)
			if err != nil {
				return ListResult{}, err
			}
			// The THREAD response carries every member of every thread, so the
			// counts are folder-wide: a thread whose members are spread over
			// several pages reports the same total on each of them.
			ApplyThreadsWithCounts(summaries, roots, counts)
			threadMode = "server"
			threadScope = ThreadScopeServer
			for i := range summaries {
				if unverified[roots[summaries[i].UID]] {
					summaries[i].ThreadID = 0
					summaries[i].ThreadCount = 0
					summaries[i].WeakAggregation = false
					summaries[i].EarlierCount = 0
					summaries[i].ThreadVerification = "unverified"
					threadScope = "unverified"
				}
			}
		} else {
			// Client path: count over a bounded scan of the filtered set so
			// the counts do not depend on the page that was loaded.
			set := allSummaries
			if set == nil {
				scan := ordered
				if len(scan) > MaxSortScan {
					scan = scan[:MaxSortScan]
				}
				set, err = c.uidFetchSummaries(ctx, scan)
				if err != nil {
					return ListResult{}, err
				}
			}
			ApplyThreads(summaries, BuildThreads(set))
			if len(set) < matched {
				// The folder is larger than the scan bound: the counts cover
				// the scanned prefix only.
				threadScope = ThreadScopeBounded
			}
		}
	}
	return ListResult{
		UIDValidity:  c.selectedUIDValidity,
		Messages:     summaries,
		Total:        total,
		NextCursor:   next,
		Sort:         opts.SortBy,
		ServerSorted: serverSorted,
		ThreadMode:   threadMode,
		ThreadScope:  threadScope,
	}, nil
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
	msg := &Message{MessageSummary: summaries[0], UIDValidity: c.selectedUIDValidity, Attachments: []AttachmentMeta{}}
	if len(rawBody) > 0 {
		text, html, atts, parseErr := parseMIMEBodyStrict(rawBody)
		if parseErr != nil {
			return nil, parseErr
		}
		msg.Text = text
		msg.HTML = html
		msg.Attachments = atts
		msg.AttachmentsCapped = len(atts) >= MaxAttachmentsPerMessage
		msg.ReplyTo, msg.To, msg.Cc, msg.Bcc = parseHeaderRecipients(rawBody)
		msg.Authentication = mailauth.Read(rawBody, c.authenticationSecrets)
	}
	return msg, nil
}

func parseHeaderRecipients(raw []byte) (replyTo, to, cc, bcc []Address) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, nil, nil
	}
	return addressesFromHeader(msg.Header.Get("Reply-To")),
		addressesFromHeader(msg.Header.Get("To")),
		addressesFromHeader(msg.Header.Get("Cc")),
		addressesFromHeader(msg.Header.Get("Bcc"))
}

func addressesFromHeader(raw string) []Address {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if list, err := mail.ParseAddressList(raw); err == nil && len(list) > 0 {
		out := make([]Address, 0, len(list))
		for _, a := range list {
			if a == nil || a.Address == "" {
				continue
			}
			out = append(out, Address{Name: decodeMIMEHeader(a.Name), Email: strings.ToLower(a.Address)})
		}
		if len(out) > 0 {
			return out
		}
	}
	// Malformed headers must not blank the whole field (THREAD-UX-REVIEW F4:
	// garbled senders put an encoded-word where the addr-spec belongs, and
	// the raw undecoded text leaked into the UI). Salvage fragment by
	// fragment; a fragment that still carries no usable addr-spec survives
	// as a display-only address carrying the DECODED text, never the raw
	// encoded-word.
	var out []Address
	for _, frag := range strings.Split(raw, ",") {
		frag = strings.TrimSpace(frag)
		if frag == "" {
			continue
		}
		if a, err := mail.ParseAddress(frag); err == nil && a.Address != "" {
			out = append(out, Address{Name: decodeMIMEHeader(a.Name), Email: strings.ToLower(a.Address)})
			continue
		}
		decoded := strings.TrimSpace(decodeMIMEHeader(frag))
		if decoded == "" {
			continue
		}
		out = append(out, Address{Name: decoded, Email: ""})
	}
	return out
}

// GetAttachment re-FETCHes BODY[] and returns one part by PartID numbering
// matching parseMIMEBody (top-level multipart parts, 1-based).
func (c *imapConn) GetAttachment(ctx context.Context, folder string, uid uint32, partID string) (filename, contentType string, data []byte, err error) {
	if uid == 0 {
		return "", "", nil, fmt.Errorf("invalid uid")
	}
	partID = strings.TrimSpace(partID)
	if partID == "" {
		return "", "", nil, fmt.Errorf("attachment not found")
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return "", "", nil, err
	}
	rawBody, err := c.uidFetchBody(ctx, uid)
	if err != nil {
		return "", "", nil, err
	}
	if len(rawBody) == 0 {
		return "", "", nil, fmt.Errorf("attachment not found")
	}
	return extractMIMEPart(rawBody, partID)
}

func (c *imapConn) uidFetchBody(ctx context.Context, uid uint32) ([]byte, error) {
	finish := c.guardIO(ctx)
	defer finish()
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
		if budget, ok := ctx.Value(importBudgetKey{}).(*importReadBudget); ok && int64(litSize) > budget.remaining {
			return nil, fmt.Errorf("import literal exceeds remaining byte budget")
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

// MoveMessages uses UID MOVE when available and reports the destination UIDs
// from the server's UIDPLUS COPYUID acknowledgement, aligned with uids.
// A server that omits COPYUID returns a nil slice: the move still succeeded,
// but the caller cannot offer an exact undo without the new UIDs.
func (c *imapConn) MoveMessages(ctx context.Context, folder string, uids []uint32, dest string) ([]uint32, error) {
	if len(uids) == 0 || dest == "" {
		return nil, fmt.Errorf("move requires uids and destination")
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return nil, err
	}
	okLine, err := c.commandCaptureOK(ctx, "UID MOVE", formatUIDSet(uids), quoteIMAP(encodeMailboxWire(dest)))
	if err != nil {
		return nil, err
	}
	index := strings.Index(okLine, "[COPYUID ")
	if index < 0 {
		return nil, nil
	}
	fields := strings.Fields(strings.SplitN(okLine[index+9:], "]", 2)[0])
	if len(fields) != 3 {
		return nil, nil
	}
	src, srcErr := expandUIDPlusSet(fields[1])
	dst, dstErr := expandUIDPlusSet(fields[2])
	if srcErr != nil || dstErr != nil || len(src) != len(dst) {
		return nil, fmt.Errorf("invalid UIDPLUS COPYUID acknowledgement")
	}
	acknowledged := map[uint32]uint32{}
	for i, uid := range src {
		acknowledged[uid] = dst[i]
	}
	destinations := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		target, ok := acknowledged[uid]
		if !ok {
			return nil, fmt.Errorf("COPYUID did not confirm uid %d", uid)
		}
		destinations = append(destinations, target)
	}
	return destinations, nil
}

// DeleteMessages marks the given UIDs \Deleted and, when expunge is set,
// removes only those UIDs. A server without UIDPLUS must fail the call.
// Falling back to EXPUNGE would remove every other client's \Deleted mail.
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
	return c.command(ctx, "UID EXPUNGE", formatUIDSet(uids))
}

func (c *imapConn) selectFolder(ctx context.Context, folder string) error {
	validity, err := c.selectMailbox(ctx, folder, false)
	if err == nil {
		c.selectedUIDValidity = validity
	}
	return err
}

// FolderUIDValidity binds a mailbox generation for subsequent SELECT/FETCH calls.
func (c *imapConn) FolderUIDValidity(ctx context.Context, folder string) (uint32, error) {
	validity, err := c.selectMailbox(ctx, folder, true)
	if err != nil {
		return 0, err
	}
	if c.validities == nil {
		c.validities = make(map[string]uint32)
	}
	c.validities[folder] = validity
	return validity, nil
}

func (c *imapConn) selectMailbox(ctx context.Context, folder string, required bool) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if folder == "" {
		return 0, fmt.Errorf("empty folder")
	}
	finish := c.guardIO(ctx)
	defer finish()
	tag := c.nextTag()
	if _, err := io.WriteString(c.conn, tag+" SELECT "+quoteIMAP(encodeMailboxWire(folder))+"\r\n"); err != nil {
		return 0, err
	}
	var validity uint32
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		upper := strings.ToUpper(line)
		if i := strings.Index(upper, "[UIDVALIDITY "); i >= 0 {
			field := strings.SplitN(upper[i+len("[UIDVALIDITY "):], "]", 2)[0]
			n, parseErr := strconv.ParseUint(strings.TrimSpace(field), 10, 32)
			if parseErr == nil {
				validity = uint32(n)
			}
		}
		if strings.HasPrefix(line, tag+" OK") {
			expected := c.validities[folder]
			if (required || expected != 0) && validity == 0 {
				return 0, ErrDraftChanged
			}
			if expected != 0 && validity != expected {
				return 0, ErrDraftChanged
			}
			return validity, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return 0, fmt.Errorf("%s", strings.TrimSpace(line))
		}
	}
}

func (c *imapConn) uidSearch(ctx context.Context, criteria string) ([]uint32, error) {
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if criteria == "" {
		criteria = "ALL"
	}
	tag := c.nextTag()
	// criteria is produced only by BuildSearchCriteria (quoted literals).
	if _, err := io.WriteString(c.conn, tag+" UID SEARCH "+criteria+"\r\n"); err != nil {
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
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tag := c.nextTag()
	// HEADER.FIELDS (REFERENCES) joins the summary so the client threading
	// path can walk full References chains when In-Reply-To is missing.
	cmd := fmt.Sprintf("%s UID FETCH %s (UID FLAGS RFC822.SIZE ENVELOPE BODYSTRUCTURE BODY.PEEK[HEADER.FIELDS (REFERENCES)])\r\n", tag, formatUIDSet(uids))
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
				sum.UIDValidity = c.selectedUIDValidity
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
