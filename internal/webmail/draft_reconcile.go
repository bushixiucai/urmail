package webmail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// DraftAppender writes already prepared bytes, so reconciliation uses the exact digest.
type DraftAppender interface {
	AppendPreparedDraft(context.Context, string, []byte) (DraftSaveResult, error)
	FindDraftCopy(context.Context, string, string, string) (DraftSaveResult, bool, error)
}

func (c *imapConn) AppendPreparedDraft(ctx context.Context, folder string, raw []byte) (DraftSaveResult, error) {
	validity, uid, err := c.appendLiteral(ctx, folder, []string{`\Draft`}, raw)
	return DraftSaveResult{Folder: folder, NameEnc: EncodeFolderName(folder), UID: uid, UIDValidity: validity}, err
}

// FindDraftCopy checks bytes as well as Message-ID. It never issues APPEND.
func (c *imapConn) FindDraftCopy(ctx context.Context, folder, id, digest string) (DraftSaveResult, bool, error) {
	if id == "" || len(digest) != 64 {
		return DraftSaveResult{}, false, errors.New("invalid draft reconciliation identity")
	}
	if err := c.selectFolder(ctx, folder); err != nil {
		return DraftSaveResult{}, false, err
	}
	validity := c.selectedUIDValidity
	uids, err := c.uidSearch(ctx, "HEADER Message-ID "+quoteIMAP(id))
	if err != nil {
		return DraftSaveResult{}, false, err
	}
	if len(uids) > 100 {
		return DraftSaveResult{}, false, errors.New("draft reconciliation exceeds candidate limit")
	}
	for _, uid := range uids {
		raw, current, readErr := c.GetRawMessage(ctx, folder, uid)
		if readErr != nil {
			return DraftSaveResult{}, false, readErr
		}
		if validity == 0 || current != validity {
			return DraftSaveResult{}, false, ErrDraftChanged
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) == digest {
			return DraftSaveResult{Folder: folder, NameEnc: EncodeFolderName(folder), UID: uid, UIDValidity: validity}, true, nil
		}
	}
	return DraftSaveResult{}, false, nil
}
