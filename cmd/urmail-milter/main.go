// Command urmail-milter is the outbound DKIM signing milter service
// (compose urmail-signer profile, command ["urmail-milter", "sign"]).
//
// It listens on the endpoint Postfix's rendered smtpd_milters chain
// references (default inet 0.0.0.0:12301, published to the host loopback
// by the compose service so the host-network Postfix reaches it), signs
// authenticated local submissions whose From-domain has a provisioned key
// in the OpenDKIM-compatible key directory, and passes everything else
// through unsigned (the chain's milter_default_action=accept stays the
// degrade posture).
//
// Subcommands:
//
//	sign        run the signing milter (default behavior of the compose
//	            service; required for the signer profile)
//	healthcheck compose HEALTHCHECK probe: SMFIC_OPTNEG handshake against
//	            the loopback form of URMAIL_MILTER_LISTEN, exit 0 on a
//	            valid OPTNEG reply, 1 otherwise
//
// Environment:
//
//	URMAIL_MILTER_LISTEN   listen address (default 0.0.0.0:12301)
//	URMAIL_DKIM_KEY_DIR    OpenDKIM-compatible key directory
//	                       (<dir>/<domain>/<selector>.private; required)
//	URMAIL_DKIM_SELECTOR   selector, default "mail" (matches the existing
//	                       mail._domainkey DNS records — no DNS change)
package main

import (
	"context"
	"fmt"
	"net"
	"net/mail"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bushixiucai/urmail/internal/dkimsign"
	"github.com/bushixiucai/urmail/internal/mailspam"
)

type signHandler struct {
	store *dkimsign.Store
}

// Decide signs AUTHENTICATED LOCAL submissions only: the submission must
// carry a SASL identity (587/465 after AUTH) or originate from the host
// loopback (SMFIC_CONNECT address / {client_addr} macro — local
// injection). The global smtpd_milters chain also covers port 25, where an
// external attacker can forge From: user@urmail.cn — signing that would
// put OUR key under an attacker-controlled identity (QA wiring-fix-2 item
// 1). Unkeyed domains, foreign senders, unauthenticated external mail and
// truncated messages all pass through unsigned.
func (h *signHandler) Decide(_ context.Context, req mailspam.VerdictRequest) mailspam.VerdictResponse {
	if req.SASLUser == "" && !isLoopbackClient(req.ClientAddr) {
		return mailspam.VerdictResponse{}
	}
	if req.FullBodyIncomplete || req.HeadersIncomplete {
		// Truncated capture cannot produce a verifiable signature.
		return mailspam.VerdictResponse{}
	}
	from := ""
	for _, header := range req.Headers {
		if strings.EqualFold(header.Name, "From") {
			from = header.Value
			break
		}
	}
	address, err := mail.ParseAddress(strings.TrimPrefix(from, " "))
	if err != nil {
		return mailspam.VerdictResponse{}
	}
	at := strings.LastIndex(address.Address, "@")
	if at <= 0 {
		return mailspam.VerdictResponse{}
	}
	domain := strings.ToLower(address.Address[at+1:])
	key, ok, err := h.store.KeyFor(domain)
	if err != nil || !ok {
		return mailspam.VerdictResponse{}
	}
	message := append(append([]byte{}, headersBytes(req.Headers)...), req.FullBody...)
	if !strings.HasSuffix(string(message), "\r\n") {
		message = append(message, '\r', '\n')
	}
	value, err := dkimsign.SignHeader(message, key, domain, h.store.Selector())
	if err != nil {
		// Signing failure degrades to unsigned delivery: never block mail
		// on the signer's own errors (the chain default is accept).
		return mailspam.VerdictResponse{}
	}
	return mailspam.VerdictResponse{InsertHeaders: [][2]string{{"DKIM-Signature", value}}}
}

// headersBytes reassembles the milter header block in received order.
func headersBytes(headers []mailspam.Header) []byte {
	var out []byte
	for _, h := range headers {
		out = append(out, h.Name...)
		out = append(out, ':')
		if !strings.HasPrefix(h.Value, " ") {
			out = append(out, ' ')
		}
		out = append(out, h.Value...)
		out = append(out, '\r', '\n')
	}
	out = append(out, '\r', '\n')
	return out
}

