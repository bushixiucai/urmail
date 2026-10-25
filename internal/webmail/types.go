package webmail

import "time"

// ListOpts controls UID listing pagination. Limit is capped by the dialer.
type ListOpts struct {
	Limit  int
	Offset int
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
	Text        string           `json:"text,omitempty"`
	HTML        string           `json:"html,omitempty"`
	Attachments []AttachmentMeta `json:"attachments"`
}

// Outgoing is a basic SMTP submission payload.
type Outgoing struct {
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
