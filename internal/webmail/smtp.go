package webmail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
)

// ErrSubmitFailed is returned when SMTP submission is rejected or unreachable.
var ErrSubmitFailed = errors.New("webmail smtp submit failed")

// ErrSubmitUncertain means DATA may have been accepted; callers must not retry blindly.
var ErrSubmitUncertain = errors.New("webmail smtp submission outcome uncertain")

// Submission is the outcome of one SMTP attempt. Raw is the exact DATA
// payload, without a Bcc header, so a later Sent APPEND cannot leak Bcc or
// require another delivery.
type Submission struct {
	Raw []byte
}

// Submitter sends mail via local submission with AUTH.
type Submitter interface {
	Send(ctx context.Context, address, password string, msg Outgoing) (Submission, error)
}

// NetSubmitter dials a loopback SMTP listener (typically 127.0.0.1:587).
type NetSubmitter struct {
	Addr        string
	Timeout     time.Duration
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
	// LoopbackPolicy scopes the STARTTLS certificate exemption (MAIL-006).
	// Nil keeps the deployment default: loopback Postfix accepts the local
	// certificate without public PKI, while every non-loopback submission
	// endpoint verifies the chain and name — see certs.LoopbackTLSConfig.
	LoopbackPolicy *certs.LoopbackCertificatePolicy
}

// Send authenticates as address and submits a simple MIME message.
func (s NetSubmitter) Send(ctx context.Context, address, password string, msg Outgoing) (Submission, error) {
	if address == "" || password == "" {
		return Submission{}, ErrSubmitFailed
	}
	addr := s.Addr
	if addr == "" {
		addr = "127.0.0.1:587"
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dial := s.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: timeout}).DialContext
	}
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()

	host, _, _ := net.SplitHostPort(addr)
	if host == "" {
		host = "127.0.0.1"
	}
	// NewClient name must stay localhost/127.0.0.1 so PlainAuth allows AUTH
	// after STARTTLS (or on test doubles without TLS). The SNI/verification
	// identity for STARTTLS is decided by certs.LoopbackTLSConfig below.
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, err)
	}
	defer func() { _ = client.Close() }()

	if ok, _ := client.Extension("STARTTLS"); ok {
		policy := s.LoopbackPolicy
		if policy == nil {
			// Deployment default: loopback submission accepts the local
			// Postfix certificate without public PKI; the exemption is scoped
			// by certs.LoopbackTLSConfig and never applies off-loopback.
			policy = &certs.LoopbackCertificatePolicy{ServerName: "box.urmail.cn", SkipVerification: true}
		}
		tlsCfg, tlsErr := certs.LoopbackTLSConfig(addr, *policy)
		if tlsErr != nil {
			return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, tlsErr)
		}
		if err = client.StartTLS(tlsCfg); err != nil {
			return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, err)
		}
	}
	if ok, _ := client.Extension("AUTH"); !ok {
		return Submission{}, fmt.Errorf("%w: AUTH not advertised", ErrSubmitFailed)
	}
	if err = client.Auth(smtp.PlainAuth("", address, password, host)); err != nil {
		return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, err)
	}
	from := msg.From
	if from == "" {
		from = address
	}
	envelope, parseErr := mail.ParseAddress(from)
	if parseErr != nil || strings.ContainsAny(envelope.Address, "\r\n") {
		return Submission{}, fmt.Errorf("%w: invalid sender", ErrSubmitFailed)
	}
	if err = client.Mail(envelope.Address); err != nil {
		return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, err)
	}
	rcpts := uniqueAddrs(append(append(append([]string{}, msg.To...), msg.Cc...), msg.Bcc...))
	if len(rcpts) == 0 {
		return Submission{}, fmt.Errorf("%w: no recipients", ErrSubmitFailed)
	}
	for _, rcpt := range rcpts {
		if err = client.Rcpt(rcpt); err != nil {
			return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, err)
	}
	payload := BuildMIME(from, msg) // SMTP: never emit Bcc headers
	if _, err = io.WriteString(w, payload); err != nil {
		// Do not Close the DATA writer on a partial write: that could submit a
		// truncated message. The connection is closed by the outer defer.
		return Submission{}, fmt.Errorf("%w: %w", ErrSubmitUncertain, err)
	}
	if err = w.Close(); err != nil {
		var reply *textproto.Error
		if errors.As(err, &reply) && reply.Code >= 400 && reply.Code <= 599 {
			return Submission{}, fmt.Errorf("%w: %w", ErrSubmitFailed, err)
		}
		return Submission{}, fmt.Errorf("%w: %w", ErrSubmitUncertain, err)
	}
	// A successful final DATA reply is the submission commit point. QUIT only
	// tears down the session; its failure must never cause another delivery.
	_ = client.Quit()
	return Submission{Raw: []byte(payload)}, nil
}

