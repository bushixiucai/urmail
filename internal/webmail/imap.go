// Package webmail implements the native webmail IMAP proxy used by urmaild.
// Browser clients never speak IMAP; handlers dial Dovecot on the mailbox
// principal's behalf after a password-verified short-lived token is issued.
package webmail

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
)

// ErrDialFailed is returned when the IMAP endpoint cannot be reached or
// authentication is rejected by the server.
var ErrDialFailed = errors.New("webmail imap dial failed")

// Folder is one mailbox name returned by IMAP LIST.
type Folder struct {
	Name      string   `json:"name"`
	NameEnc   string   `json:"name_enc,omitempty"`
	Delimiter string   `json:"delimiter"`
	Flags     []string `json:"flags"`
}

// Client is a short-lived IMAP session used by handlers.
type Client interface {
	ListFolders(ctx context.Context) ([]Folder, error)
	ListMessages(ctx context.Context, folder string, opts ListOpts) (ListResult, error)
	GetMessage(ctx context.Context, folder string, uid uint32) (*Message, error)
	GetAttachment(ctx context.Context, folder string, uid uint32, partID string) (filename, contentType string, data []byte, err error)
	SetFlags(ctx context.Context, folder string, uids []uint32, add, remove []string) error
	MoveMessages(ctx context.Context, folder string, uids []uint32, dest string) error
	DeleteMessages(ctx context.Context, folder string, uids []uint32, expunge bool) error
	// EnsureDraftsFolder resolves SPECIAL-USE \Drafts (or name Drafts), creating
	// DraftsFolderName when missing. Returns the IMAP mailbox name.
	EnsureDraftsFolder(ctx context.Context) (string, error)
	// SaveDraft APPENDs MIME with \Draft into the drafts folder. When replaceUID
	// is non-zero, the previous UID is deleted+expunged after a successful APPEND
	// (append-new-then-delete-old). A successful result always includes verified
	// UIDPLUS identity; CleanupPending reports a failed old-draft removal.
	SaveDraft(ctx context.Context, from string, msg Outgoing, replaceUID, replaceUIDValidity uint32) (DraftSaveResult, error)
	// FolderUIDValidity binds subsequent reads to the current mailbox identity.
	FolderUIDValidity(ctx context.Context, folder string) (uint32, error)
	// SaveSentCopy stores the SMTP payload in Sent. The payload must not contain a Bcc header.
	SaveSentCopy(ctx context.Context, raw []byte) (DraftSaveResult, error)
	// MoveToTrash moves UIDs into Trash without expunging them.
	MoveToTrash(ctx context.Context, folder string, uids []uint32) (string, error)
	Close() error
}

// Dialer opens an authenticated IMAP session for a mailbox identity.
type Dialer interface {
	Dial(ctx context.Context, address, password string) (Client, error)
}

// NetDialer dials a loopback IMAP listener with LOGIN + LIST.
type NetDialer struct {
	Addr    string
	Timeout time.Duration
	// TLS wraps the TCP connection (IMAPS). When nil, TLS is used automatically
	// for :993. ServerName defaults to the dial host; set InsecureSkipVerify for
	// loopback Dovecot with a hostname cert (box.urmail.cn).
	TLS         *tls.Config
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
	// LoopbackPolicy scopes the loopback certificate exemption (MAIL-006).
	// Nil keeps the deployment default: loopback destinations are exempt from
	// verification (SNI box.urmail.cn), every other destination verifies the
	// chain and name — see certs.LoopbackTLSConfig.
	LoopbackPolicy *certs.LoopbackCertificatePolicy
}

// Dial authenticates against Addr and returns a Client that owns the connection.
func (d NetDialer) Dial(ctx context.Context, address, password string) (Client, error) {
	if address == "" || password == "" {
		return nil, ErrDialFailed
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dial := d.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: timeout}).DialContext
	}
	raw, err := dial(ctx, "tcp", d.Addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDialFailed, err)
	}
	conn := raw
	useTLS := d.TLS != nil || strings.HasSuffix(d.Addr, ":993")
	if useTLS {
		var cfg *tls.Config
		cfg = d.TLS
		if cfg == nil {
			policy := d.LoopbackPolicy
			if policy == nil {
				// Deployment default: the webmail process talks to the local
				// Dovecot over 127.0.0.1, where the dial address cannot match
				// the public hostname certificate. The exemption is scoped by
				// certs.LoopbackTLSConfig and never applies off-loopback.
				policy = &certs.LoopbackCertificatePolicy{ServerName: "box.urmail.cn", SkipVerification: true}
			}
			var cfgErr error
			cfg, cfgErr = certs.LoopbackTLSConfig(d.Addr, *policy)
			if cfgErr != nil {
				return nil, fmt.Errorf("%w: %w", ErrDialFailed, cfgErr)
			}
		}
		tlsConn := tls.Client(raw, cfg)
		handshakeCtx, cancelHandshake := context.WithTimeout(ctx, timeout)
		err = tlsConn.HandshakeContext(handshakeCtx)
		cancelHandshake()
		if err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("%w: %w", ErrDialFailed, err)
		}
		conn = tlsConn
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c := &imapConn{conn: conn, r: bufio.NewReader(conn), tag: 0, timeout: timeout}
	finishGreeting := c.guardIO(ctx)
	err = c.readGreeting()
	finishGreeting()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrDialFailed, err)
	}
	if err = c.command(ctx, "LOGIN", quoteIMAP(address), quoteIMAP(password)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrDialFailed, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

type imapConn struct {
	conn                net.Conn
	r                   *bufio.Reader
	tag                 int
	timeout             time.Duration
	validities          map[string]uint32
	selectedUIDValidity uint32
}

func (c *imapConn) nextTag() string {
	c.tag++
	return "A" + strconv.Itoa(c.tag)
}

func (c *imapConn) readGreeting() error {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "* OK") {
		return fmt.Errorf("unexpected greeting %q", strings.TrimSpace(line))
	}
	return nil
}

