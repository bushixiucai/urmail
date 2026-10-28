package webmail

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// MIME metadata is read from Dovecot BODYSTRUCTURE, never inferred from a
// message subject, top-level Content-Type, or an untrusted cached flag.
func (c *imapConn) filterAttachmentUIDs(ctx context.Context, uids []uint32) ([]uint32, error) {
	if len(uids) > 20000 {
		return nil, fmt.Errorf("%w: narrow attachment search to at most 20000 candidates", ErrInvalidSearchQuery)
	}
	var result []uint32
	for offset := 0; offset < len(uids); offset += 100 {
		end := min(offset+100, len(uids))
		batch := uids[offset:end]
		found, err := c.fetchAttachmentFlags(ctx, batch)
		if err != nil {
			return nil, err
		}
		for _, uid := range batch {
			if found[uid] {
				result = append(result, uid)
			}
		}
	}
	return result, nil
}

func (c *imapConn) fetchAttachmentFlags(ctx context.Context, uids []uint32) (map[uint32]bool, error) {
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tag := c.nextTag()
	if _, err := fmt.Fprintf(c.conn, "%s UID FETCH %s (UID BODYSTRUCTURE)\r\n", tag, formatUIDSet(uids)); err != nil {
		return nil, err
	}
	result := map[uint32]bool{}
	for {
		line, err := c.readLogicalLine()
		if err != nil {
			return nil, err
		}
		if payload, ok := extractFetchPayload(line); ok {
			attrs := parseFetchAttrs(payload)
			uid, uidErr := parseUID(attrs["UID"])
			if uidErr == nil && attrs["BODYSTRUCTURE"] != "" {
				result[uid] = bodyStructureHasAttachment(attrs["BODYSTRUCTURE"], 0)
			}
		}
		if strings.HasPrefix(line, tag+" OK") {
			return result, nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, fmt.Errorf("attachment metadata: %w", io.ErrUnexpectedEOF)
		}
	}
}

// RFC 3501 section 7.4.2: multipart extensions begin after the subtype;
// single-part extensions follow MD5 (text adds lines, message/rfc822 adds
// envelope, nested body and lines). Only actual MIME disposition/filename
// parameters count, including named inline attachments.
func bodyStructureHasAttachment(raw string, depth int) bool {
	if depth > 64 {
		return false
	}
	inner, ok := stripOuterParens(strings.TrimSpace(raw))
	if !ok {
		return false
	}
	parts := splitBodyStructure(inner)
	if len(parts) == 0 {
		return false
	}
	if strings.HasPrefix(parts[0], "(") {
		i := 0
		for i < len(parts) && strings.HasPrefix(parts[i], "(") {
			if bodyStructureHasAttachment(parts[i], depth+1) {
				return true
			}
			i++
		}
		return i+2 < len(parts) && attachmentDisposition(parts[i+2])
	}
	if len(parts) < 7 {
		return false
	}
	if mimeFilename(parts[2], "name") {
		return true
	}
	kind, sub := strings.ToUpper(nstring(parts[0])), strings.ToUpper(nstring(parts[1]))
	disposition := 8
	if kind == "TEXT" {
		disposition = 9
	}
	if kind == "MESSAGE" && sub == "RFC822" {
		if len(parts) > 8 && bodyStructureHasAttachment(parts[8], depth+1) {
			return true
		}
		disposition = 11
	}
	return len(parts) > disposition && attachmentDisposition(parts[disposition])
}
func attachmentDisposition(raw string) bool {
	inner, ok := stripOuterParens(raw)
	if !ok {
		return false
	}
	parts := splitTopLevel(inner)
	return len(parts) > 0 && (strings.EqualFold(nstring(parts[0]), "attachment") || len(parts) > 1 && mimeFilename(parts[1], "filename"))
}
func mimeFilename(raw, key string) bool {
	inner, ok := stripOuterParens(raw)
	if !ok {
		return false
	}
	p := splitTopLevel(inner)
	for i := 0; i+1 < len(p); i += 2 {
		name := strings.ToLower(nstring(p[i]))
		if (name == key || strings.HasPrefix(name, key+"*")) && nstring(p[i+1]) != "" {
			return true
		}
	}
	return false
}

// Multipart body lists may be adjacent without whitespace on the wire.
func splitBodyStructure(raw string) []string {
	var normalized strings.Builder
	depth := 0
	quoted := false
	escaped := false
	for _, r := range raw {
		normalized.WriteRune(r)
		if quoted {
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				quoted = false
			}
			continue
		}
		switch r {
		case '"':
			quoted = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				normalized.WriteByte(' ')
			}
		}
	}
	return splitTopLevel(normalized.String())
}
