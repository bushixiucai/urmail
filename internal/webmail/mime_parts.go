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
	attachment                bool
	body                      []byte
}

// visitMIME walks nested multipart structures using the same part identifiers
// for the message view, attachment download, and scheduled-draft reconstruction.
func visitMIME(raw []byte, visit func(mimeLeaf) error) error {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: headers", ErrInvalidMIME)
	}
	count := 0
	return walkMIME(textproto.MIMEHeader(msg.Header), msg.Body, "", 0, &count, visit)
}

func walkMIME(header textproto.MIMEHeader, body io.Reader, id string, depth int, count *int, visit func(mimeLeaf) error) error {
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
			if err := walkMIME(part.Header, part, partID, depth+1, count, visit); err != nil {
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
	data, err := io.ReadAll(io.LimitReader(decoded, maxDecodedMIMEBytes+1))
	if err != nil {
		return fmt.Errorf("%w: transfer data", ErrInvalidMIME)
	}
	if len(data) > maxDecodedMIMEBytes {
		return ErrAttachmentTooLarge
	}
	attachment := disp == "attachment" || filename != "" || (media != "text/plain" && media != "text/html")
	return visit(mimeLeaf{id: id, filename: filename, contentType: media, attachment: attachment, body: data})
}

func parseMIMEBodyStrict(raw []byte) (text, html string, atts []AttachmentMeta, err error) {
	atts = []AttachmentMeta{}
	err = visitMIME(raw, func(part mimeLeaf) error {
		switch {
		case part.attachment:
			atts = append(atts, AttachmentMeta{PartID: part.id, Filename: part.filename, ContentType: part.contentType, Size: int64(len(part.body))})
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
	err = visitMIME(raw, func(part mimeLeaf) error {
		if part.id != partID {
			return nil
		}
		if len(part.body) > MaxAttachmentBytes {
			return ErrAttachmentTooLarge
		}
		found = true
		filename, contentType, data = part.filename, part.contentType, part.body
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