func (c *imapConn) command(ctx context.Context, verb string, args ...string) error {
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return err
	}
	tag := c.nextTag()
	var b strings.Builder
	b.WriteString(tag)
	b.WriteByte(' ')
	b.WriteString(verb)
	for _, arg := range args {
		b.WriteByte(' ')
		b.WriteString(arg)
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(c.conn, b.String()); err != nil {
		return err
	}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "* ") {
			continue
		}
		if strings.HasPrefix(line, tag+" OK") {
			return nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return fmt.Errorf("%s", line)
		}
	}
}

// ListFolders runs LIST "" "*" and parses untagged LIST responses.
func (c *imapConn) ListFolders(ctx context.Context) ([]Folder, error) {
	finish := c.guardIO(ctx)
	defer finish()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tag := c.nextTag()
	if _, err := io.WriteString(c.conn, tag+" LIST \"\" \"*\"\r\n"); err != nil {
		return nil, err
	}
	var folders []Folder
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "* LIST ") {
			folder, ok := parseLIST(line)
			if ok {
				folders = append(folders, folder)
			}
			continue
		}
		if strings.HasPrefix(line, tag+" OK") {
			return WithEncodedNames(folders), nil
		}
		if strings.HasPrefix(line, tag+" ") {
			return nil, fmt.Errorf("%s", line)
		}
	}
}

func (c *imapConn) Close() error {
	if c.conn == nil {
		return nil
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	_, _ = io.WriteString(c.conn, c.nextTag()+" LOGOUT\r\n")
	err := c.conn.Close()
	c.conn = nil
	return err
}

func quoteIMAP(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// parseLIST parses: * LIST (\HasNoChildren) "/" INBOX
// or quoted names: * LIST () "/" "Sent Messages"
func parseLIST(line string) (Folder, bool) {
	rest := strings.TrimPrefix(line, "* LIST ")
	if rest == line || !strings.HasPrefix(rest, "(") {
		return Folder{}, false
	}
	endFlags := strings.IndexByte(rest, ')')
	if endFlags < 0 {
		return Folder{}, false
	}
	flagsRaw := strings.TrimSpace(rest[1:endFlags])
	var flags []string
	if flagsRaw != "" {
		flags = strings.Fields(flagsRaw)
	}
	rest = strings.TrimSpace(rest[endFlags+1:])
	delimiter, rest, ok := readIMAPString(rest)
	if !ok {
		return Folder{}, false
	}
	name, _, ok := readIMAPString(strings.TrimSpace(rest))
	if !ok || name == "" {
		return Folder{}, false
	}
	return Folder{Name: name, Delimiter: delimiter, Flags: flags}, true
}

func readIMAPString(s string) (string, string, bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' {
		var b strings.Builder
		escaped := false
		for i := 1; i < len(s); i++ {
			ch := s[i]
			if escaped {
				b.WriteByte(ch)
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				return b.String(), s[i+1:], true
			}
			b.WriteByte(ch)
		}
		return "", "", false
	}
	parts := strings.SplitN(s, " ", 2)
	if len(parts) == 1 {
		return parts[0], "", true
	}
	return parts[0], parts[1], true
}

// guardIO bounds both unresponsive peers and cancellation while blocked in I/O.
// Each Client is single-owner, so no commands may run concurrently on it.
func (c *imapConn) guardIO(ctx context.Context) func() {
	budget := c.timeout
	if budget <= 0 {
		budget = 10 * time.Second
	}
	deadline := time.Now().Add(budget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetDeadline(deadline)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetDeadline(time.Now()); close(done) })
	return func() {
		if !stop() {
			<-done
		}
		_ = c.conn.SetDeadline(time.Time{})
	}
}
