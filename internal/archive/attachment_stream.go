package archive

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"strings"
)

// SnapshotAttachment decodes one part into a private unlinked file. The caller
// provides an already verified raw snapshot and owns Close on the returned file.
func SnapshotAttachment(ctx context.Context, raw io.ReadSeeker, index int) (Attachment, *os.File, error) {
	var selected Attachment
	if index < 0 || index >= 200 {
		return selected, nil, errors.New("attachment not found")
	}
	if _, err := raw.Seek(0, io.SeekStart); err != nil {
		return selected, nil, err
	}
	message, err := mail.ReadMessage(contextReader{ctx, raw})
	if err != nil {
		return selected, nil, errors.New("invalid_message")
	}
	var snapshot *os.File
	found := errors.New("selected attachment prepared")
	parts, attachments := 0, 0
	var walk func(mail.Header, io.Reader, int) error
	walk = func(header mail.Header, reader io.Reader, depth int) error {
		parts++
		if depth > 12 || parts > 200 {
			return errors.New("mime_limits")
		}
		media, params, e := mime.ParseMediaType(header.Get("Content-Type"))
		if e != nil {
			media = "text/plain"
			params = map[string]string{}
		}
		switch strings.ToLower(header.Get("Content-Transfer-Encoding")) {
		case "base64":
			reader = base64.NewDecoder(base64.StdEncoding, reader)
		case "quoted-printable":
			reader = quotedprintable.NewReader(reader)
		}
		if strings.HasPrefix(media, "multipart/") {
			if params["boundary"] == "" {
				return errors.New("missing_boundary")
			}
			multi := multipart.NewReader(reader, params["boundary"])
			for {
				part, e := multi.NextPart()
				if errors.Is(e, io.EOF) {
					return nil
				}
				if e != nil {
					return errors.New("invalid_multipart")
				}
				e = walk(mail.Header(part.Header), part, depth+1)
				if e != nil {
					return e
				}
				_ = part.Close()
			}
		}
		disposition, attributes, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
		name := attributes["filename"]
		if name == "" {
			name = params["name"]
		}
		attachment := disposition == "attachment" || name != "" || !strings.HasPrefix(media, "text/")
		if attachment && attachments == index {
			snapshot, e = os.CreateTemp("", "urmail-archive-part-*")
			if e != nil {
				return e
			}
			if e = os.Remove(snapshot.Name()); e != nil {
				_ = snapshot.Close()
				snapshot = nil
				return e
			}
			digest := sha256.New()
			size, e := io.Copy(io.MultiWriter(snapshot, digest), io.LimitReader(contextReader{ctx, reader}, MaxMessage+1))
			if e != nil {
				return e
			}
			if size > MaxMessage {
				return errors.New("attachment_limit")
			}
			if _, e = snapshot.Seek(0, io.SeekStart); e != nil {
				return e
			}
			selected = Attachment{Index: index, Name: bounded(name, 512), MediaType: media, Size: size, SHA256: hex.EncodeToString(digest.Sum(nil))}
			return found
		}
		if attachment {
			attachments++
		}
		size, e := io.Copy(io.Discard, io.LimitReader(contextReader{ctx, reader}, MaxMessage+1))
		if e != nil {
			return errors.New("invalid_encoding")
		}
		if size > MaxMessage {
			return errors.New("part_limit")
		}
		return nil
	}
	err = walk(message.Header, message.Body, 0)
	if !errors.Is(err, found) {
		if snapshot != nil {
			_ = snapshot.Close()
		}
		if err == nil {
			err = errors.New("attachment not found")
		}
		return Attachment{}, nil, err
	}
	return selected, snapshot, nil
}
