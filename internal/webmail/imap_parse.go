package webmail

import (
	"fmt"
	"mime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

// parseEnvelope extracts subject/from/date from an IMAP ENVELOPE s-expression.
// ENVELOPE = (date subject from sender reply-to to cc bcc in-reply-to message-id)
func parseEnvelope(s string) (subject string, from []Address, date time.Time) {
	subject, from, _, _, _, date = parseEnvelopeParties(s)
	return subject, from, date
}

// parseEnvelopeIDs extracts in-reply-to (item 8) and message-id (item 9)
// from an ENVELOPE; both feed client-side threading.
func parseEnvelopeIDs(s string) (inReplyTo, messageID string) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "(") {
		return "", ""
	}
	inner, ok := stripOuterParens(trimmed)
	if !ok {
		return "", ""
	}
	items := splitTopLevel(inner)
	if len(items) > 8 {
		inReplyTo = decodeMIMEHeader(nstring(items[8]))
	}
	if len(items) > 9 {
		messageID = decodeMIMEHeader(nstring(items[9]))
	}
	return inReplyTo, messageID
}

// parseEnvelopeParties also returns the to/cc lists and Reply-To. An empty
// Reply-To means the sender did not set one; callers must not invent it from
// From. To/cc feed the threading subject fallback.
func parseEnvelopeParties(s string) (subject string, from, to, cc, replyTo []Address, date time.Time) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") {
		return "", nil, nil, nil, nil, time.Time{}
	}
	inner, ok := stripOuterParens(s)
	if !ok {
		return "", nil, nil, nil, nil, time.Time{}
	}
	items := splitTopLevel(inner)
	if len(items) < 3 {
		return "", nil, nil, nil, nil, time.Time{}
	}
	dateStr := nstring(items[0])
	subject = decodeMIMEHeader(nstring(items[1]))
	from = parseAddressList(items[2])
	if len(items) > 4 {
		replyTo = parseAddressList(items[4])
	}
	if len(items) > 5 {
		to = parseAddressList(items[5])
	}
	if len(items) > 6 {
		cc = parseAddressList(items[6])
	}
	if dateStr != "" {
		date = parseIMAPDate(dateStr)
	}
	return subject, from, to, cc, replyTo, date
}

func parseAddressList(s string) []Address {
	s = strings.TrimSpace(s)
	if s == "NIL" || s == "" {
		return nil
	}
	inner, ok := stripOuterParens(s)
	if !ok || strings.TrimSpace(inner) == "" {
		return nil
	}
	var out []Address
	for _, item := range splitTopLevel(inner) {
		if addr, ok := parseAddress(item); ok {
			out = append(out, addr)
		}
	}
	return out
}

// parseAddress parses (name route mailbox host).
func parseAddress(s string) (Address, bool) {
	s = strings.TrimSpace(s)
	inner, ok := stripOuterParens(s)
	if !ok {
		return Address{}, false
	}
	parts := splitTopLevel(inner)
	if len(parts) < 4 {
		return Address{}, false
	}
	name := decodeMIMEHeader(nstring(parts[0]))
	mailbox := nstring(parts[2])
	host := nstring(parts[3])
	if mailbox == "" || host == "" {
		return Address{}, false
	}
	return Address{Name: name, Email: mailbox + "@" + host}, true
}

func nstring(s string) string {
	s = strings.TrimSpace(s)
	if s == "NIL" {
		return ""
	}
	if strings.HasPrefix(s, "{") {
		// Literal already expanded into quoted form by the reader, or raw.
		return s
	}
	if strings.HasPrefix(s, "\"") {
		v, _, ok := readIMAPString(s)
		if ok {
			return v
		}
	}
	return s
}

func decodeMIMEHeader(s string) string {
	if s == "" {
		return ""
	}
	dec := mime.WordDecoder{CharsetReader: charset.NewReaderLabel}
	out, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}

