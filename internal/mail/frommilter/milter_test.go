package frommilter

import (
	"encoding/binary"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/frombind"
)

// fakeHandler decides via frombind using a fixed sender-login map, exactly
// like the production wiring will.
type fakeHandler struct {
	mapRaw string
}

const testMap = "alice@a.test\talice@a.test\nceo@b.test\talice@a.test\n"

func (f fakeHandler) Authorize(login string, froms []string) (bool, string) {
	allowed := frombind.AllowedFromDomains(frombind.ParseSenderLoginMap(f.mapRaw)[strings.ToLower(login)])
	authenticated := login != ""
	d, _ := frombind.Decide(authenticated, allowed, froms)
	return d == frombind.RejectMissingFrom || d == frombind.RejectMultipleFrom || d == frombind.RejectDomain, d.Reason()
}

// client is a minimal milter protocol client for tests. Every command gets a
// response from the server, so send+read always come in pairs.
type client struct {
	t *testing.T
	c net.Conn
}

func (cl *client) send(pkt []byte) {
	cl.t.Helper()
	buf := make([]byte, 4+len(pkt))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(pkt)))
	copy(buf[4:], pkt)
	if _, err := cl.c.Write(buf); err != nil {
		cl.t.Fatalf("client write: %v", err)
	}
}

func (cl *client) readResponse() byte {
	cl.t.Helper()
	var lenBuf [4]byte
	if _, err := ioReadFull(cl.c, lenBuf[:]); err != nil {
		cl.t.Fatalf("client read: %v", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	buf := make([]byte, n)
	if _, err := ioReadFull(cl.c, buf); err != nil {
		cl.t.Fatalf("client read payload: %v", err)
	}
	return buf[0]
}

func macroPacket(stage string, kv ...string) []byte {
	pkt := append([]byte{}, cmdMacro)
	pkt = append(pkt, stage...)
	for _, f := range kv {
		pkt = append(pkt, f...)
		pkt = append(pkt, 0)
	}
	return pkt
}

func headerPacket(name, value string) []byte {
	pkt := append([]byte{}, cmdHeader)
	pkt = append(pkt, name...)
	pkt = append(pkt, 0)
	pkt = append(pkt, value...)
	pkt = append(pkt, 0)
	return pkt
}

func mailPacket(addr string) []byte {
	pkt := append([]byte{}, cmdMail)
	pkt = append(pkt, addr...)
	pkt = append(pkt, 0)
	return pkt
}

func socketPath(t *testing.T) string {
	t.Helper()
	// macOS caps unix socket paths at ~104 bytes; t.TempDir is too deep.
	dir, err := os.MkdirTemp("", "frombind")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir + "/s.sock"
}

func ioReadFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func TestMilterSessionRejectsSpoofedAndAllowsOwn(t *testing.T) {
	sock := socketPath(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = Serve(l, fakeHandler{mapRaw: testMap}) }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cl := &client{t: t, c: conn}

	cl.send(append([]byte{cmdOptNeg}, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0, 0))
	if got := cl.readResponse(); got != cmdOptNeg {
		t.Fatalf("optneg response = %q", got)
	}

	cl.send(macroPacket("C", "{client_addr}", "127.0.0.1"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("connect macro response = %q", got)
	}
	cl.send(macroPacket("M", "{auth_authen}", "alice@a.test"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("mail macro response = %q", got)
	}
	cl.send(mailPacket("<alice@a.test>"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("mail response = %q", got)
	}
	cl.send(headerPacket("From", "CEO <ceo@outside.test>"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("header response = %q", got)
	}
	// alice@a.test must not claim outside.test: loud reject at EOH.
	cl.send([]byte{cmdEOH})
	if got := cl.readResponse(); got != resReject {
		t.Fatalf("spoofed From response = %q, want reject", got)
	}

	// ABORT resets per-message state: no stale From headers survive.
	cl.send([]byte{cmdAbort})
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("abort response = %q", got)
	}
	cl.send(macroPacket("M", "{auth_authen}", "alice@a.test"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("mail macro after abort response = %q", got)
	}
	cl.send(mailPacket("<loop@a.test>"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("mail after abort response = %q", got)
	}
	cl.send([]byte{cmdEOH})
	if got := cl.readResponse(); got != resReject {
		t.Fatalf("missing-From after abort response = %q, want reject", got)
	}

	// A granted From passes; the delegated alias ceo@b.test belongs to the
	// same login, so its domain is allowed too.
	cl.send(headerPacket("From", "Alice <alice@a.test>"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("own-domain header response = %q", got)
	}
	cl.send([]byte{cmdEOH})
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("own-domain EOH response = %q", got)
	}

	// Unauthenticated inbound (no {auth_authen}) must continue: foreign
	// From domains are normal on inbound mail.
	cl.send(macroPacket("M", "{auth_authen}", ""))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("empty auth macro response = %q", got)
	}
	cl.send(mailPacket("<outsider@elsewhere.test>"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("unauth mail response = %q", got)
	}
	cl.send(headerPacket("From", "Outsider <outsider@elsewhere.test>"))
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("unauth header response = %q", got)
	}
	cl.send([]byte{cmdEOH})
	if got := cl.readResponse(); got != resContinue {
		t.Fatalf("unauthenticated EOH response = %q", got)
	}
}