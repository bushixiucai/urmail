// Package marketing runs tenant-qualified durable campaign delivery.
package marketing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

// ErrChannelUnavailable prevents unimplemented egress from falling back to mail.
var ErrChannelUnavailable = errors.New("dedicated marketing channel is unavailable")

// crlf guards header and envelope injection.
var crlf = string([]byte{0x0d, 0x0a})

// ErrPermanentBounce identifies a definitive SMTP recipient rejection.
var ErrPermanentBounce = errors.New("marketing recipient permanently refused")

// SMTPConfig is stored only as encrypted configuration by platform operators.
type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	// BounceAddress is the per-channel campaign RETURN-PATH (envelope MAIL
	// FROM). Optional; when set the gateway can route complaints to it.
	BounceAddress string `json:"bounce_address,omitempty"`
	Dedicated     bool   `json:"dedicated"`
	ImplicitTLS   bool   `json:"implicit_tls"`
	FeedbackKey   string `json:"feedback_key,omitempty"`
	SourceIP      string `json:"source_ip,omitempty"`
}

// Validate refuses ambiguous/shared egress and header injection.
func (c SMTPConfig) Validate(kind string) error {
	if kind != "dedicated_gateway" || !c.Dedicated || c.Host == "" || c.Port < 1 || c.Port > 65535 || strings.ContainsAny(c.Host, "\r\n /\\@") {
		return ErrChannelUnavailable
	}
	address, err := mail.ParseAddress(c.From)
	if err != nil || address.Address != c.From || strings.ContainsAny(c.From, "\r\n") {
		return ErrChannelUnavailable
	}
	if (c.Username == "") != (c.Password == "") {
		return ErrChannelUnavailable
	}
	if c.BounceAddress != "" {
		bounce, err := mail.ParseAddress(c.BounceAddress)
		if err != nil || bounce.Address != c.BounceAddress || strings.ContainsAny(c.BounceAddress, crlf) {
			return ErrChannelUnavailable
		}
	}
	return nil
}

// Delivery is an immutable recipient/content snapshot passed to one sender.
type Delivery struct {
	ID         int64
	TenantID   int64
	CampaignID int64
	Email      string
	Subject    string
	Body       string
	MessageID  string
	// EnvelopeFrom is the bounce-address RETURN-PATH; empty falls back to the
	// channel sender address in the client.
	EnvelopeFrom   string
	UnsubscribeURL string
	BeforeData     func(context.Context) error
}

// Sender reports uncertain outcomes explicitly after entering SMTP DATA.
type Sender func(context.Context, SMTPConfig, Delivery) error

// SendSMTP requires verified TLS, uses only the configured dedicated gateway,
// and never routes through a transactional sendmail command or fallback relay.
func SendSMTP(ctx context.Context, cfg SMTPConfig, d Delivery) error {
	return sendSMTP(ctx, cfg, d, nil)
}

func sendSMTP(ctx context.Context, cfg SMTPConfig, d Delivery, roots *x509.CertPool) error {
	if err := cfg.Validate("dedicated_gateway"); err != nil {
		return err
	}
	if d.Email == "" || strings.ContainsAny(d.Email+d.Subject+d.MessageID+d.UnsubscribeURL, "\r\n") {
		return errors.New("invalid marketing envelope")
	}
	address, err := mail.ParseAddress(d.Email)
	if err != nil || address.Address != d.Email {
		return errors.New("invalid marketing recipient")
	}
	unsubscribe, err := url.Parse(d.UnsubscribeURL)
	if err != nil || unsubscribe.Scheme != "https" || unsubscribe.Host == "" || unsubscribe.User != nil {
		return errors.New("invalid unsubscribe endpoint")
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return errors.New("marketing gateway connection failed")
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(30 * time.Second)
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return errors.New("marketing gateway deadline failed")
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: roots}
	if cfg.ImplicitTLS {
		secured := tls.Client(conn, tlsConfig)
		if err = secured.HandshakeContext(ctx); err != nil {
			return errors.New("marketing gateway TLS failed")
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return errors.New("marketing gateway greeting failed")
	}
	defer func() { _ = client.Close() }()
	if !cfg.ImplicitTLS {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return errors.New("marketing gateway requires STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return errors.New("marketing gateway STARTTLS failed")
		}
	}
	if cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return errors.New("marketing gateway authentication failed")
		}
	}
	envelope := cfg.From
	if d.EnvelopeFrom != "" {
		if _, err = mail.ParseAddress(d.EnvelopeFrom); err != nil || strings.ContainsAny(d.EnvelopeFrom, crlf) {
			return errors.New("invalid marketing bounce address")
		}
		envelope = d.EnvelopeFrom
	}
	if err = client.Mail(envelope); err != nil {
		return errors.New("marketing sender refused")
	}
	if err = client.Rcpt(d.Email); err != nil {
		var response *textproto.Error
		if errors.As(err, &response) && response.Code >= 500 && response.Code <= 599 {
			return ErrPermanentBounce
		}
		return errors.New("marketing recipient refused")
	}
	if d.BeforeData != nil {
		if err = d.BeforeData(ctx); err != nil {
			return fmt.Errorf("marketing authorization revoked before DATA: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("marketing DATA refused")
	}
	payload := fmt.Sprintf("From: <%s>\r\nTo: <%s>\r\nSubject: %s\r\nMessage-ID: <%s>\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nList-Unsubscribe: <%s>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n\r\n%s\r\n\r\n%s\r\n", cfg.From, d.Email, mime.QEncoding.Encode("UTF-8", d.Subject), d.MessageID, time.Now().UTC().Format(time.RFC1123Z), d.UnsubscribeURL, strings.ReplaceAll(strings.ReplaceAll(d.Body, "\r\n", "\n"), "\n", "\r\n"), d.UnsubscribeURL)
	if _, err = writer.Write([]byte(payload)); err != nil {
		return domainMail.MarkDeliveryUncertain(errors.New("marketing DATA transport uncertain"))
	}
	if err = writer.Close(); err != nil {
		return domainMail.MarkDeliveryUncertain(errors.New("marketing acceptance uncertain"))
	}
	// A QUIT failure cannot revoke a successful final DATA response.
	_ = client.Quit()
	return nil
}