// envelopeDate maps an unparsable ENVELOPE date to a nil pointer so the JSON
// payload omits the field entirely.
func envelopeDate(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func parseIMAPDate(s string) time.Time {
	layouts := []string{
		"02-Jan-2006 15:04:05 -0700",
		"2-Jan-2006 15:04:05 -0700",
		time.RFC1123Z,
		time.RFC1123,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func stripOuterParens(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return "", false
	}
	return s[1 : len(s)-1], true
}

// splitTopLevel splits an IMAP list body on spaces outside quotes, parens
// and square brackets. Brackets group BODY[...] section names (e.g.
// BODY[HEADER.FIELDS (REFERENCES)]), whose inner parens must not reset the
// nesting depth.
func splitTopLevel(s string) []string {
	var out []string
	var b strings.Builder
	depth := 0
	inQuote := false
	escaped := false
	flush := func() {
		if b.Len() == 0 {
			return
		}
		out = append(out, b.String())
		b.Reset()
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inQuote {
			b.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inQuote = false
			}
			continue
		}
		switch ch {
		case '"':
			inQuote = true
			b.WriteByte(ch)
		case '(', '[':
			depth++
			b.WriteByte(ch)
		case ')', ']':
			if depth > 0 {
				depth--
			}
			b.WriteByte(ch)
		case ' ', '\t':
			if depth == 0 {
				flush()
				continue
			}
			b.WriteByte(ch)
		default:
			b.WriteByte(ch)
		}
	}
	flush()
	return out
}

// parseFetchAttrs parses the parenthesized attribute list of a FETCH response.
// Values may contain nested lists; BODY[] / RFC822 literals are already inlined
// as the raw string content (not quoted).
func parseFetchAttrs(s string) map[string]string {
	s = strings.TrimSpace(s)
	inner, ok := stripOuterParens(s)
	if !ok {
		inner = s
	}
	items := splitTopLevel(inner)
	out := make(map[string]string)
	for i := 0; i < len(items); {
		key := strings.ToUpper(items[i])
		i++
		if i >= len(items) {
			out[key] = ""
			break
		}
		// BODY[…] / BODY.PEEK[…] take the following value.
		if strings.HasPrefix(key, "BODY") || key == "RFC822" || key == "RFC822.HEADER" || key == "RFC822.TEXT" {
			out[key] = items[i]
			i++
			continue
		}
		out[key] = items[i]
		i++
	}
	return out
}

func parseUID(v string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(n), nil
}

func parseSize(v string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func parseFlags(v string) []string {
	v = strings.TrimSpace(v)
	inner, ok := stripOuterParens(v)
	if !ok {
		return nil
	}
	if strings.TrimSpace(inner) == "" {
		return []string{}
	}
	return strings.Fields(inner)
}

func formatUIDSet(uids []uint32) string {
	if len(uids) == 0 {
		return ""
	}
	parts := make([]string, len(uids))
	for i, u := range uids {
		parts[i] = strconv.FormatUint(uint64(u), 10)
	}
	return strings.Join(parts, ",")
}

func parseUIDSearch(lines []string) []uint32 {
	var uids []uint32
	for _, line := range lines {
		if !strings.HasPrefix(line, "* SEARCH") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "* SEARCH"))
		if rest == "" {
			continue
		}
		for _, f := range strings.Fields(rest) {
			n, err := strconv.ParseUint(f, 10, 32)
			if err != nil {
				continue
			}
			uids = append(uids, uint32(n))
		}
	}
	return uids
}

func extractFetchPayload(line string) (attrs string, ok bool) {
	// * <seq> FETCH (...)
	if !strings.HasPrefix(line, "* ") {
		return "", false
	}
	rest := line[2:]
	idx := strings.Index(rest, " FETCH ")
	if idx < 0 {
		return "", false
	}
	payload := strings.TrimSpace(rest[idx+len(" FETCH "):])
	if !strings.HasPrefix(payload, "(") {
		return "", false
	}
	return payload, true
}

func summaryFromAttrs(attrs map[string]string) (MessageSummary, error) {
	uid, err := parseUID(attrs["UID"])
	if err != nil {
		return MessageSummary{}, fmt.Errorf("missing uid")
	}
	subject, from, to, cc, _, date := parseEnvelopeParties(attrs["ENVELOPE"])
	inReplyTo, messageID := parseEnvelopeIDs(attrs["ENVELOPE"])
	return MessageSummary{
		UID:            uid,
		Subject:        subject,
		From:           from,
		To:             to,
		Cc:             cc,
		Date:           envelopeDate(date),
		Flags:          parseFlags(attrs["FLAGS"]),
		Size:           parseSize(attrs["RFC822.SIZE"]),
		InReplyTo:      inReplyTo,
		MessageID:      messageID,
		References:     fetchReferences(attrs),
		HasAttachments: bodyStructureHasAttachment(attrs["BODYSTRUCTURE"], 0),
	}, nil
}

// fetchReferences pulls the References header out of the FETCH attrs. The
// section key spelling varies slightly across servers (spacing inside the
// brackets), so the lookup matches on the section prefix and field name.
func fetchReferences(attrs map[string]string) []string {
	for k, v := range attrs {
		if !strings.HasPrefix(k, "BODY[") || !strings.Contains(k, "REFERENCES") {
			continue
		}
		value, _, ok := readIMAPString(v)
		if !ok {
			value = nstring(v)
		}
		return parseMessageIDs(value)
	}
	return nil
}

// parseMessageIDs extracts every <...> token from a header field value
// (References chains span folded lines; whitespace never falls inside a
// msg-id).
func parseMessageIDs(s string) []string {
	var out []string
	for {
		start := strings.IndexByte(s, '<')
		if start < 0 {
			return out
		}
		end := strings.IndexByte(s[start:], '>')
		if end < 0 {
			return out
		}
		if id := s[start+1 : start+end]; id != "" {
			out = append(out, id)
		}
		s = s[start+end+1:]
	}
}
