// Package mailimport validates bounded, byte-preserving EML and mboxrd uploads.
package mailimport

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

// Import limits and item error codes are shared by preview and confirmation.
const (
	MaxSourceBytes         = 20 << 20
	MaxMessageBytes        = 8 << 20
	MaxItems               = 100
	maxHeaderBytes         = 64 << 10
	maxMIMEParts           = 256
	maxMIMEDepth           = 16
	CodeInvalidHeaders     = "MAIL_IMPORT_INVALID_HEADERS"
	CodeInvalidMIME        = "MAIL_IMPORT_INVALID_MIME"
	CodeMessageTooLarge    = "MAIL_IMPORT_MESSAGE_TOO_LARGE"
	CodeStructureLimit     = "MAIL_IMPORT_STRUCTURE_LIMIT"
	CodeInvalidEncoding    = "MAIL_IMPORT_INVALID_ENCODING"
	CodeUnsupportedCharset = "MAIL_IMPORT_UNSUPPORTED_CHARSET"
	CodeEmptyMessage       = "MAIL_IMPORT_EMPTY_MESSAGE"
	CodeBinaryMBOX         = "MAIL_IMPORT_BINARY_MBOX_UNSUPPORTED"
)

// Import errors distinguish invalid complete uploads from per-item refusals.
var (
	ErrSourceTooLarge    = errors.New("mail import source exceeds limit")
	ErrUnsupportedFormat = errors.New("unsupported mail import format")
	ErrTooManyItems      = errors.New("mail import item count exceeds limit")
	ErrInvalidSource     = errors.New("invalid mail import source")
	ErrMBOXFraming       = errors.New("message cannot be represented losslessly in mboxrd")
)

