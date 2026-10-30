package webmail

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
)

// OutgoingFromDraft FETCHes a draft UID and rebuilds an Outgoing suitable for
// NetSubmitter (including attachment bytes). Bcc is preserved from draft headers.
func OutgoingFromDraft(ctx context.Context, client Client, folder string, uid uint32) (Outgoing, error) {
	if client == nil || folder == "" || uid == 0 {
		return Outgoing{}, fmt.Errorf("invalid draft reference")
	}
	msg, err := client.GetMessage(ctx, folder, uid)
	if err != nil {
		return Outgoing{}, err
	}
	if msg.AttachmentsCapped {
		return Outgoing{}, fmt.Errorf("draft attachments exceed reconstruction limit")
	}
	out := Outgoing{
		To:      emailsOf(msg.To),
		Cc:      emailsOf(msg.Cc),
		Bcc:     emailsOf(msg.Bcc),
		Subject: msg.Subject,
		Text:    msg.Text,
		HTML:    msg.HTML,
	}
	if len(msg.From) > 0 {
		out.From = (&mail.Address{Name: msg.From[0].Name, Address: msg.From[0].Email}).String()
	}
	out.InReplyTo = msg.InReplyTo
	out.References = msg.InReplyTo
	for _, meta := range msg.Attachments {
		filename, ct, data, aerr := client.GetAttachment(ctx, folder, uid, meta.PartID)
		if aerr != nil {
			return Outgoing{}, aerr
		}
		if filename == "" {
			filename = meta.Filename
		}
		if ct == "" {
			ct = meta.ContentType
		}
		out.Attachments = append(out.Attachments, OutgoingAttachment{
			ContentID:   meta.ContentID,
			Inline:      meta.Inline,
			Filename:    filename,
			ContentType: ct,
			Content:     data,
		})
	}
	if len(out.To)+len(out.Cc)+len(out.Bcc) == 0 {
		return Outgoing{}, fmt.Errorf("draft has no recipients")
	}
	return out, nil
}

func emailsOf(addrs []Address) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, a := range addrs {
		email := strings.TrimSpace(strings.ToLower(a.Email))
		if email == "" || !strings.Contains(email, "@") {
			continue
		}
		if _, ok := seen[email]; ok {
			continue
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}
	return out
}