func uniqueAddrs(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, a := range in {
		a = strings.TrimSpace(strings.ToLower(a))
		if a == "" {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out
}

// BuildMIME renders an RFC822 message for SMTP DATA (no Bcc header).
func BuildMIME(from string, msg Outgoing) string {
	return buildMIME(from, msg, false)
}

// BuildDraftMIME is BuildMIME plus a Bcc header so drafts can resume recipients.
func BuildDraftMIME(from string, msg Outgoing) string {
	if msg.From != "" {
		from = msg.From
	}
	return buildMIME(from, msg, true)
}

func buildMIME(from string, msg Outgoing, includeBcc bool) string {
	var b strings.Builder
	writeHeader := func(k, v string) {
		v = strings.ReplaceAll(v, "\r", "")
		v = strings.ReplaceAll(v, "\n", "")
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	writeHeader("From", from)
	if len(msg.To) > 0 {
		writeHeader("To", strings.Join(msg.To, ", "))
	}
	if len(msg.Cc) > 0 {
		writeHeader("Cc", strings.Join(msg.Cc, ", "))
	}
	if includeBcc && len(msg.Bcc) > 0 {
		writeHeader("Bcc", strings.Join(msg.Bcc, ", "))
	}
	writeHeader("Subject", msg.Subject)
	id := strings.TrimSpace(msg.MessageID)
	if strings.HasPrefix(id, "<") && strings.HasSuffix(id, ">") && strings.Contains(id, "@") && !strings.ContainsAny(id, "\r\n\t ") {
		writeHeader("Message-ID", id)
	}
	if msg.InReplyTo != "" {
		writeHeader("In-Reply-To", msg.InReplyTo)
	}
	if msg.References != "" {
		writeHeader("References", msg.References)
	}
	writeHeader("MIME-Version", "1.0")

	hasAtt := len(msg.Attachments) > 0
	hasHTML := strings.TrimSpace(msg.HTML) != ""
	hasText := strings.TrimSpace(msg.Text) != ""
	if !hasText && !hasHTML {
		hasText = true
		msg.Text = ""
	}

	switch {
	case hasAtt:
		boundary := "urmail-mix-" + fmt.Sprintf("%d", time.Now().UnixNano())
		writeHeader("Content-Type", "multipart/mixed; boundary="+boundary)
		b.WriteString("\r\n")
		b.WriteString("--" + boundary + "\r\n")
		writeBodyPart(&b, hasText, hasHTML, msg)
		for _, att := range msg.Attachments {
			b.WriteString("--" + boundary + "\r\n")
			ct := att.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			name := att.Filename
			if name == "" {
				name = "attachment"
			}
			b.WriteString("Content-Type: " + ct + "; name=\"" + sanitizeFilename(name) + "\"\r\n")
			// RFC 2046 section 5.2.1 forbids base64 for message/rfc822.
			eml := strings.EqualFold(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]), "message/rfc822")
			if eml {
				b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
			} else {
				b.WriteString("Content-Transfer-Encoding: base64\r\n")
			}
			disposition := "attachment"
			if att.Inline {
				disposition = "inline"
			}
			b.WriteString("Content-Disposition: " + disposition + "; filename=\"" + sanitizeFilename(name) + "\"\r\n")
			if att.ContentID != "" {
				writeHeader("Content-ID", "<"+strings.Trim(att.ContentID, "<>")+">")
			}
			b.WriteString("\r\n")
			if eml {
				b.Write(att.Content)
			} else {
				b.WriteString(wrapBase64(base64.StdEncoding.EncodeToString(att.Content)))
			}
			b.WriteString("\r\n")
		}
		b.WriteString("--" + boundary + "--\r\n")
	case hasHTML && hasText:
		boundary := "urmail-alt-" + fmt.Sprintf("%d", time.Now().UnixNano())
		writeHeader("Content-Type", "multipart/alternative; boundary="+boundary)
		b.WriteString("\r\n")
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		b.WriteString(msg.Text)
		b.WriteString("\r\n--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		b.WriteString(msg.HTML)
		b.WriteString("\r\n--" + boundary + "--\r\n")
	case hasHTML:
		writeHeader("Content-Type", "text/html; charset=UTF-8")
		b.WriteString("\r\n")
		b.WriteString(msg.HTML)
	default:
		writeHeader("Content-Type", "text/plain; charset=UTF-8")
		b.WriteString("\r\n")
		b.WriteString(msg.Text)
	}
	return b.String()
}

func writeBodyPart(b *strings.Builder, hasText, hasHTML bool, msg Outgoing) {
	if hasHTML && hasText {
		boundary := "urmail-alt-inner"
		b.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n\r\n")
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		b.WriteString(msg.Text)
		b.WriteString("\r\n--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		b.WriteString(msg.HTML)
		b.WriteString("\r\n--" + boundary + "--\r\n")
		return
	}
	if hasHTML {
		b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		b.WriteString(msg.HTML)
		b.WriteString("\r\n")
		return
	}
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(msg.Text)
	b.WriteString("\r\n")
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, "_")
	name = strings.ReplaceAll(name, `"`, "_")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	return name
}

func wrapBase64(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}