// isLoopbackClient accepts the loopback {client_addr} forms Postfix macro
// expansion produces.
func isLoopbackClient(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	ip := net.ParseIP(strings.Trim(addr, "[]"))
	return ip != nil && ip.IsLoopback()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: urmail-milter sign|healthcheck")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sign":
		err = runSign()
	case "healthcheck":
		err = runHealthcheck(os.Getenv("URMAIL_MILTER_LISTEN"))
	default:
		err = fmt.Errorf("unknown subcommand %q (want sign|healthcheck)", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "urmail-milter:", err)
		os.Exit(1)
	}
}

// runSign runs the DKIM signing milter until SIGTERM/SIGINT.
func runSign() error {
	listen := os.Getenv("URMAIL_MILTER_LISTEN")
	if listen == "" {
		listen = "0.0.0.0:12301"
	}
	selector := os.Getenv("URMAIL_DKIM_SELECTOR")
	if selector == "" {
		selector = "mail"
	}
	keyDir := os.Getenv("URMAIL_DKIM_KEY_DIR")
	if keyDir == "" {
		return fmt.Errorf("URMAIL_DKIM_KEY_DIR is required")
	}
	store := dkimsign.NewStore(keyDir, selector)
	if domains, err := store.SignDomains(); err == nil {
		fmt.Printf("urmail-milter: signing domains %v (selector %q)\n", domains, selector)
	} else {
		fmt.Fprintf(os.Stderr, "urmail-milter: key dir %s unreadable: %v\n", keyDir, err)
	}
	server := &mailspam.Server{
		Addr:    listen,
		Handler: &signHandler{store: store},
		Config:  mailspam.MilterConfig{KeepFullBody: true, MaxFullBodyBytes: 32 << 20},
	}
	// Graceful SIGTERM: stop accepting, let the in-flight milter session
	// finish (Postfix retries on the next connection; the chain default is
	// accept, so a restarting signer never blocks mail).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.ListenAndServe(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("milter serve: %w", err)
	}
	return nil
}

// runHealthcheck performs a real milter SMFIC_OPTNEG handshake against the
// loopback form of the configured listener and returns nil only when the
// negotiation completes (compose HEALTHCHECK contract).
func runHealthcheck(listen string) error {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("parse listen address: %w", err)
	}
	if port == "" {
		return fmt.Errorf("listen address has no port")
	}
	// Numeric port validation lets the dialer build the address from a
	// constant host plus a vetted integer — no tainted string flows into
	// the dial (gosec G704).
	portNum, portErr := strconv.Atoi(port)
	if portErr != nil || portNum < 1 || portNum > 65535 {
		return fmt.Errorf("listen address has invalid port %q", port)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(portNum)), 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial milter: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	// SMFIC_OPTNEG: version 6, SMFIF_ADDHDRS|SMFIF_CHGHDRS, no actions.
	plen := []byte{0x00, 0x00, 0x00, 0x0d}
	payload := append([]byte{'O'}, plen...)
	payload = append(payload, []byte{0x00, 0x00, 0x00, 0x06, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x03}...)
	if _, err = conn.Write(payload); err != nil {
		return fmt.Errorf("write optneg: %w", err)
	}
	var hlen [4]byte
	if _, err = readFull(conn, hlen[:]); err != nil {
		return fmt.Errorf("read optneg reply: %w", err)
	}
	replyLen := int(hlen[0])<<24 | int(hlen[1])<<16 | int(hlen[2])<<8 | int(hlen[3])
	if replyLen < 1 || replyLen > 4096 {
		return fmt.Errorf("milter reply length %d out of range", replyLen)
	}
	reply := make([]byte, replyLen)
	if _, err = readFull(conn, reply); err != nil {
		return fmt.Errorf("read optneg body: %w", err)
	}
	if reply[0] != 'O' {
		return fmt.Errorf("milter reply %q is not OPTNEG", reply[0])
	}
	// SMFIC_QUIT: polite close.
	_, _ = conn.Write([]byte{0x00, 0x00, 0x00, 0x01, 'Q'})
	return nil
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
