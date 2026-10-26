package webmail

import "time"

// ListOpts controls UID listing pagination and optional SEARCH.
// When Query is non-empty, ListMessages runs UID SEARCH with criteria from
// BuildSearchCriteria instead of ALL; total is the search hit count.
type ListOpts struct {
	Limit  int
	Offset int
	Query  string
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
	UID     uint32    `json:"uid"`
	Subject string    `json:"subject"`
	From    []Address `json:"from"`
	Date    time.Time `json:"date"`
	Flags   []string  `json:"flags"`
	Size    int64     `json:"size"`
}

// ListResult is a page of message summaries plus the folder UID total.
type ListResult struct {
	Messages []MessageSummary `json:"messages"`
	Total    int              `json:"total"`
}

// AttachmentMeta describes a non-inline MIME part (download endpoint optional).
type AttachmentMeta struct {
	PartID      string `json:"part_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// Message is a single message body view for the lean inbox.
type Message struct {
	MessageSummary
	UIDValidity uint32           `json:"uid_validity"`
	To          []Address        `json:"to,omitempty"`
	Cc          []Address        `json:"cc,omitempty"`
	Bcc         []Address        `json:"bcc,omitempty"` // present on drafts; omitted on normal mail
	Text        string           `json:"text,omitempty"`
	HTML        string           `json:"html,omitempty"`
	Attachments []AttachmentMeta `json:"attachments"`
}

// Outgoing is a basic SMTP submission payload.
type Outgoing struct {
	// MessageID is an optional stable tracking identity, never a delivery deduplication guarantee.
	MessageID   string
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
	Filename    string
	ContentType string
	Content     []byte
}
