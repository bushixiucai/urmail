package airetrieval

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/net/html"

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
type WebmailSource struct {
	Client           webmail.Client
	IndexAttachments bool
}

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

// ExtractHTMLText extracts clean plain text from HTML, stripping scripts, styles and tags.
func ExtractHTMLText(rawHTML string) string {
	if strings.TrimSpace(rawHTML) == "" {
		return ""
	}
	tokenizer := html.NewTokenizer(strings.NewReader(rawHTML))
	var out strings.Builder
	skip := 0
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.StartTagToken || kind == html.EndTagToken {
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if name == "script" || name == "style" {
				if kind == html.StartTagToken {
					skip++
				} else if skip > 0 {
					skip--
				}
			} else if kind == html.StartTagToken && (name == "p" || name == "br" || name == "div" || name == "tr" || name == "li" || name == "h1" || name == "h2" || name == "h3") {
				out.WriteString("\n")
			}
		}
		if kind == html.TextToken && skip == 0 {
			out.WriteString(tokenizer.Token().Data)
		}
	}
	lines := strings.Split(out.String(), "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return strings.Join(cleaned, "\n")
}

// MessageText fetches one message body's plain text, normalizing HTML when Text is empty,
// and indexing Subject and From sender as searchable content.
func (s WebmailSource) MessageText(ctx context.Context, folder string, uid uint32) (string, error) {
	message, err := s.Client.GetMessage(ctx, folder, uid)
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(message.Text)
	if body == "" && strings.TrimSpace(message.HTML) != "" {
		body = ExtractHTMLText(message.HTML)
	}

	var headerParts []string
	if strings.TrimSpace(message.Subject) != "" {
		headerParts = append(headerParts, "Subject: "+strings.TrimSpace(message.Subject))
	}
	if len(message.From) > 0 {
		var froms []string
		for _, f := range message.From {
			if f.Name != "" && f.Email != "" {
				froms = append(froms, fmt.Sprintf("%s <%s>", f.Name, f.Email))
			} else if f.Email != "" {
				froms = append(froms, f.Email)
			} else if f.Name != "" {
				froms = append(froms, f.Name)
			}
		}
		if len(froms) > 0 {
			headerParts = append(headerParts, "From: "+strings.Join(froms, ", "))
		}
	}

	// D11 / A11 Attachment indexing:
	// Attachments default to unindexed. Only parsed when specifically opted-in.
	if s.IndexAttachments && len(message.Attachments) > 0 {
		var atts []string
		for _, att := range message.Attachments {
			if att.Filename != "" {
				atts = append(atts, fmt.Sprintf("[Attachment: %s (%s)]", att.Filename, att.ContentType))
			}
		}
		if len(atts) > 0 {
			headerParts = append(headerParts, strings.Join(atts, "\n"))
		}
	}

	header := strings.Join(headerParts, "\n")
	if header != "" && body != "" {
		return header + "\n\n" + body, nil
	}
	if header != "" {
		return header, nil
	}
	return body, nil
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
