package airetrieval

import (
	"context"
	"fmt"
	"strings"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// MailSource is the read-only view of one mailbox's live mail used to refresh
// the index and to re-verify that indexed messages still exist. It is never a
// bypass of authorization: callers pass it only for the principal's own mail.
type MailSource interface {
	// Folders lists searchable folder names.
	Folders(ctx context.Context) ([]string, error)
	// MessageUIDs returns the folder's UIDVALIDITY and live UIDs. A message
	// missing here is deleted or expunged and must drop out of the index.
	MessageUIDs(ctx context.Context, folder string) (uidValidity uint32, uids []uint32, err error)
	// MessageText returns the searchable plain text of one message.
	MessageText(ctx context.Context, folder string, uid uint32) (string, error)
}

// WebmailSource adapts a live webmail IMAP client to MailSource.
type WebmailSource struct{ Client webmail.Client }

// Folders lists selectable folders. Namespace parents are not mailboxes.
func (s WebmailSource) Folders(ctx context.Context) ([]string, error) {
	folders, err := s.Client.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(folders))
	for _, f := range folders {
		selectable := f.Name != ""
		for _, flag := range f.Flags {
			if strings.EqualFold(flag, `\Noselect`) || strings.EqualFold(flag, `\NonExistent`) {
				selectable = false
				break
			}
		}
		if selectable {
			names = append(names, f.Name)
		}
	}
	return names, nil
}

// MessageUIDs lists all UIDs of a folder and binds them to the UIDVALIDITY.
func (s WebmailSource) MessageUIDs(ctx context.Context, folder string) (uint32, []uint32, error) {
	validity, err := s.Client.FolderUIDValidity(ctx, folder)
	if err != nil {
		return 0, nil, err
	}
	var uids []uint32
	var cursor uint32
	for pages := 0; pages < 1000; pages++ {
		result, err := s.Client.ListMessages(ctx, folder, webmail.ListOpts{Limit: webmail.MaxPageSize, CursorUID: cursor})
		if err != nil {
			return 0, nil, err
		}
		for _, m := range result.Messages {
			uids = append(uids, m.UID)
		}
		if result.NextCursor == 0 {
			return validity, uids, nil
		}
		if result.NextCursor == cursor {
			return 0, nil, fmt.Errorf("AI source pagination did not advance")
		}
		cursor = result.NextCursor
	}
	return 0, nil, fmt.Errorf("AI source message budget exceeded")
}

// MessageText fetches one message body's plain text.
func (s WebmailSource) MessageText(ctx context.Context, folder string, uid uint32) (string, error) {
	message, err := s.Client.GetMessage(ctx, folder, uid)
	if err != nil {
		return "", err
	}
	return message.Text, nil
}

// MessageKey identifies one indexed message inside one mailbox.
type MessageKey struct {
	Folder      string
	UIDValidity uint32
	UID         uint32
}

func (k MessageKey) String() string {
	return fmt.Sprintf("%s\x1f%d\x1f%d", k.Folder, k.UIDValidity, k.UID)
}

// quoteForSource extracts the model-visible quote of a chunk. It satisfies the
// aianswer.Source quote rules: trimmed, newline-free and bounded.
func quoteForSource(content string) string {
	quote := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, strings.TrimSpace(content))
	if len(quote) > 500 {
		quote = strings.TrimSpace(quote[:500])
	}
	return quote
}
