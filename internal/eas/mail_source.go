package eas

import (
	"context"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// MailMessage is the per-UID snapshot the email sync diff runs on. Flags map
// from IMAP; \Answered and \Deleted are fingerprint inputs only because the
// implemented MS-ASEMAIL subset has no wire element for them.
type MailMessage struct {
	UID               uint32
	Subject           string
	From, To, Cc      []string // "Name <addr>" rendered addresses
	Date              time.Time
	Read, Flagged     bool
	Answered, Deleted bool
	Size              int64
}

// Fingerprint is the change detector for one message.
func (m MailMessage) Fingerprint() string {
	var b strings.Builder
	if m.Read {
		b.WriteByte('r')
	}
	if m.Flagged {
		b.WriteByte('f')
	}
	if m.Answered {
		b.WriteByte('a')
	}
	if m.Deleted {
		b.WriteByte('d')
	}
	b.WriteByte('|')
	b.WriteString(m.Subject)
	b.WriteByte('|')
	b.WriteString(m.Date.UTC().Format(time.RFC3339))
	return b.String()
}

// MailSource is the email half of Sync. Implementations serve exactly one
// mailbox: the address comes from the authenticated session, never from the
// request. Every method must bound its IMAP traffic.
type MailSource interface {
	// UIDValidity returns the folder's UIDVALIDITY generation.
	UIDValidity(ctx context.Context, folder string) (uint32, error)
	// ListWindow returns at most limit newest messages, newest first.
	ListWindow(ctx context.Context, folder string, limit int) ([]MailMessage, int, error)
	// Details returns a truncated plain-text body preview plus the To/Cc
	// header addresses for one UID. Callers budget these per-message fetches;
	// implementations must never batch more than the requested UID.
	Details(ctx context.Context, folder string, uid uint32, maxRunes int) (preview string, to, cc []string, err error)
	// MarkRead stores or clears \\Seen for the given UIDs only.
	MarkRead(ctx context.Context, folder string, uids []uint32, read bool) error
	// DeleteUIDs expunges exactly the given UIDs, never the whole folder.
	DeleteUIDs(ctx context.Context, folder string, uids []uint32) error
	// Close releases the underlying connection.
	Close() error
}

// MailSourceFactory builds a MailSource for one request. The token hash comes
// from the authenticated session context. A nil result means email collections
// answer with the documented server-error status for this request.
type MailSourceFactory func(ctx context.Context, sess Session) (MailSource, error)

// MailSourceFromDialer builds the production factory over the native webmail
// IMAP dialer and credential vault, mirroring EmailFoldersFrom. A vault miss
// or dial failure yields a nil source, not an error: the transport stays
// usable with collab-only collections.
func MailSourceFromDialer(dialer webmail.Dialer, vault webmail.CredentialVault) MailSourceFactory {
	return func(ctx context.Context, sess Session) (MailSource, error) {
		if dialer == nil || vault == nil || sess.TokenHash == "" || sess.Address == "" {
			return nil, nil
		}
		password, ok := vault.Get(sess.TokenHash)
		if !ok || password == "" {
			return nil, nil
		}
		client, err := dialer.Dial(ctx, sess.Address, password)
		if err != nil {
			return nil, nil
		}
		return &webmailMailSource{client: client}, nil
	}
}

// webmailMailSource adapts the webmail IMAP client to MailSource.
type webmailMailSource struct{ client webmail.Client }

func (s *webmailMailSource) UIDValidity(ctx context.Context, folder string) (uint32, error) {
	return s.client.FolderUIDValidity(ctx, folder)
}

func (s *webmailMailSource) ListWindow(ctx context.Context, folder string, limit int) ([]MailMessage, int, error) {
	result, err := s.client.ListMessages(ctx, folder, webmail.ListOpts{Limit: limit})
	if err != nil {
		return nil, 0, err
	}
	out := make([]MailMessage, 0, len(result.Messages))
	for _, m := range result.Messages {
		out = append(out, mailMessageFromSummary(m))
	}
	return out, result.Total, nil
}

func (s *webmailMailSource) Details(ctx context.Context, folder string, uid uint32, maxRunes int) (string, []string, []string, error) {
	msg, err := s.client.GetMessage(ctx, folder, uid)
	if err != nil {
		return "", nil, nil, err
	}
	to := make([]string, 0, len(msg.To))
	for _, a := range msg.To {
		to = append(to, renderAddress(a))
	}
	cc := make([]string, 0, len(msg.Cc))
	for _, a := range msg.Cc {
		cc = append(cc, renderAddress(a))
	}
	return truncateRunes(msg.Text, maxRunes), to, cc, nil
}

func (s *webmailMailSource) MarkRead(ctx context.Context, folder string, uids []uint32, read bool) error {
	if read {
		return s.client.SetFlags(ctx, folder, uids, []string{"\\Seen"}, nil)
	}
	return s.client.SetFlags(ctx, folder, uids, nil, []string{"\\Seen"})
}

func (s *webmailMailSource) DeleteUIDs(ctx context.Context, folder string, uids []uint32) error {
	// UID EXPUNGE of exactly these UIDs; falling back to a whole-folder
	// EXPUNGE would destroy other clients' pending deletes (WEBMAIL-003 rule).
	return s.client.DeleteMessages(ctx, folder, uids, true)
}

func (s *webmailMailSource) Close() error { return s.client.Close() }

func mailMessageFromSummary(m webmail.MessageSummary) MailMessage {
	msg := MailMessage{UID: m.UID, Subject: m.Subject, Date: m.Date, Size: m.Size}
	for _, a := range m.From {
		msg.From = append(msg.From, renderAddress(a))
	}
	for _, fl := range m.Flags {
		switch fl {
		case "\\Seen":
			msg.Read = true
		case "\\Flagged":
			msg.Flagged = true
		case "\\Answered":
			msg.Answered = true
		case "\\Deleted":
			msg.Deleted = true
		}
	}
	return msg
}

func renderAddress(a webmail.Address) string {
	if a.Name == "" {
		return a.Email
	}
	return a.Name + " <" + a.Email + ">"
}

// truncateRunes bounds a preview by runes, never splitting a code point.
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) > limit {
		return string(r[:limit])
	}
	return string(r)
}

// MailPageSource exposes cursor pagination without breaking legacy adapters.
type MailPageSource interface {
	ListPage(context.Context, string, uint32, int) ([]MailMessage, uint32, error)
}

// MailContentSource supplies complete metadata for attachment discovery.
type MailContentSource interface {
	FullMessage(context.Context, string, uint32) (*webmail.Message, error)
}

func (s *webmailMailSource) ListPage(ctx context.Context, folder string, cursor uint32, limit int) ([]MailMessage, uint32, error) {
	result, err := s.client.ListMessages(ctx, folder, webmail.ListOpts{Limit: limit, CursorUID: cursor})
	if err != nil {
		return nil, 0, err
	}
	out := make([]MailMessage, 0, len(result.Messages))
	for _, m := range result.Messages {
		out = append(out, mailMessageFromSummary(m))
	}
	return out, result.NextCursor, nil
}
func (s *webmailMailSource) FullMessage(ctx context.Context, folder string, uid uint32) (*webmail.Message, error) {
	return s.client.GetMessage(ctx, folder, uid)
}
