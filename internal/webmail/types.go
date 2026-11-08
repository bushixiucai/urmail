package webmail

import (
	"time"

	"github.com/bushixiucai/urmail/internal/mailauth"
)

// ListOpts controls UID listing pagination, optional SEARCH, server-side or
// fallback sorting, and optional thread grouping.
//
// Pagination contract (WEBMAIL-004): pages are anchored on a UID cursor.
// The server computes the folder's full ordered UID sequence (IMAP SORT when
// available, otherwise a bounded summary fetch sorted locally), positions the
// page immediately AFTER CursorUID within that sequence (CursorUID 0 = first
// page), and returns the next Limit UIDs. New arrivals can only insert UIDs
// before the cursor without shifting existing entries' relative order, so
// walking NextCursor never duplicates or skips existing messages. If the
// cursor UID has been expunged, positioning falls back to the sequence head;
// clients should treat that as a resync (a message may repeat once).
type ListOpts struct {
	Limit  int
	Offset int
	Query  string
	// SortBy is one of "", "date", "subject", "from", "size". Empty means the
	// historical arrival order (UID descending, newest first).
	SortBy string
	// SortDesc reverses the sort direction (default: descending).
	SortDesc bool
	// CursorUID anchors the page; see the type comment for the contract.
	// When non-zero it takes precedence over Offset.
	CursorUID uint32
	// Thread requests thread_id/thread_count on the returned summaries.
	Thread bool
	// Preview requests a one-line body snippet (preview) for every message
	// on the returned page. The snippets are downloaded with one batched
	// IMAP command per page and are byte-bounded; see preview_fetch.go.
	Preview bool
}

const (
	// DefaultPageSize is used when the client omits limit.
	DefaultPageSize = 50
	// MaxPageSize is the hard cap for list endpoints.
	MaxPageSize = 100
)

// ClampListOpts normalizes limit/offset.
func ClampListOpts(opts ListOpts) ListOpts {
	if opts.Limit <= 0 {
		opts.Limit = DefaultPageSize
	}
	if opts.Limit > MaxPageSize {
		opts.Limit = MaxPageSize
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	return opts
}

// Address is a display name + email from an IMAP envelope.
type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// MessageSummary is the envelope row for a folder listing.
type MessageSummary struct {
	// UIDValidity belongs to the source mailbox, including cross-folder results.
	UIDValidity    uint32    `json:"uid_validity"`
	HasAttachments bool      `json:"has_attachments"`
	UID            uint32    `json:"uid"`
	Subject        string    `json:"subject"`
	From           []Address `json:"from"`
	// Date is nil when the IMAP ENVELOPE date could not be parsed: the JSON
	// field is omitted so clients render an em dash instead of 1/1/1.
	Date  *time.Time `json:"date,omitempty"`
	Flags []string   `json:"flags"`
	Size  int64      `json:"size"`
	// MessageID / InReplyTo come from ENVELOPE and feed client-side threading.
	MessageID string `json:"message_id,omitempty"`
	InReplyTo string `json:"in_reply_to,omitempty"`
	// ThreadID is the root UID of the message's thread; ThreadCount is the
	// number of messages in that thread. Both are populated only when
	// ListOpts.Thread is set.
	ThreadID    uint32 `json:"thread_id,omitempty"`
	ThreadCount int    `json:"thread_count,omitempty"`
	// Folder / FolderEnc tag cross-folder search results; empty on plain
	// single-folder listings.
	Folder    string `json:"folder,omitempty"`
	FolderEnc string `json:"folder_enc,omitempty"`
	// Preview is a one-line plain-text snippet of the message body. It is
	// populated only when the caller asked for previews (ListOpts.Preview)
	// and omitted when the body has no readable text.
	Preview string `json:"preview,omitempty"`
}

// ListResult is a page of message summaries plus the folder UID total.
type ListResult struct {
	UIDValidity uint32           `json:"uid_validity"`
	Messages    []MessageSummary `json:"messages"`
	Total       int              `json:"total"`
	// NextCursor is the UID the client sends back as cursor for the next
	// page (0 when the sequence is exhausted).
	NextCursor uint32 `json:"next_cursor,omitempty"`
	// Sort is the effective sort key ("" for arrival order); ServerSorted
	// reports whether IMAP SORT produced the ordering.
	Sort         string `json:"sort,omitempty"`
	ServerSorted bool   `json:"server_sorted,omitempty"`
	// ThreadMode reports which threading path produced thread_id:
	// "" (not requested), "server" (IMAP THREAD=REFERENCES) or "client"
	// (grouping computed over the fetched summaries).
	ThreadMode string `json:"thread_mode,omitempty"`
	// ThreadScope reports how complete the stamped thread_count is:
	// "server" (counts come from the IMAP THREAD response and cover every
	// member of the thread), "folder" (the client path counted the whole
	// filtered set) or "bounded" (the client path could only count the
	// scanned prefix, so the counts are a lower bound). Empty when threads
	// were not requested.
	ThreadScope string `json:"thread_scope,omitempty"`
}

// AttachmentMeta describes a non-inline MIME part (download endpoint optional).
type AttachmentMeta struct {
	Inline      bool   `json:"inline,omitempty"`
	PartID      string `json:"part_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	// ContentID is the RFC 2392 Content-ID (without angle brackets) used to
	// rewrite cid: inline images to the attachment endpoint.
	ContentID string `json:"content_id,omitempty"`
}

// Message is a single message body view for the lean inbox.
type Message struct {
	Authentication mailauth.Result `json:"authentication"`
	MessageSummary
	UIDValidity uint32           `json:"uid_validity"`
	To          []Address        `json:"to,omitempty"`
	Cc          []Address        `json:"cc,omitempty"`
	ReplyTo     []Address        `json:"reply_to,omitempty"`
	Bcc         []Address        `json:"bcc,omitempty"` // present on drafts; omitted on normal mail
	Text        string           `json:"text,omitempty"`
	HTML        string           `json:"html,omitempty"`
	Attachments []AttachmentMeta `json:"attachments"`
	// Notes are render-time sanitization tokens (see sanitize_html.go);
	// the original message in IMAP is never modified.
	Notes []string `json:"notes,omitempty"`
	// AttachmentsCapped reports that more parts exist than
	// MaxAttachmentsPerMessage surfaced metadata entries.
	AttachmentsCapped bool `json:"attachments_capped,omitempty"`
}

// Outgoing is a basic SMTP submission payload.
type Outgoing struct {
	// MessageID is an optional stable tracking identity, never a delivery deduplication guarantee.
	MessageID string
	Date      time.Time
	// Raw freezes the exact SMTP DATA bytes before a durable submission intention.
	Raw         []byte
	InReplyTo   string
	References  string
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	Subject     string
	Text        string
	HTML        string
	Attachments []OutgoingAttachment
}

// OutgoingAttachment is a base64-decoded file part for SMTP.
type OutgoingAttachment struct {
	ContentID   string
	Inline      bool
	Filename    string
	ContentType string
	Content     []byte
}
