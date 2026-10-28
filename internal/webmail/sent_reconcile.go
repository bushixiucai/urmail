package webmail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/mail"
	"strings"
)

// SentCopyFinder reconciles APPEND acknowledgements using exact stored bytes.
type SentCopyFinder interface {
	FindSentCopy(context.Context, []byte) (DraftSaveResult, bool, error)
}

// FindSentCopy never treats a matching Message-ID alone as proof of a copy.
func (c *imapConn) FindSentCopy(ctx context.Context, raw []byte) (DraftSaveResult, bool, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return DraftSaveResult{}, false, err
	}
	id := msg.Header.Get("Message-ID")
	if id == "" || len(id) > 998 || strings.ContainsAny(id, "\r\n") {
		return DraftSaveResult{}, false, errors.New("invalid sent reconciliation identity")
	}
	folders, err := c.ListFolders(ctx)
	if err != nil {
		return DraftSaveResult{}, false, err
	}
	folder := ResolveSentFolder(folders)
	if folder == "" {
		return DraftSaveResult{}, false, nil
	}
	if err = c.selectFolder(ctx, folder); err != nil {
		return DraftSaveResult{}, false, err
	}
	validity := c.selectedUIDValidity
	uids, err := c.uidSearch(ctx, "HEADER Message-ID "+quoteIMAP(id))
	if err != nil {
		return DraftSaveResult{}, false, err
	}
	if len(uids) > 100 {
		return DraftSaveResult{}, false, errors.New("sent reconciliation exceeds candidate limit")
	}
	digest := sha256.Sum256(raw)
	for _, uid := range uids {
		found, current, readErr := c.GetRawMessage(ctx, folder, uid)
		if readErr != nil {
			return DraftSaveResult{}, false, readErr
		}
		if validity == 0 || current != validity {
			return DraftSaveResult{}, false, errors.New("sent mailbox identity changed")
		}
		if sha256.Sum256(found) == digest {
			return DraftSaveResult{Folder: folder, NameEnc: EncodeFolderName(folder), UID: uid, UIDValidity: validity}, true, nil
		}
	}
	return DraftSaveResult{}, false, nil
}
