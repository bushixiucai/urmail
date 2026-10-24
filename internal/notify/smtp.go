package notify

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
	"strconv"
	"strings"
	"time"
)

// ErrSubmissionUnknown prevents replay after ambiguous SMTP submission.
var ErrSubmissionUnknown = errors.New("notification SMTP submission requires reconciliation")

// SMTPConfig is encrypted as one platform-owned setting, never a public DTO.
type SMTPConfig struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	From        string `json:"from"`
	ImplicitTLS bool   `json:"implicit_tls"`
}

// Validate checks relay configuration without contacting the remote server.
func (c SMTPConfig) Validate() error {
	if c.Host == "" || c.Port < 1 || c.Port > 65535 || strings.ContainsAny(c.Host, "\r\n /\\@") || (c.Username == "") != (c.Password == "") {
		return errors.New("invalid notification channel")
	}
	addr, err := mail.ParseAddress(c.From)
	if err != nil || addr.Address != c.From || strings.ContainsAny(c.From, "\r\n") {
		return errors.New("invalid notification sender")
	}
	return nil
}

// Message contains the durable notification content submitted to a relay.
type Message struct {
	ID                int64
	To, Subject, Body string
}

// Sender submits one message through an explicitly configured relay.
type Sender func(context.Context, SMTPConfig, Message) error

// SendSMTP uses the managed transactional relay and verified TLS. Once DATA
// begins, transport failures retain an uncertain delivery instead of replaying.
func SendSMTP(ctx context.Context, cfg SMTPConfig, m Message) error {
	return sendSMTP(ctx, cfg, m, nil)
}

func sendSMTP(ctx context.Context, cfg SMTPConfig, m Message, roots *x509.CertPool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	addr, err := mail.ParseAddress(m.To)
	if err != nil || addr.Address != m.To || strings.ContainsAny(m.To+m.Subject, "\r\n") {
		return errors.New("invalid notification recipient")
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return errors.New("notification relay unavailable")
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return errors.New("notification deadline unavailable")
	}
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: roots}
	if cfg.ImplicitTLS {
		secured := tls.Client(conn, tlsConfig)
		if err = secured.HandshakeContext(ctx); err != nil {
			return errors.New("notification TLS unavailable")
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return errors.New("notification SMTP unavailable")
	}
	defer func() { _ = client.Close() }()
	if !cfg.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("notification TLS required")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return errors.New("notification TLS unavailable")
		}
	}
	if cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return errors.New("notification relay authentication failed")
		}
	}
	if err = client.Mail(cfg.From); err != nil {
		return errors.New("notification envelope refused")
	}
	if err = client.Rcpt(m.To); err != nil {
		return errors.New("notification recipient refused")
	}
	data, err := client.Data()
	if err != nil {
		return ErrSubmissionUnknown
	}
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <notification-%d@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\nAuto-Submitted: auto-generated\r\n\r\n%s\r\n", cfg.From, m.To, mime.QEncoding.Encode("utf-8", m.Subject), m.ID, strings.Split(cfg.From, "@")[1], body)
	if _, err = data.Write([]byte(raw)); err != nil {
		return ErrSubmissionUnknown
	}
	if err = data.Close(); err != nil {
		return ErrSubmissionUnknown
	}
	// A failed QUIT cannot undo a confirmed DATA acceptance.
	_ = client.Quit()
	return nil
}
