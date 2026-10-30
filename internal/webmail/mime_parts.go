package webmail

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
)

const (
	// MaxAttachmentBytes bounds decoded downloads without silently truncating them.
	MaxAttachmentBytes  = 16 << 20
	maxDecodedMIMEBytes = 64 << 20
	maxMIMEParts        = 256
	maxMIMEDepth        = 16
)

var (
	// ErrAttachmentTooLarge reports an explicit decoded download size limit.
	ErrAttachmentTooLarge = errors.New("attachment exceeds download limit")
	// ErrInvalidMIME reports malformed or unsupported MIME content.
	ErrInvalidMIME = errors.New("invalid MIME message")
)

type mimeLeaf struct {
	id, filename, contentType string
	contentID                 string
	inline                    bool
	attachmentContentType     string
	attachment                bool
	body                      []byte
}

// visitMIME walks nested multipart structures using the same part identifiers
// for the message view, attachment download, and scheduled-draft reconstruction.
func visitMIME(raw []byte, visit func(mimeLeaf) error) error {
	return visitMIMELimited(raw, 0, visit)
}

// visitMIMELimited walks MIME with a per-leaf decoded-byte cap; 0 means the
// metadata-walk default (maxDecodedMIMEBytes).
func visitMIMELimited(raw []byte, leafLimit int64, visit func(mimeLeaf) error) error {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: headers", ErrInvalidMIME)
	}
	count := 0
	return walkMIME(textproto.MIMEHeader(msg.Header), msg.Body, "", 0, &count, leafLimit, visit)
}

func walkMIME(header textproto.MIMEHeader, body io.Reader, id string, depth int, count *int, leafLimit int64, visit func(mimeLeaf) error) error {
	*count++
	if depth > maxMIMEDepth || *count > maxMIMEParts {
		return fmt.Errorf("%w: structure limit", ErrInvalidMIME)
	}
	ct := header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	media, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return fmt.Errorf("%w: content type", ErrInvalidMIME)
	}
	decoded := body
	switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
	case "base64":
		decoded = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		decoded = quotedprintable.NewReader(body)
	case "", "7bit", "8bit", "binary":
	default:
		return fmt.Errorf("%w: transfer encoding", ErrInvalidMIME)
	}
	if strings.HasPrefix(media, "multipart/") {
		if params["boundary"] == "" {
			return fmt.Errorf("%w: boundary", ErrInvalidMIME)
		}
		reader := multipart.NewReader(decoded, params["boundary"])
		for n := 1; ; n++ {
			part, nextErr := reader.NextRawPart()
			if nextErr == io.EOF {
				return nil
			}
			if nextErr != nil {
				return fmt.Errorf("%w: multipart", ErrInvalidMIME)
			}
			partID := strconv.Itoa(n)
			if id != "" {
				partID = id + "." + partID
			}
			if err := walkMIME(part.Header, part, partID, depth+1, count, leafLimit, visit); err != nil {
				return err
			}
		}
	}
	if id == "" {
		id = "1"
	}
	filename := params["name"]
	disp, dispParams, err := mime.ParseMediaType(header.Get("Content-Disposition"))
	if err == nil && dispParams["filename"] != "" {
		filename = dispParams["filename"]
	}
	contentID := strings.Trim(header.Get("Content-ID"), "<> \t\r\n")
	// Leaf reads are bounded: metadata walks allow the full decoded bound,
	// attachment downloads pass a tighter per-part cap so an oversized part
	// fails with ErrAttachmentTooLarge without buffering past the limit.
	leafCap := int64(maxDecodedMIMEBytes)
	if leafLimit > 0 {
		leafCap = leafLimit
	}
	data, err := io.ReadAll(io.LimitReader(decoded, leafCap+1))
	if err != nil {
		return fmt.Errorf("%w: transfer data", ErrInvalidMIME)
	}
	if len(data) > int(leafCap) {
		return ErrAttachmentTooLarge
	}
	delete(params, "name")
	attachment := disp == "attachment" || filename != "" || (media != "text/plain" && media != "text/html")
	return visit(mimeLeaf{id: id, filename: filename, contentType: media, contentID: contentID, inline: disp == "inline", attachmentContentType: mime.FormatMediaType(media, params), attachment: attachment, body: data})
}

func parseMIMEBodyStrict(raw []byte) (text, html string, atts []AttachmentMeta, err error) {
	atts = []AttachmentMeta{}
	err = visitMIME(raw, func(part mimeLeaf) error {
		switch {
		case part.attachment:
			// Surfaced attachment list is capped (WEBMAIL-007); downloads of
			// later parts still work via their part IDs.
			if len(atts) < MaxAttachmentsPerMessage {
				atts = append(atts, AttachmentMeta{PartID: part.id, Filename: part.filename, ContentType: part.attachmentContentType, Inline: part.inline, Size: int64(len(part.body)), ContentID: part.contentID})
			}
		case part.contentType == "text/plain" && text == "":
			text = string(part.body)
		case part.contentType == "text/html" && html == "":
			html = string(part.body)
		}
		return nil
	})
	return
}

func parseMIMEBody(raw []byte) (text, html string, atts []AttachmentMeta) {
	text, html, atts, _ = parseMIMEBodyStrict(raw)
	return
}

func extractMIMEPart(raw []byte, partID string) (filename, contentType string, data []byte, err error) {
	found := false
	err = visitMIMELimited(raw, MaxAttachmentBytes, func(part mimeLeaf) error {
		if part.id != partID {
			return nil
		}
		if len(part.body) > MaxAttachmentBytes {
			return ErrAttachmentTooLarge
		}
		found = true
		filename, contentType, data = part.filename, part.attachmentContentType, part.body
		if filename == "" {
			filename = "attachment-" + partID
		}
		return nil
	})
	if err != nil {
		return "", "", nil, err
	}
	if !found {
		return "", "", nil, fmt.Errorf("attachment not found")
	}
	return
}
