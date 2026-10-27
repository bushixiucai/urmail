package webmail

import (
	"context"
	"errors"
	"fmt"
)

// ErrMessageNotFound is returned when the requested UID does not exist in the
// selected folder.
var ErrMessageNotFound = errors.New("message not found")

// RawMessageAccessor is implemented by IMAP sessions that can export the full
// RFC822 payload of a stored message. Handlers type-assert this capability so
// test doubles of Client stay minimal.
type RawMessageAccessor interface {
	// GetRawMessage returns the exact stored RFC822 bytes plus the folder's
	// UIDVALIDITY. The payload is never re-encoded.
	GetRawMessage(ctx context.Context, folder string, uid uint32) (raw []byte, uidValidity uint32, err error)
}

// MessageImporter is implemented by IMAP sessions that can APPEND a complete
// RFC822 payload into an existing folder. Imported messages get no special
// flags beyond \Seen omitted; the client decides flag state.
type MessageImporter interface {
	// ImportMessage APPENDs raw into folder and returns the APPENDUID
	// identity. Raw must be a complete RFC822 message; the 32 MiB transport
	// ceiling of appendLiteral applies.
	ImportMessage(ctx context.Context, folder string, raw []byte) (DraftSaveResult, error)
}

// GetRawMessage FETCHes BODY.PEEK[] without parsing, so export is byte-faithful.
func (c *imapConn) GetRawMessage(ctx context.Context, folder string, uid uint32) ([]byte, uint32, error) {
	if uid == 0 {
		return nil, 0, fmt.Errorf("invalid uid")
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return nil, 0, err
	}
	raw, err := c.uidFetchBody(ctx, uid)
	if err != nil {
		return nil, 0, err
	}
	if len(raw) == 0 {
		return nil, 0, ErrMessageNotFound
	}
	return raw, c.selectedUIDValidity, nil
}

// ImportMessage APPENDs a complete RFC822 payload into folder.
func (c *imapConn) ImportMessage(ctx context.Context, folder string, raw []byte) (DraftSaveResult, error) {
	validity, uid, err := c.appendLiteral(ctx, folder, nil, raw)
	if err != nil {
		return DraftSaveResult{}, err
	}
	return DraftSaveResult{Folder: folder, NameEnc: EncodeFolderName(folder), UID: uid, UIDValidity: validity}, nil
}