// Item describes one source message without exposing its MIME bytes in JSON.
type Item struct {
	Index       int    `json:"index"`
	Subject     string `json:"subject"`
	MessageID   string `json:"message_id"`
	Raw         []byte `json:"-"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ErrorCode   string `json:"error_code,omitempty"`
	DuplicateOf int    `json:"duplicate_of,omitempty"`
}

// Preview binds a parsed upload to its exact original bytes and stable indices.
type Preview struct {
	SourceDigest string `json:"source_digest"`
	Format       string `json:"format"`
	Filename     string `json:"filename"`
	Items        []Item `json:"items"`
	Bytes        int64  `json:"bytes"`
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// Parse never silently truncates a source, message list, or damaged item.
func Parse(format, filename string, source []byte) (Preview, error) {
	preview := Preview{SourceDigest: "", Format: strings.ToLower(strings.TrimSpace(format)), Filename: safeFilename(filename), Bytes: int64(len(source)), Items: []Item{}}
	if len(source) > MaxSourceBytes {
		return preview, ErrSourceTooLarge
	}
	preview.SourceDigest = digest(source)
	if len(source) == 0 {
		return preview, ErrInvalidSource
	}
	var messages [][]byte
	switch preview.Format {
	case "eml":
		messages = [][]byte{source}
	case "mbox":
		var err error
		messages, err = splitMBOX(source)
		if err != nil {
			return preview, err
		}
	default:
		return preview, ErrUnsupportedFormat
	}
	seen := make(map[string]int, len(messages))
	for index, raw := range messages {
		item := Item{Index: index + 1}
		switch {
		case len(raw) == 0:
			item.ErrorCode = CodeEmptyMessage
		case len(raw) > MaxMessageBytes:
			item.ErrorCode = CodeMessageTooLarge
		default:
			if preview.Format == "mbox" {
				raw = unescapeMBOX(raw)
			}
			if preview.Format == "mbox" && hasBinaryMIME(raw) {
				item.ErrorCode = CodeBinaryMBOX
			} else {
				raw = NormalizeRaw(raw)
				if len(raw) > MaxMessageBytes {
					item.ErrorCode = CodeMessageTooLarge
				} else {
					item = validateItem(index+1, raw)
				}
			}
		}
		if item.ErrorCode == "" {
			if first, exists := seen[item.SHA256]; exists {
				item.DuplicateOf = first
			} else {
				seen[item.SHA256] = item.Index
			}
		}
		preview.Items = append(preview.Items, item)
	}
	return preview, nil
}

func safeFilename(filename string) string {
	filename = path.Base(strings.ReplaceAll(filename, `\`, "/"))
	filename = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, filename)
	if filename == "." || filename == ".." || filename == "/" || !utf8.ValidString(filename) {
		return "mail-upload"
	}
	return truncate(filename, 200)
}
func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

// NormalizeRaw canonicalizes textual line endings without rewriting any header
// or message identity. Explicit binary MIME bodies keep their original octets;
// mboxrd export rejects those bodies rather than pretending to preserve them.
func NormalizeRaw(raw []byte) []byte {
	if hasBinaryMIME(raw) {
		if end, ok := headerEnd(raw); ok {
			out := normalizeLines(raw[:end])
			return append(out, raw[end:]...)
		}
	}
	return normalizeLines(raw)
}
func hasBinaryMIME(raw []byte) bool {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	parts := 0
	return binaryPart(textproto.MIMEHeader(message.Header), message.Body, 0, &parts)
}
func binaryPart(header textproto.MIMEHeader, body io.Reader, depth int, parts *int) bool {
	*parts++
	if depth > maxMIMEDepth || *parts > maxMIMEParts {
		return true
	}
	encoding := strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding")))
	if encoding == "binary" {
		return true
	}
	if encoding == "base64" || encoding == "quoted-printable" {
		return false
	}
	media, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		return false
	}
	if strings.HasPrefix(media, "multipart/") && params["boundary"] != "" {
		reader := multipart.NewReader(body, params["boundary"])
		for {
			part, e := reader.NextRawPart()
			if e != nil {
				return false
			}
			if binaryPart(part.Header, part, depth+1, parts) {
				return true
			}
		}
	}
	if media == "message/rfc822" {
		message, e := mail.ReadMessage(body)
		return e == nil && binaryPart(textproto.MIMEHeader(message.Header), message.Body, depth+1, parts)
	}
	return false
}

func normalizeLines(raw []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(raw))
	for i, b := range raw {
		if b == '\n' && (i == 0 || raw[i-1] != '\r') {
			out.WriteByte('\r')
		}
		out.WriteByte(b)
	}
	return out.Bytes()
}
func headerEnd(raw []byte) (int, bool) {
	for pos := 0; pos < len(raw); {
		relative := bytes.IndexByte(raw[pos:], '\n')
		if relative < 0 {
			return 0, false
		}
		end := pos + relative + 1
		line := bytes.TrimSuffix(raw[pos:end-1], []byte{'\r'})
		if len(line) == 0 {
			return end, true
		}
		pos = end
	}
	return 0, false
}
func headersValid(header textproto.MIMEHeader) bool {
	size := 0
	for name, values := range header {
		size += len(name)
		for _, value := range values {
			size += len(value)
			if strings.IndexFunc(value, func(r rune) bool { return r < 32 && r != '\t' || r == 127 }) >= 0 {
				return false
			}
		}
		switch strings.ToLower(name) {
		case "subject", "message-id", "content-type", "content-transfer-encoding", "content-disposition", "mime-version":
			if len(values) != 1 {
				return false
			}
		}
	}
	return size <= maxHeaderBytes
}
func rawHeadersValid(raw []byte) bool {
	end, ok := headerEnd(raw)
	if !ok || end > maxHeaderBytes {
		return false
	}
	for _, line := range bytes.Split(raw[:end], []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) > 16384 || bytes.ContainsAny(line, "\x00\r") {
			return false
		}
	}
	return true
}
func validateItem(index int, raw []byte) Item {
	item := Item{Index: index}
	if !rawHeadersValid(raw) {
		item.ErrorCode = CodeInvalidHeaders
		return item
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil || !headersValid(textproto.MIMEHeader(message.Header)) {
		item.ErrorCode = CodeInvalidHeaders
		return item
	}
	decoder := mime.WordDecoder{CharsetReader: charset.NewReaderLabel}
	subject, err := decoder.DecodeHeader(message.Header.Get("Subject"))
	if err != nil || !utf8.ValidString(subject) {
		item.ErrorCode = CodeInvalidHeaders
		return item
	}
	subject = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, subject)
	id := strings.TrimSpace(message.Header.Get("Message-ID"))
	if len(id) > 998 || !utf8.ValidString(id) || strings.ContainsAny(id, "\r\n\x00") {
		item.ErrorCode = CodeInvalidHeaders
		return item
	}
	budget := mimeBudget{}
	if code := validateMIME(textproto.MIMEHeader(message.Header), message.Body, 0, &budget); code != "" {
		item.ErrorCode = code
		return item
	}
	item.Subject = truncate(subject, 256)
	item.MessageID = id
	item.Raw = raw
	item.SHA256 = digest(raw)
	item.Size = int64(len(raw))
	return item
}

type mimeBudget struct {
	parts   int
	decoded int64
}

func validateMIME(header textproto.MIMEHeader, body io.Reader, depth int, budget *mimeBudget) string {
	budget.parts++
	if depth > maxMIMEDepth || budget.parts > maxMIMEParts {
		return CodeStructureLimit
	}
	if !headersValid(header) {
		return CodeInvalidHeaders
	}
	contentType := header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return CodeInvalidMIME
	}
	if disposition := header.Get("Content-Disposition"); disposition != "" {
		if _, _, err = mime.ParseMediaType(disposition); err != nil {
			return CodeInvalidMIME
		}
	}
	decoded := body
	switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
	case "base64":
		decoded = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		decoded = quotedprintable.NewReader(body)
	case "", "7bit", "8bit", "binary":
	default:
		return CodeInvalidEncoding
	}
	if strings.HasPrefix(media, "multipart/") {
		if len(params["boundary"]) == 0 || len(params["boundary"]) > 70 {
			return CodeInvalidMIME
		}
		reader := multipart.NewReader(decoded, params["boundary"])
		parts := 0
		for {
			part, nextErr := reader.NextRawPart()
			if nextErr == io.EOF {
				if parts == 0 {
					return CodeInvalidMIME
				}
				return ""
			}
			if nextErr != nil {
				return CodeInvalidMIME
			}
			parts++
			if code := validateMIME(part.Header, part, depth+1, budget); code != "" {
				return code
			}
			if err = part.Close(); err != nil {
				return CodeInvalidMIME
			}
		}
	}
	if media == "message/rfc822" {
		reader := bufio.NewReader(io.LimitReader(decoded, MaxMessageBytes+1))
		message, e := mail.ReadMessage(reader)
		if e != nil {
			return CodeInvalidMIME
		}
		return validateMIME(textproto.MIMEHeader(message.Header), message.Body, depth+1, budget)
	}
	remaining := int64(MaxMessageBytes) - budget.decoded
	if remaining < 0 {
		return CodeMessageTooLarge
	}
	if strings.HasPrefix(media, "text/") {
		data, e := io.ReadAll(io.LimitReader(decoded, remaining+1))
		if e != nil {
			return CodeInvalidEncoding
		}
		if int64(len(data)) > remaining {
			return CodeMessageTooLarge
		}
		budget.decoded += int64(len(data))
		label := strings.ToLower(params["charset"])
		if label == "" || label == "utf-8" || label == "utf8" {
			if !utf8.Valid(data) {
				return CodeInvalidEncoding
			}
		} else {
			text, e := charset.NewReaderLabel(label, bytes.NewReader(data))
			if e != nil {
				return CodeUnsupportedCharset
			}
			if _, e = io.Copy(io.Discard, io.LimitReader(text, 4*int64(MaxMessageBytes)+1)); e != nil {
				return CodeInvalidEncoding
			}
		}
		return ""
	}
	n, e := io.Copy(io.Discard, io.LimitReader(decoded, remaining+1))
	if e != nil {
		return CodeInvalidEncoding
	}
	budget.decoded += n
	if n > remaining {
		return CodeMessageTooLarge
	}
	return ""
}

func envelope(line []byte) bool {
	if len(line) > 1024 || !bytes.HasPrefix(line, []byte("From ")) {
		return false
	}
	fields := strings.Fields(string(line))
	if len(fields) < 7 || fields[1] == "" {
		return false
	}
	fields = fields[2:]
	for i, field := range fields {
		if field == "remote" {
			fields = fields[:i]
			break
		}
	}
	value := strings.Join(fields, " ")
	for _, layout := range []string{time.ANSIC, "Mon Jan 2 15:04:05 -0700 2006", "Mon Jan 2 15:04:05 MST 2006", "Mon Jan 2 15:04:05 2006 -0700"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}
func stripFrame(raw []byte) []byte {
	if bytes.HasSuffix(raw, []byte("\r\n\r\n")) {
		return raw[:len(raw)-2]
	}
	if bytes.HasSuffix(raw, []byte("\n\n")) {
		return raw[:len(raw)-1]
	}
	return raw
}
func splitMBOX(source []byte) ([][]byte, error) {
	messages := [][]byte{}
	start := -1
	for pos := 0; pos < len(source); {
		relative := bytes.IndexByte(source[pos:], '\n')
		end := len(source)
		if relative >= 0 {
			end = pos + relative + 1
		}
		line := bytes.TrimSuffix(bytes.TrimSuffix(source[pos:end], []byte{'\n'}), []byte{'\r'})
		if envelope(line) {
			if start >= 0 {
				messages = append(messages, stripFrame(source[start:pos]))
				if len(messages) >= MaxItems {
					return nil, ErrTooManyItems
				}
			} else if pos != 0 {
				return nil, ErrInvalidSource
			}
			start = end
		}
		pos = end
	}
	if start < 0 {
		return nil, ErrInvalidSource
	}
	messages = append(messages, stripFrame(source[start:]))
	return messages, nil
}
func fromQuoted(line []byte) bool {
	for len(line) > 0 && line[0] == '>' {
		line = line[1:]
	}
	return bytes.HasPrefix(line, []byte("From "))
}
func unescapeMBOX(raw []byte) []byte {
	var out bytes.Buffer
	for len(raw) > 0 {
		end := bytes.IndexByte(raw, '\n')
		if end < 0 {
			end = len(raw)
		} else {
			end++
		}
		line := raw[:end]
		if len(line) > 0 && line[0] == '>' && fromQuoted(line) {
			line = line[1:]
		}
		out.Write(line)
		raw = raw[end:]
	}
	return out.Bytes()
}

// ExportMBOX emits the exact mboxrd framing consumed by Parse. Explicit binary
// or unterminated messages are refused rather than changing their body bytes.
func ExportMBOX(messages [][]byte) ([]byte, error) {
	if len(messages) == 0 {
		return nil, ErrInvalidSource
	}
	if len(messages) > MaxItems {
		return nil, ErrTooManyItems
	}
	var out bytes.Buffer
	for _, message := range messages {
		if len(message) > MaxMessageBytes {
			return nil, ErrSourceTooLarge
		}
		if hasBinaryMIME(message) {
			return nil, ErrMBOXFraming
		}
		raw := NormalizeRaw(message)
		if !bytes.HasSuffix(raw, []byte("\r\n")) {
			return nil, ErrMBOXFraming
		}
		item := validateItem(1, raw)
		if item.ErrorCode != "" {
			return nil, fmt.Errorf("%w: %s", ErrInvalidSource, item.ErrorCode)
		}
		out.WriteString("From MAILER-DAEMON Thu Jan  1 00:00:00 1970\r\n")
		for len(raw) > 0 {
			end := bytes.IndexByte(raw, '\n')
			if end < 0 {
				return nil, ErrMBOXFraming
			}
			line := raw[:end+1]
			if fromQuoted(line) {
				out.WriteByte('>')
			}
			out.Write(line)
			raw = raw[end+1:]
		}
		out.WriteString("\r\n")
		if out.Len() > MaxSourceBytes {
			return nil, ErrSourceTooLarge
		}
	}
	return out.Bytes(), nil
}
