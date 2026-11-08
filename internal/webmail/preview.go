package webmail

// List preview snippets (v0.6.10 L3).
//
// The folder list renders one line of body text under the subject. The
// snippet is derived from the plain-text body of the message:
//
//   - quoted reply lines ("> ..."), quote headers ("On ... wrote:") and the
//     signature block (everything after the RFC 3676 "-- " delimiter) are
//     removed;
//   - the first one or two remaining lines are kept;
//   - whitespace is collapsed and the result is truncated to previewMaxChars
//     runes, preferring a word boundary.
//
// Only the head of a message is downloaded: the list endpoint fetches one
// bounded snippet per message with a single batched IMAP command per page
// (see preview_fetch.go).

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/quotedprintable"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	// previewMaxChars caps the snippet length in runes.
	previewMaxChars = 120
	// previewFetchBytes is the per-message byte budget for the batched fetch.
	previewFetchBytes = 2048
	// previewPageBytes caps the total bytes downloaded for one page of
	// snippets.
	previewPageBytes = 128 << 10
	// previewMinFetchBytes is the floor for the adaptive per-message budget.
	previewMinFetchBytes = 512
)

var (
	// quoteHeaderPattern matches the "On <date>, <who> wrote:" style header
	// that introduces a quoted reply.
	quoteHeaderPattern = regexp.MustCompile(`(?i)(wrote|schrieb|写道)\s*[:：]\s*$`)
	// originalMessagePattern matches the separator that introduces the
	// quoted original mail ("-----Original Message-----", "转发邮件", ...).
	// Everything behind it is quoted, so the snippet stops there.
	originalMessagePattern = regexp.MustCompile(`(?i)^-{2,}.*(original message|forwarded message|原始邮件|转发邮件).*-{2,}$`)
	// signaturePattern matches the RFC 3676 signature delimiter line
	// ("-- "); the signature itself is dropped.
	signaturePattern = regexp.MustCompile(`^--\s*$`)
	// cidReferencePattern matches inline "cid:" references, including their
	// usual bracket decoration ("[cid:...]"). They are image pointers,
	// never readable text, so they never enter a snippet.
	cidReferencePattern = regexp.MustCompile(`(?i)\[[^\]\n]*\bcid:[^\]\n]*\]|\((?i:cid)[^)\n]*\)|\bcid:[^\s"'<>\[\]()]`)
	// fetchUIDPattern extracts the UID attribute of a FETCH response.
	fetchUIDPattern = regexp.MustCompile(`(?i)\bUID\s+(\d+)`)
)

// previewSource is the raw snippet candidate of one MIME part: the part
// content plus the transfer encoding and charset needed to decode it.
type previewSource struct {
	content string
	cte     string
	charset string
	isHTML  bool
}

// previewFromRaw builds the snippet of a (possibly truncated) raw message.
// The message only has to contain its headers and the beginning of its body;
// a truncated multipart message simply yields a shorter snippet.
func previewFromRaw(raw []byte) string {
	header, body, ok := splitMessageHead(raw)
	if !ok {
		return ""
	}
	fields := parseHeaderFields(header)
	mediaType, params := parseContentType(fields["content-type"])
	source, ok := collectPreviewSource(body, mediaType, params, strings.ToLower(strings.TrimSpace(fields["content-transfer-encoding"])), 0)
	if !ok {
		return ""
	}
	content := decodeTransferEncoding([]byte(source.content), source.cte)
	content = decodeCharset(content, source.charset)
	text := string(content)
	if source.isHTML {
		text = htmlToPlainText(text)
	}
	return snippetFromPlainText(text)
}

