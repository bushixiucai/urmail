package eas

import (
	"context"
	"errors"
	"strings"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// EmailFoldersFrom builds an EmailFolderLister over the native webmail IMAP
// dialer and credential vault, mirroring how other mailbox endpoints resolve
// the IMAP secret. A vault miss yields no email folders (the bearer is valid
// but IMAP cannot be dialed), which surfaces as a collab-only hierarchy.
func EmailFoldersFrom(dialer webmail.Dialer, vault webmail.CredentialVault) EmailFolderLister {
	return func(ctx context.Context, sess Session, tokenHash string) ([]Folder, error) {
		if dialer == nil || vault == nil || tokenHash == "" {
			return nil, errors.New("eas: mailbox credentials unavailable")
		}
		password, ok := vault.Get(tokenHash)
		if !ok || password == "" {
			return nil, errors.New("eas: mailbox credentials unavailable")
		}
		client, err := dialer.Dial(ctx, sess.Address, password)
		if err != nil {
			return nil, err
		}
		defer func() { _ = client.Close() }()
		folders, err := client.ListFolders(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]Folder, 0, len(folders))
		for _, f := range folders {
			name := strings.TrimSpace(f.Name)
			if name == "" {
				continue
			}
			out = append(out, Folder{
				ID:          "e:" + name,
				DisplayName: name,
				Type:        emailFolderType(name, f.Flags),
				Class:       "Email",
			})
		}
		return out, nil
	}
}

// emailFolderType maps an IMAP mailbox to the EAS folder Type using
// SPECIAL-USE flags first, then conventional names.
func emailFolderType(name string, flags []string) int {
	for _, fl := range flags {
		switch strings.ToLower(fl) {
		case "\\sent":
			return folderTypeSent
		case "\\drafts":
			return folderTypeDrafts
		case "\\trash":
			return folderTypeDeleted
		case "\\junk":
			return folderTypeUserCreatedEmail
		}
	}
	switch strings.ToLower(name) {
	case "inbox":
		return folderTypeInbox
	case "drafts":
		return folderTypeDrafts
	case "trash", "deleted messages":
		return folderTypeDeleted
	case "sent", "sent messages":
		return folderTypeSent
	case "outbox":
		return folderTypeOutbox
	default:
		return folderTypeUserCreatedEmail
	}
}
