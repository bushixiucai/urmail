package webmail

import (
	"context"
	"errors"
	"strings"
)

const (
	// SentFolderName is created when the server has no SPECIAL-USE Sent mailbox.
	SentFolderName = "Sent"
	// TrashFolderName is created when the server has no SPECIAL-USE Trash mailbox.
	TrashFolderName = "Trash"
)

// ErrSentCopyPending means SMTP already accepted the message and the Sent
// APPEND did not finish. Callers must not submit the message again.
var ErrSentCopyPending = errors.New("message accepted but sent copy pending")

// ResolveSentFolder picks SPECIAL-USE \Sent, else a folder named Sent.
func ResolveSentFolder(folders []Folder) string {
	return resolveSpecial(folders, `\Sent`, SentFolderName)
}

// ResolveTrashFolder picks SPECIAL-USE \Trash, else a folder named Trash.
func ResolveTrashFolder(folders []Folder) string {
	return resolveSpecial(folders, `\Trash`, TrashFolderName)
}

func resolveSpecial(folders []Folder, flag, fallback string) string {
	for _, folder := range folders {
		if folderHasFlag(folder.Flags, flag) {
			return folder.Name
		}
	}
	for _, folder := range folders {
		if strings.EqualFold(strings.TrimSpace(folder.Name), fallback) {
			return folder.Name
		}
	}
	return ""
}

// EnsureSentFolder resolves or creates the sent mailbox.
func (c *imapConn) EnsureSentFolder(ctx context.Context) (string, error) {
	return c.ensureSpecialFolder(ctx, ResolveSentFolder, SentFolderName)
}

// EnsureTrashFolder resolves or creates the trash mailbox.
func (c *imapConn) EnsureTrashFolder(ctx context.Context) (string, error) {
	return c.ensureSpecialFolder(ctx, ResolveTrashFolder, TrashFolderName)
}

func (c *imapConn) ensureSpecialFolder(ctx context.Context, resolve func([]Folder) string, name string) (string, error) {
	folders, err := c.ListFolders(ctx)
	if err != nil {
		return "", err
	}
	if found := resolve(folders); found != "" {
		return found, nil
	}
	if err = c.createMailbox(ctx, name); err != nil {
		return "", err
	}
	return name, nil
}

// SaveSentCopy APPENDs the exact SMTP payload, which has no Bcc header.
// A failure after SMTP acceptance is ErrSentCopyPending, never a reason to resend.
func (c *imapConn) SaveSentCopy(ctx context.Context, raw []byte) (DraftSaveResult, error) {
	folder, err := c.EnsureSentFolder(ctx)
	if err != nil {
		return DraftSaveResult{}, errors.Join(ErrSentCopyPending, err)
	}
	validity, uid, err := c.appendLiteral(ctx, folder, []string{`\Seen`}, raw)
	if err != nil {
		return DraftSaveResult{}, errors.Join(ErrSentCopyPending, err)
	}
	return DraftSaveResult{Folder: folder, NameEnc: EncodeFolderName(folder), UID: uid, UIDValidity: validity}, nil
}

// MoveToTrash moves the selected UIDs into Trash. It does not EXPUNGE.
func (c *imapConn) MoveToTrash(ctx context.Context, folder string, uids []uint32) (string, error) {
	dest, err := c.EnsureTrashFolder(ctx)
	if err != nil {
		return "", err
	}
	if err = c.MoveMessages(ctx, folder, uids, dest); err != nil {
		return dest, err
	}
	return dest, nil
}
