package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/bushixiucai/urmail/internal/dkimsign"
	"github.com/bushixiucai/urmail/internal/mailspam"
)

// ---- DKIM key helpers ----

func provisionWireKey(t *testing.T, dir, domain string) *rsa.PublicKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	domainDir := filepath.Join(dir, domain)
	if err = os.MkdirAll(domainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	if err = os.WriteFile(filepath.Join(domainDir, "mail.private"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return &key.PublicKey
}

func txtForWire(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
}

func signedWireMessage(sig string) string {
	return "DKIM-Signature: " + sig + "\r\n" +
		"From: Alice <alice@example.test>\r\n" +
		"To: Bob <bob@remote.test>\r\n" +
		"Subject: wire probe\r\n" +
		"Date: Thu, 27 Nov 2026 00:00:00 +0000\r\n" +
		"Message-ID: <m1@example.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"\r\n" +
		"wire probe body\r\n"
}

// ---- milter wire protocol client (mirrors fakePostfix) ----
//
// Every helper below sends ONE length-prefixed packet [len][cmd][payload...]
// and then consumes that event's SMFIR_CONTINUE reply, exactly like Postfix
// on the wire. (rev-6: the original scaffolding split command and payload
// into separate packets, so the server never saw header/body events and the
// signing gate never fired; CONNECT additionally lost its command byte to
// the hostname's first character.)

type wireClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func startWireSigner(t *testing.T, handler mailspam.VerdictHandler) *wireClient {
	t.Helper()
	reserve, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	_ = reserve.Close()

	server := &mailspam.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: handler,
		Config:  mailspam.MilterConfig{KeepFullBody: true, MaxFullBodyBytes: 32 << 20},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.ListenAndServe(ctx) }()

	var conn net.Conn
	dialer := &net.Dialer{Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var dialErr error
		conn, dialErr = dialer.DialContext(t.Context(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if dialErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("signer never became dialable on port %d: %v", port, dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return &wireClient{t: t, conn: conn, r: bufio.NewReader(conn)}
}

func (c *wireClient) send(data []byte) {
	c.t.Helper()
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(data)))
	copy(buf[4:], data)
	if _, err := c.conn.Write(buf); err != nil {
		c.t.Fatalf("client write: %v", err)
	}
}

func (c *wireClient) readAction() (byte, []byte) {
	c.t.Helper()
	var lenBuf [4]byte
	total := 0
	for total < 4 {
		n, err := c.conn.Read(lenBuf[total:])
		if err != nil {
			c.t.Fatalf("client read length: %v", err)
		}
		total += n
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > 1<<20 {
		c.t.Fatalf("bad response length %d", n)
	}
	buf := make([]byte, n)
	total = 0
	for total < int(n) {
		nr, err := c.conn.Read(buf[total:])
		if err != nil {
			c.t.Fatalf("client read payload: %v", err)
		}
		total += nr
	}
	return buf[0], buf[1:]
}

func (c *wireClient) expectContinue() {
	c.t.Helper()
	cmd, payload := c.readAction()
	if cmd != 'c' {
		c.t.Fatalf("expected continue reply, got %q (%q)", string(cmd), string(payload))
	}
}

func (c *wireClient) handshake() {
	c.t.Helper()
	negotiation := make([]byte, 13)
	negotiation[0] = 'O'
	binary.BigEndian.PutUint32(negotiation[1:5], 6)
	binary.BigEndian.PutUint32(negotiation[5:9], 0xff)
	c.send(negotiation)
	response, payload := c.readAction()
	if response != 'O' || len(payload) != 12 {
		c.t.Fatalf("OPTNEG reply malformed: %c %v", response, payload)
	}
}

func (c *wireClient) sendMacro(stage byte, kv ...string) {
	c.t.Helper()
	payload := []byte{'D', stage}
	for _, p := range kv {
		payload = append(payload, p...)
		payload = append(payload, 0)
	}
	c.send(payload)
}

// sendConnect emits one SMFIC_CONNECT packet: [C][hostname\0 family(1B)
// port(2B network) address(4/16B binary)] — command byte and payload ride
// in the SAME length-prefixed packet, as the milter protocol requires (the
// address bytes may contain NULs; the trailing terminator is tolerated by
// the server and ignored).
func (c *wireClient) sendConnect(host, family string, addr []byte) {
	c.t.Helper()
	pkt := make([]byte, 0, 1+len(host)+1+len(family)+2+len(addr)+1)
	pkt = append(pkt, 'C')
	pkt = append(pkt, host...)
	pkt = append(pkt, 0)
	pkt = append(pkt, family...)
	port := []byte{0x01, 0xBB}
	pkt = append(pkt, port...)
	pkt = append(pkt, addr...)
	pkt = append(pkt, 0)
	c.send(pkt)
}

func (c *wireClient) helo(host string) {
	c.t.Helper()
	c.send(append([]byte{'H'}, []byte(host+"\x00")...))
	c.expectContinue()
}

func (c *wireClient) mail(from string) {
	c.t.Helper()
	c.send(append([]byte{'M'}, []byte("<"+from+">\x00"+"SIZE=100\x00")...))
	c.expectContinue()
}

func (c *wireClient) rcpt(to string) {
	c.t.Helper()
	c.send(append([]byte{'R'}, []byte("<"+to+">\x00")...))
	c.expectContinue()
}

func (c *wireClient) data() {
	c.t.Helper()
	c.send([]byte{'T'})
	c.expectContinue()
}

func (c *wireClient) header(name, value string) {
	c.t.Helper()
	c.send(append([]byte{'L'}, []byte(name+"\x00"+value+"\x00")...))
	c.expectContinue()
}

func (c *wireClient) bodyChunk(chunk string) {
	c.t.Helper()
	c.send(append([]byte{'B'}, []byte(chunk)...))
	c.expectContinue()
}

// eom sends SMFIC_EOM and reads responses until continue/accept/reject,
// capturing any DKIM-Signature ins-header action. Every earlier event
// consumed its own SMFIR_CONTINUE (Postfix behaves the same), so the first
// reply read here belongs to EOM itself.
func (c *wireClient) eom() (byte, string) {
	c.t.Helper()
	c.send([]byte{'E'})
	var sig string
	for {
		cmd, payload := c.readAction()
		switch cmd {
		case 'i':
			// SMFIR_INSHEADER payload: 4-byte network-order header index,
			// then name\0value\0. Skip the index before the field split
			// (rev-6: parsing the raw payload made the name check compare
			// against the leading NUL padding, so the signature was never
			// captured).
			if len(payload) >= 4 {
				rest := string(payload[4:])
				fields := strings.SplitN(strings.TrimSuffix(rest, "\x00"), "\x00", 2)
				if len(fields) == 2 && fields[0] == "DKIM-Signature" {
					sig = fields[1]
				}
			}
		case 'c':
			return 'c', sig
		case 'a', 'r':
			return cmd, sig
		}
	}
}

func (c *wireClient) quit() {
	c.t.Helper()
	c.send([]byte{'Q'})
	// No reply: the server returns (true, nil) and closes the connection.
}

// ---- wire e2e tests ----

// TestWireEndToEndSignsAuthenticatedSubmission drives the REAL milter TCP
// wire protocol end to end (QA wiring-fix-2 S-3 case ①): CONNECT from a
// loopback client → OPTNEG → authenticated MAIL with headers/body → EOM;
// the inserted DKIM-Signature must verify under the standards verifier
// (RFC 6376 — the same contract Perl Mail::DKIM enforces).
func TestWireEndToEndSignsAuthenticatedSubmission(t *testing.T) {
	dir := t.TempDir()
	pub := provisionWireKey(t, dir, "example.test")
	handler := &signHandler{store: dkimsign.NewStore(dir, "mail")}
	client := startWireSigner(t, handler)

	client.handshake()
	client.sendMacro('C', "{daemon_name}", "test-daemon")
	client.sendConnect("client.example", "4", []byte{127, 0, 0, 1})
	client.expectContinue()
	client.helo("client.example")
	client.mail("alice@example.test")
	client.rcpt("bob@remote.test")
	client.data()
	client.header("From", "Alice <alice@example.test>")
	client.header("To", "Bob <bob@remote.test>")
	client.header("Subject", "wire probe")
	client.header("Date", "Thu, 27 Nov 2026 00:00:00 +0000")
	client.header("Message-ID", "<m1@example.test>")
	client.header("MIME-Version", "1.0")
	client.bodyChunk("wire probe body\r\n")
	cmd, sig := client.eom()
	if cmd != 'c' {
		t.Fatalf("EOM action %q", string(cmd))
	}
	if sig == "" {
		t.Fatal("no DKIM-Signature inserted")
	}

	// The message Postfix sends on: DKIM-Signature inserted at the top
	// (value carries the folded continuation lines; the header terminator
	// CRLF is Postfix's own insertion — same as TestSignAndVerifyRoundTrip).
	signed := signedWireMessage(sig)
	txt := txtForWire(pub)
	verifications, err := dkim.VerifyWithOptions(strings.NewReader(signed), &dkim.VerifyOptions{
		LookupTXT: func(domain string) ([]string, error) {
			if strings.HasPrefix(domain, "mail._domainkey.") {
				return []string{txt}, nil
			}
			return nil, os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatalf("verify failed hard: %v", err)
	}
	if len(verifications) != 1 || verifications[0].Err != nil {
		t.Fatalf("signature did not verify: %+v (sig=%q)", verifications, sig)
	}
	client.quit()
}

// TestWireEndToEndRejectsUnauthenticatedForgery is the S-3 security
// negative (case ②): an unauthenticated external client (public CONNECT
// address, no SASL) forging a local From gets NO DKIM-Signature insertion.
func TestWireEndToEndRejectsUnauthenticatedForgery(t *testing.T) {
	dir := t.TempDir()
	// Provision the key for the forged domain: with an empty key dir the
	// message is unsigned because the domain is UNKEYED, not because the
	// client is unauthenticated — the anchor would prove nothing (rev-6).
	provisionWireKey(t, dir, "example.test")
	handler := &signHandler{store: dkimsign.NewStore(dir, "mail")}
	client := startWireSigner(t, handler)

	client.handshake()
	client.sendMacro('C', "{daemon_name}", "test-daemon")
	client.sendConnect("evil.example", "4", []byte{203, 0, 113, 9})
	client.expectContinue()
	client.helo("evil.example")
	client.mail("user@example.test")
	client.rcpt("bob@remote.test")
	client.data()
	client.header("From", "Someone <user@example.test>")
	client.header("To", "Bob <bob@remote.test>")
	client.bodyChunk("forged body\r\n")
	cmd, _ := client.eom()
	if cmd == 'i' {
		t.Fatal("external forgery WAS signed — key laundering")
	}
	client.quit()
}

// TestWireEndToEndSecondMailOnSameConnection keeps the S-2b regression
// anchor over the wire: the second mail on the same TCP connection still
// resolves the connection-level peer address and signs.
func TestWireEndToEndSecondMailOnSameConnection(t *testing.T) {
	dir := t.TempDir()
	// The S-2b anchor asserts a d=example.test signature on BOTH mails, so
	// the key must be provisioned (rev-6: an empty key dir made the anchor
	// fail with sig "" regardless of the wire behavior under test).
	provisionWireKey(t, dir, "example.test")
	handler := &signHandler{store: dkimsign.NewStore(dir, "mail")}
	client := startWireSigner(t, handler)

	client.handshake()
	client.sendMacro('C', "{daemon_name}", "test-daemon")
	client.sendConnect("client.example", "4", []byte{127, 0, 0, 1})
	client.expectContinue()
	client.helo("client.example")

	for i := 1; i <= 2; i++ {
		client.mail("alice@example.test")
		client.rcpt("bob@remote.test")
		client.data()
		client.header("From", "Alice <alice@example.test>")
		client.header("To", "Bob <bob@remote.test>")
		client.header("Subject", "wire probe")
		client.header("Date", "Thu, 27 Nov 2026 00:00:00 +0000")
		client.header("Message-ID", fmt.Sprintf("<m%d@example.test>", i))
		client.header("MIME-Version", "1.0")
		client.bodyChunk("body\r\n")
		cmd, sig := client.eom()
		if cmd != 'c' || sig == "" || !strings.Contains(sig, "d=example.test") {
			t.Fatalf("mail %d: action %q sig %q", i, cmd, sig)
		}
	}
	client.quit()
}
