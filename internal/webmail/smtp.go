package webmail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// ErrSubmitFailed is returned when SMTP submission is rejected or unreachable.
var ErrSubmitFailed = errors.New("webmail smtp submit failed")

// Submitter sends mail via local submission with AUTH.
type Submitter interface {
	Send(ctx context.Context, address, password string, msg Outgoing) error
}

// NetSubmitter dials a loopback SMTP listener (typically 127.0.0.1:587).
type NetSubmitter struct {
	Addr        string
	Timeout     time.Duration
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

// Send authenticates as address and submits a simple MIME message.
func (s NetSubmitter) Send(ctx context.Context, address, password string, msg Outgoing) error {
	if address == "" || password == "" {
		return ErrSubmitFailed
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
		return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	host, _, _ := net.SplitHostPort(addr)
	if host == "" {
		host = "127.0.0.1"
	}
	// NewClient name must stay localhost/127.0.0.1 so PlainAuth allows AUTH after STARTTLS
	// (or on test doubles without TLS). SNI uses the real mail hostname separately.
	tlsServerName := host
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		tlsServerName = "box.urmail.cn"
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	defer func() { _ = client.Close() }()

	if ok, _ := client.Extension("STARTTLS"); ok {
		// Loopback submission: accept the local Postfix cert without public PKI.
		tlsCfg := &tls.Config{ServerName: tlsServerName, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // intentional for 127.0.0.1 submission
		if err = client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
		}
	}
	if ok, _ := client.Extension("AUTH"); !ok {
		return fmt.Errorf("%w: AUTH not advertised", ErrSubmitFailed)
	}
	if err = client.Auth(smtp.PlainAuth("", address, password, host)); err != nil {
		return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	from := msg.From
	if from == "" {
		from = address
	}
	if err = client.Mail(from); err != nil {
		return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	rcpts := uniqueAddrs(append(append(append([]string{}, msg.To...), msg.Cc...), msg.Bcc...))
	if len(rcpts) == 0 {
		return fmt.Errorf("%w: no recipients", ErrSubmitFailed)
	}
	for _, rcpt := range rcpts {
		if err = client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	payload := buildMIME(from, msg)
	if _, err = io.WriteString(w, payload); err != nil {
		_ = w.Close()
		return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	if err = client.Quit(); err != nil {
		return fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	return nil
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

func buildMIME(from string, msg Outgoing) string {
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
	writeHeader("Subject", msg.Subject)
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
			b.WriteString("Content-Transfer-Encoding: base64\r\n")
			b.WriteString("Content-Disposition: attachment; filename=\"" + sanitizeFilename(name) + "\"\r\n\r\n")
			b.WriteString(wrapBase64(base64.StdEncoding.EncodeToString(att.Content)))
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
