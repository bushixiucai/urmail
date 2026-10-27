package mailspam

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// listenLoopback binds an ephemeral loopback listener with the
// context-aware ListenConfig the repo lint gate requires.
func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// dialLoopback connects with a context-aware dialer.
func dialLoopback(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// fakePostfix is a minimal milter client used to drive the wire layer exactly
// like Postfix would: length-prefixed packets, one command byte, response per
// non-macro command.
type fakePostfix struct {
	t *testing.T
	c net.Conn
	r *bufio.Reader
}

func newFakePostfix(t *testing.T, c net.Conn) *fakePostfix {
	return &fakePostfix{t: t, c: c, r: bufio.NewReader(c)}
}

func (f *fakePostfix) send(payload []byte) {
	f.t.Helper()
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)
	if _, err := f.c.Write(buf); err != nil {
		f.t.Fatalf("client write: %v", err)
	}
}

// readAction reads one response packet and returns (response byte, payload).
func (f *fakePostfix) readAction() (byte, []byte) {
	f.t.Helper()
	var lenBuf [4]byte
	if _, err := io.ReadFull(f.r, lenBuf[:]); err != nil {
		f.t.Fatalf("client read length: %v", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > maxPacket {
		f.t.Fatalf("client read: bad response length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f.r, buf); err != nil {
		f.t.Fatalf("client read payload: %v", err)
	}
	return buf[0], buf[1:]
}

// sendMacro emits SMFIC_MACRO: the stage byte is NOT NUL-terminated on the
// wire; the key/value pairs are.
func (f *fakePostfix) sendMacro(stage byte, kv ...string) {
	f.t.Helper()
	payload := []byte{cmdMacro, stage}
	for _, p := range kv {
		payload = append(payload, p...)
		payload = append(payload, 0)
	}
	f.send(payload)
}

func pkt(cmd byte, parts ...string) []byte {
	out := []byte{cmd}
	for _, p := range parts {
		out = append(out, p...)
		out = append(out, 0)
	}
	return out
}

// handshake performs the OPTNEG exchange and asserts the negotiated bits.
func (f *fakePostfix) handshake() {
	f.t.Helper()
	negotiation := make([]byte, 13)
	negotiation[0] = cmdOptNeg
	binary.BigEndian.PutUint32(negotiation[1:5], 6)
	binary.BigEndian.PutUint32(negotiation[5:9], 0xff)
	f.send(negotiation)
	response, payload := f.readAction()
	if response != cmdOptNeg || len(payload) != 12 {
		f.t.Fatalf("OPTNEG reply malformed: %c %v", response, payload)
	}
	if version := binary.BigEndian.Uint32(payload[0:4]); version != protoVersion {
		f.t.Fatalf("negotiated version %d", version)
	}
	if actions := binary.BigEndian.Uint32(payload[4:8]); actions != actionAddHdrs|actionChgHdrs {
		f.t.Fatalf("negotiated actions %d, want SMFIF_ADDHDRS only", actions)
	}
	if skip := binary.BigEndian.Uint32(payload[8:12]); skip != 0 {
		f.t.Fatalf("must not skip RCPT/BODY events, skip mask %d", skip)
	}
}

// deliver sends one full message exchange and returns the EOM action list.
func (f *fakePostfix) deliver(from, rcpt string, headers [][2]string, body string) []VerdictHeadersAction {
	f.t.Helper()
	f.sendMacro('M', "{auth_authen}", "user@urmail.cn")
	f.send(pkt(cmdConnect, "client.example", "4", string([]byte{192, 0, 2, 9}), string([]byte{0, 25})))
	if response, _ := f.readAction(); response != resContinue {
		f.t.Fatalf("CONNECT response %c", response)
	}
	f.send(pkt(cmdHelo, "client.example"))
	if response, _ := f.readAction(); response != resContinue {
		f.t.Fatalf("HELO response %c", response)
	}
	f.send(pkt(cmdMail, "<"+from+">", "SIZE=100"))
	if response, _ := f.readAction(); response != resContinue {
		f.t.Fatalf("MAIL response %c", response)
	}
	f.send(pkt(cmdRcpt, "<"+rcpt+">"))
	if response, _ := f.readAction(); response != resContinue {
		f.t.Fatalf("RCPT response %c", response)
	}
	f.send(pkt(cmdData))
	if response, _ := f.readAction(); response != resContinue {
		f.t.Fatalf("DATA response %c", response)
	}
	for _, h := range headers {
		f.send(pkt(cmdHeader, h[0], h[1]))
		if response, _ := f.readAction(); response != resContinue {
			f.t.Fatalf("HEADER response %c", response)
		}
	}
	f.send(pkt(cmdEOH))
	if response, _ := f.readAction(); response != resContinue {
		f.t.Fatalf("EOH response %c", response)
	}
	chunks := splitBodyChunks(body, 32)
	for _, chunk := range chunks {
		f.send(append([]byte{cmdBody}, chunk...))
		if response, _ := f.readAction(); response != resContinue {
			f.t.Fatalf("BODY response %c", response)
		}
	}
	f.send([]byte{cmdEOB})
	var actions []VerdictHeadersAction
	for {
		response, payload := f.readAction()
		if response == resContinue {
			return actions
		}
		if response == resReject {
			return []VerdictHeadersAction{{reject: true}}
		}
		if response == resChgHeader {
			if len(payload) < 6 {
				f.t.Fatal("short CHGHEADER")
			}
			continue
		}
		if response != resInsHeader {
			f.t.Fatalf("unexpected EOM action %c", response)
		}
		if len(payload) < 6 {
			f.t.Fatalf("INSHEADER payload too short: %v", payload)
		}
		index := binary.BigEndian.Uint32(payload[0:4])
		fields := strings.SplitN(string(payload[4:]), "\x00", 3)
		if len(fields) < 2 {
			f.t.Fatalf("INSHEADER name/value malformed: %v", fields)
		}
		actions = append(actions, VerdictHeadersAction{index: index, name: fields[0], value: fields[1]})
	}
}

// VerdictHeadersAction is one decoded INSHEADER reply.
type VerdictHeadersAction struct {
	index  uint32
	name   string
	value  string
	reject bool
}

func splitBodyChunks(body string, size int) []string {
	var chunks []string
	for len(body) > 0 {
		n := size
		if n > len(body) {
			n = len(body)
		}
		chunks = append(chunks, body[:n])
		body = body[n:]
	}
	if chunks == nil {
		chunks = []string{""}
	}
	return chunks
}

func startMilter(t *testing.T, h VerdictHandler, cfg MilterConfig) (*Server, *fakePostfix) {
	t.Helper()
	ln := listenLoopback(t)
	server := &Server{Addr: ln.Addr().String(), Handler: h, Config: cfg}
	go func() {
		_ = serveConn(context.Background(), mustAccept(t, ln), cfg.withDefaults(), h)
	}()
	conn := dialLoopback(t, ln.Addr().String())
	t.Cleanup(func() { _ = conn.Close(); _ = ln.Close() })
	return server, newFakePostfix(t, conn)
}

func mustAccept(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(5 * time.Second))
	}
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func staticHandler(response VerdictResponse) VerdictHandler {
	return VerdictFunc(func(context.Context, VerdictRequest) VerdictResponse { return response })
}

func TestMilterRoundTripInsertsHeaders(t *testing.T) {
	sent := VerdictResponse{InsertHeaders: [][2]string{{HeaderNameSpam, "yes"}, {HeaderNameScore, "7.50"}}}
	_, client := startMilter(t, staticHandler(sent), MilterConfig{})
	client.handshake()
	actions := client.deliver("spammer@bulk.example", "victim@urmail.cn", [][2]string{
		{"From", "Spammer <spammer@bulk.example>"},
		{"Subject", "Buy now"},
	}, "cheap goods body")
	if len(actions) != 2 {
		t.Fatalf("expected 2 INSHEADER actions, got %+v", actions)
	}
	// Postfix applies each insert as it arrives: the Score lands at index 0
	// first, then the flag insert at index 0 lands above it, so the final
	// header order top-down is Spam, Score — the request order.
	if actions[1].name != HeaderNameSpam || actions[1].value != "yes" || actions[1].index != 0 {
		t.Fatalf("top header wrong: %+v", actions[1])
	}
	if actions[0].name != HeaderNameScore || actions[0].value != "7.50" || actions[0].index != 0 {
		t.Fatalf("second header wrong: %+v", actions[0])
	}
}

func TestMilterDenyActionsDefaultOff(t *testing.T) {
	// The handler asks for rejection; the default wire config must refuse to
	// forward it (classification + Junk foldering is the v1 product action).
	sent := VerdictResponse{InsertHeaders: [][2]string{{HeaderNameSpam, "yes"}}, Reject: true}
	_, client := startMilter(t, staticHandler(sent), MilterConfig{})
	client.handshake()
	actions := client.deliver("spammer@bulk.example", "victim@urmail.cn", nil, "body")
	for _, action := range actions {
		if action.reject {
			t.Fatal("rejection leaked through with DenyActions disabled")
		}
	}
}

func TestMilterDenyActionsExplicitlyEnabled(t *testing.T) {
	sent := VerdictResponse{InsertHeaders: [][2]string{{HeaderNameSpam, "yes"}}, Reject: true}
	_, client := startMilter(t, staticHandler(sent), MilterConfig{DenyActions: true})
	client.handshake()
	actions := client.deliver("spammer@bulk.example", "victim@urmail.cn", nil, "body")
	if len(actions) != 1 || !actions[0].reject {
		t.Fatalf("explicit deny action not honored: %+v", actions)
	}
}

func TestMilterPassesContextToHandler(t *testing.T) {
	var got VerdictRequest
	handler := VerdictFunc(func(_ context.Context, req VerdictRequest) VerdictResponse {
		got = req
		return VerdictResponse{}
	})
	_, client := startMilter(t, handler, MilterConfig{})
	client.handshake()
	_ = client.deliver("outer@bulk.example", "inbox@urmail.cn", [][2]string{
		{"From", "Outer <outer@bulk.example>"},
		{"Authentication-Results", "mx.urmail.cn; spf=pass"},
	}, strings.Repeat("payload ", 40))
	if got.SASLUser != "user@urmail.cn" {
		t.Fatalf("SASL macro lost: %q", got.SASLUser)
	}
	if got.EnvelopeFrom != "outer@bulk.example" {
		t.Fatalf("envelope from wrong: %q", got.EnvelopeFrom)
	}
	if len(got.Recipients) != 1 || got.Recipients[0] != "inbox@urmail.cn" {
		t.Fatalf("recipients wrong: %v", got.Recipients)
	}
	if len(got.Headers) != 2 {
		t.Fatalf("headers not collected: %v", got.Headers)
	}
	if !strings.Contains(string(got.BodySample), "payload") {
		t.Fatalf("body sample empty: %d bytes", len(got.BodySample))
	}
}

func TestMilterBodySampleCapped(t *testing.T) {
	var got VerdictRequest
	handler := VerdictFunc(func(_ context.Context, req VerdictRequest) VerdictResponse {
		got = req
		return VerdictResponse{}
	})
	_, client := startMilter(t, handler, MilterConfig{MaxBodySampleBytes: 1024})
	client.handshake()
	_ = client.deliver("a@b.example", "c@urmail.cn", nil, strings.Repeat("x", 10000))
	if len(got.BodySample) != 1024 {
		t.Fatalf("body sample not capped: %d", len(got.BodySample))
	}
}

func TestMilterAbortResetsSession(t *testing.T) {
	var calls int
	handler := VerdictFunc(func(_ context.Context, req VerdictRequest) VerdictResponse {
		calls++
		if len(req.Recipients) != 1 {
			t.Fatalf("aborted message state leaked into next message: %+v", req)
		}
		return VerdictResponse{}
	})
	_, client := startMilter(t, handler, MilterConfig{})
	client.handshake()
	// First message aborted mid-flight after RCPT.
	client.send(pkt(cmdMail, "<a@b.example>"))
	_, _ = client.readAction()
	client.send(pkt(cmdRcpt, "<first@urmail.cn>"))
	_, _ = client.readAction()
	client.send([]byte{cmdAbort})
	// Second message completes normally; it must see only its own recipient.
	_ = client.deliver("c@d.example", "second@urmail.cn", nil, "body")
	if calls != 1 {
		t.Fatalf("EOM fired %d times for two messages with one abort", calls)
	}
}

func TestMilterQuitClosesConnection(t *testing.T) {
	_, client := startMilter(t, staticHandler(VerdictResponse{}), MilterConfig{})
	client.handshake()
	client.send([]byte{cmdQuit})
	// The server closes after QUIT; the next read must hit EOF.
	if _, err := client.r.Peek(1); err == nil {
		t.Fatal("connection still open after QUIT")
	}
}