// collectPreviewSource finds the part that carries the readable body. A
// text/plain part always wins; a text/html part is the fallback. Multipart
// bodies are walked part by part, so inline "cid:" images never contribute
// text.
func collectPreviewSource(body []byte, mediaType string, params map[string]string, cte string, depth int) (previewSource, bool) {
	if depth > 3 {
		return previewSource{}, false
	}
	switch mediaType {
	case "text/plain":
		return previewSource{content: string(body), cte: cte, charset: params["charset"]}, true
	case "text/html":
		return previewSource{content: string(body), cte: cte, charset: params["charset"], isHTML: true}, true
	case "":
		// A message without Content-Type is plain text per RFC 2045.
		return previewSource{content: string(body), cte: cte, charset: params["charset"]}, true
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return previewSource{}, false
	}
	boundary := params["boundary"]
	if boundary == "" {
		return previewSource{}, false
	}
	var fallback previewSource
	haveFallback := false
	for _, part := range splitMultipartParts(body, boundary) {
		partFields := parseHeaderFields(part.header)
		partType, partParams := parseContentType(partFields["content-type"])
		partCTE := strings.ToLower(strings.TrimSpace(partFields["content-transfer-encoding"]))
		if strings.HasPrefix(partType, "multipart/") {
			nested, ok := collectPreviewSource(part.body, partType, partParams, partCTE, depth+1)
			if ok && nested.isHTML {
				if !haveFallback {
					fallback, haveFallback = nested, true
				}
				continue
			}
			if ok {
				return nested, true
			}
			continue
		}
		switch partType {
		case "text/plain":
			return previewSource{content: string(part.body), cte: partCTE, charset: partParams["charset"]}, true
		case "text/html":
			if !haveFallback {
				fallback = previewSource{content: string(part.body), cte: partCTE, charset: partParams["charset"], isHTML: true}
				haveFallback = true
			}
		}
	}
	return fallback, haveFallback
}

// snippetFromPlainText applies the snippet rules to a plain-text body:
// quoted lines, quote headers and the signature block are dropped, the first
// one or two remaining lines are kept, whitespace is collapsed and the
// result is truncated.
func snippetFromPlainText(text string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	kept := make([]string, 0, 2)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			continue // quoted reply line
		}
		if originalMessagePattern.MatchString(trimmed) {
			break // the quoted original mail starts here
		}
		if quoteHeaderPattern.MatchString(trimmed) {
			continue // quote header, the quoted body follows
		}
		if signaturePattern.MatchString(trimmed) {
			break // signature block starts here
		}
		kept = append(kept, trimmed)
		if len(kept) == 2 {
			break
		}
	}
	snippet := cidReferencePattern.ReplaceAllString(strings.Join(kept, " "), "")
	return truncateSnippet(collapseSpaces(snippet))
}

// collapseSpaces folds every run of whitespace into a single space.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateSnippet shortens a snippet to previewMaxChars runes, cutting at a
// word boundary when one is close enough to the limit.
func truncateSnippet(s string) string {
	runes := []rune(s)
	if len(runes) <= previewMaxChars {
		return s
	}
	cut := previewMaxChars - 1 // room for the ellipsis
	prefix := string(runes[:cut])
	if idx := strings.LastIndex(prefix, " "); idx > 0 && idx >= cut/2 {
		prefix = prefix[:idx]
	}
	return strings.TrimRight(prefix, " ") + "…"
}

// htmlToPlainText renders an HTML body as plain text. Attributes (including
// "cid:" image references) never become text, and script/style content is
// dropped.
func htmlToPlainText(src string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(src))
	var out strings.Builder
	skipDepth := 0
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return out.String()
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			switch string(name) {
			case "script", "style", "head":
				skipDepth++
			case "br", "p", "div", "li", "tr", "blockquote", "table", "section", "article":
				out.WriteByte('\n')
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			switch string(name) {
			case "script", "style", "head":
				if skipDepth > 0 {
					skipDepth--
				}
			case "p", "div", "li", "tr", "blockquote", "table", "section", "article":
				out.WriteByte('\n')
			}
		case html.TextToken:
			if skipDepth == 0 {
				out.Write(tokenizer.Text())
			}
		}
	}
}

// decodeTransferEncoding undoes the Content-Transfer-Encoding of a part. The
// input may be truncated, so decoding stops at the first damaged byte
// instead of failing.
func decodeTransferEncoding(body []byte, cte string) []byte {
	switch cte {
	case "base64":
		cleaned := stripWhitespace(body)
		end := len(cleaned) - len(cleaned)%4
		decoded, err := base64.StdEncoding.DecodeString(string(cleaned[:end]))
		if err != nil {
			return decoded
		}
		return decoded
	case "quoted-printable":
		decoded, _ := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		return decoded
	default:
		return body
	}
}

// decodeCharset converts a part to UTF-8. Unknown or broken charsets keep the
// original bytes.
func decodeCharset(body []byte, label string) []byte {
	label = strings.TrimSpace(label)
	if label == "" || strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
		return body
	}
	reader, err := charset.NewReaderLabel(label, bytes.NewReader(body))
	if err != nil {
		return body
	}
	decoded, err := io.ReadAll(reader)
	if err != nil && len(decoded) == 0 {
		return body
	}
	return decoded
}

