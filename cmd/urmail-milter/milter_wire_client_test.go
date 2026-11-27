package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/dkimsign"
	"github.com/bushixiucai/urmail/internal/mailspam"
)

// wireTestClient drives the signer's milter protocol over a real TCP
// connection, mirroring the fakePostfix helper in internal/mailspam
// milter_test.go (same length prefix, same command encoding).
type wireTestClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

// startWireSigner boots the signer on an ephemeral loopback port and
// returns a connected client.
func startWireSigner(t *testing.T, store *dkimsign.Store) *wireTestClient {
	t.Helper()
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	_ = reserve.Close()

	server := &mailspam.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: &signHandler{store: store},
		Config:  mailspam.MilterConfig{KeepFullBody: true, MaxFullBodyBytes: 32 << 20},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.ListenAndServe(ctx) }()

	var conn net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for {
		var dialErr error
		conn, dialErr = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if dialErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("signer never became dialable on port %d: %v", port, dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Read deadline prevents any protocol mismatch from blocking forever.
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return &wireTestClient{t: t, conn: conn, r: newConnReader(conn)}
}

func newConnReader(c net.Conn) *bufio.Reader {
	return bufio.NewReader(c)
}

// send writes one milter packet: [4B BE length] + payload (payload[0] is
// the command byte).
func (c *wireTestClient) send(payload []byte) {
	c.t.Helper()
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)
	if _, err := c.conn.Write(buf); err != nil {
		c.t.Fatalf("client write: %v", err)
	}
}

// readAction reads one response packet: [4B BE length] + data (data[0] is
// the response byte).
func (c *wireTestClient) readAction() (byte, []byte) {
	c.t.Helper()
	var lenBuf [4]byte
	if _, err := c.readFull(lenBuf[:]); err != nil {
		c.t.Fatalf("client read length: %v", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > 1<<20 {
		c.t.Fatalf("client read: bad response length %d", n)
	}
	buf := make([]byte, n)
	if _, err := c.readFull(buf); err != nil {
		c.t.Fatalf("client read payload: %v", err)
	}
	return buf[0], buf[1:]
}

func (c *wireTestClient) readFull(buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.conn.Read(buf[total:])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (c *wireTestClient) expectContinue() {
	c.t.Helper()
	cmd, payload := c.readAction()
	if cmd != 'c' {
		c.t.Fatalf("expected continue reply, got %q (%q)", string(cmd), string(payload))
	}
}

// handshake performs the SMFIC_OPTNEG exchange, matching
// fakePostfix.handshake() byte-for-byte.
func (c *wireTestClient) handshake() {
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

// pkt builds one milter protocol packet with NUL-separated parts.
func wirePkt(cmd byte, parts ...string) []byte {
	out := []byte{cmd}
	for _, p := range parts {
		out = append(out, p...)
		out = append(out, 0)
	}
	return out
}

// sendMacro emits SMFIC_MACRO for the given stage.
func (c *wireTestClient) sendMacro(stage byte, kv ...string) {
	c.t.Helper()
	payload := []byte{'D', stage}
	for _, p := range kv {
		payload = append(payload, p...)
		payload = append(payload, 0)
	}
	c.send(payload)
}

// deliverMail sends one full message exchange (MACRO, CONNECT, MAIL, RCPT,
// DATA, headers, body, EOM) and returns the EOM action code plus the
// DKIM-Signature header value if an SMFIR_INSHEADER action was present.
// The clientAddr is the address Postfix would report in SMFIC_CONNECT.
func (c *wireTestClient) deliverMail(from, rcpt string, clientAddr []byte, headers [][2]string, body string) (byte, string) {
	c.t.Helper()
	c.sendMacro('M', "{auth_authen}", from)
	c.send(wirePkt('C', "client.example", "4", string([]byte{0x01, 0xBB}), string(clientAddr)))
	c.expectContinue()
	c.send(wirePkt('H', "client.example"))
	c.expectContinue()
	c.send(wirePkt('M', "<"+from+">", "SIZE=100"))
	c.expectContinue()
	c.send(wirePkt('R', "<"+rcpt+">"))
	c.expectContinue()
	c.send(wirePkt('T'))
	c.expectContinue()
	for _, h := range headers {
		c.send(wirePkt('L', h[0], h[1]))
	}
	c.send(wirePkt('B', body))
	c.send(wirePkt('E'))
	var sig string
	for {
		cmd, payload := c.readAction()
		switch cmd {
		case 'i':
			fields := strings.SplitN(strings.TrimSuffix(string(payload), "\x00"), "\x00", 2)
			if len(fields) == 2 && fields[0] == "DKIM-Signature" {
				sig = fields[1]
			}
		case 'c':
			return 'c', sig
		case 'a', 'r':
			return cmd, sig
		}
	}
}

func (c *wireTestClient) quit() {
	c.t.Helper()
	c.send(wirePkt('Q'))
	// The server does NOT reply to QUIT: it returns (true, nil) which means
	// close the connection without a response. Reading here would hang.
}

// wireSignedMessage assembles the message Postfix sends on: DKIM-Signature
// inserted at the top of the header block.
func wireSignedMessage(sig string) string {
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