// stripWhitespace removes the whitespace characters that MIME encoders add.
func stripWhitespace(body []byte) []byte {
	out := make([]byte, 0, len(body))
	for _, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
		default:
			out = append(out, b)
		}
	}
	return out
}

// splitMessageHead splits a raw message into its header block and its body.
func splitMessageHead(raw []byte) (header, body []byte, ok bool) {
	if idx := bytes.Index(raw, []byte("\r\n\r\n")); idx >= 0 {
		return raw[:idx], raw[idx+4:], true
	}
	if idx := bytes.Index(raw, []byte("\n\n")); idx >= 0 {
		return raw[:idx], raw[idx+2:], true
	}
	return nil, nil, false
}

// parseHeaderFields parses a MIME header block into lowercase field names.
// Malformed lines are skipped; folded continuation lines are joined.
func parseHeaderFields(header []byte) map[string]string {
	fields := map[string]string{}
	var lastKey string
	for _, line := range strings.Split(string(header), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if lastKey != "" {
				fields[lastKey] += " " + strings.TrimSpace(line)
			}
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		fields[key] = strings.TrimSpace(line[idx+1:])
		lastKey = key
	}
	return fields
}

// parseContentType splits a Content-Type header into its media type and
// parameters. An unparsable header is treated as plain text.
func parseContentType(value string) (string, map[string]string) {
	params := map[string]string{}
	value = strings.TrimSpace(value)
	if value == "" {
		return "text/plain", params
	}
	mediaType, parsed, err := mime.ParseMediaType(value)
	if err != nil {
		parts := strings.Split(value, ";")
		mediaType = strings.ToLower(strings.TrimSpace(parts[0]))
		for _, part := range parts[1:] {
			key, val, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			params[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(val), "\"")
		}
		return mediaType, params
	}
	for key, val := range parsed {
		params[strings.ToLower(key)] = val
	}
	return strings.ToLower(mediaType), params
}

// mimePart is one part of a multipart body.
type mimePart struct {
	header []byte
	body   []byte
}

// splitMultipartParts splits a multipart body into its parts. The body may be
// truncated: the last part is then simply incomplete.
func splitMultipartParts(body []byte, boundary string) []mimePart {
	delimiter := "--" + boundary
	lines := splitRawLines(body)
	var parts []mimePart
	index := 0
	for index < len(lines) {
		trimmed := strings.TrimSpace(string(body[lines[index].start:lines[index].end]))
		if !strings.HasPrefix(trimmed, delimiter) {
			index++
			continue
		}
		if strings.TrimSpace(strings.TrimPrefix(trimmed, delimiter)) == "--" {
			break // closing delimiter
		}
		headerStart := index + 1
		headerEnd := headerStart
		for headerEnd < len(lines) && strings.TrimSpace(string(body[lines[headerEnd].start:lines[headerEnd].end])) != "" {
			headerEnd++
		}
		if headerEnd >= len(lines) {
			break // truncated before the part body
		}
		contentStart := lines[headerEnd].end
		for contentStart < len(body) && (body[contentStart] == '\r' || body[contentStart] == '\n') {
			contentStart++
		}
		contentEnd := len(body)
		for next := headerEnd + 1; next < len(lines); next++ {
			candidate := strings.TrimSpace(string(body[lines[next].start:lines[next].end]))
			if strings.HasPrefix(candidate, delimiter) {
				contentEnd = lines[next].start
				index = next
				break
			}
		}
		parts = append(parts, mimePart{
			header: body[lines[headerStart].start:lines[headerEnd].start],
			body:   body[contentStart:contentEnd],
		})
		if index <= headerEnd {
			index = headerEnd + 1
		}
	}
	return parts
}

// rawLine is one line of a byte slice, without its line terminator.
type rawLine struct {
	start int
	end   int
}

// splitRawLines splits a byte slice into its lines.
func splitRawLines(body []byte) []rawLine {
	var lines []rawLine
	start := 0
	for i := 0; i < len(body); i++ {
		if body[i] != '\n' {
			continue
		}
		end := i
		if end > start && body[end-1] == '\r' {
			end--
		}
		lines = append(lines, rawLine{start: start, end: end})
		start = i + 1
	}
	if start < len(body) {
		lines = append(lines, rawLine{start: start, end: len(body)})
	}
	return lines
}
