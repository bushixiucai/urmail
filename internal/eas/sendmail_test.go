package eas

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// Wire bytes are assembled from the published MS-ASWBXML page 21 table,
// independently of Encode, including the OPAQUE length and both END tokens.
func composeWire(clientID string, raw []byte) []byte {
	b := []byte{3, 1, 106, 0, 0, 21, 0x45, 0x51, 3}
	b = append(b, clientID...)
	b = append(b, 0, 1, 0x08, 0x50, 0xC3)
	var lenbuf [5]byte
	n := binary.PutUvarint(lenbuf[:], uint64(len(raw)))
	// WBXML integers are big endian seven-bit groups, unlike Go Uvarint.
	for i := n - 1; i >= 0; i-- {
		v := lenbuf[i] & 0x7F
		if i > 0 {
			v |= 0x80
		}
		b = append(b, v)
	}
	b = append(b, raw...)
	return append(b, 1, 1)
}

func TestNormativeWBXMLFixtures(t *testing.T) {
	// MS FolderHierarchy FolderSync=16, SyncKey=12, content=40.
	for _, fixture := range []string{"03016a00000756520330000101", "03016a00455c4f4b033000015203653a494e424f580001010101"} {
		raw, _ := hex.DecodeString(fixture)
		n, err := Decode(raw)
		if err != nil {
			t.Fatalf("%s: %v", fixture, err)
		}
		if !bytes.Equal(Encode(n), raw) {
			t.Fatalf("canonical wire %x", Encode(n))
		}
	}
	for _, bad := range [][]byte{{3, 1, 106, 0, 0, 7, 0x96}, {3, 1, 106, 0, 0, 7, 0x56, 0x52, 3, '0', 0, 1}} {
		if _, err := Decode(bad); err == nil {
			t.Fatalf("accepted malformed %x", bad)
		}
	}
	if tokCommands != 0x16 || tokEmailFlagStatus != 0x3B || tokCalSubject != 0x26 || tokContactFirstName != 0x1F {
		t.Fatal("normative application tokens drift")
	}
}

const deviceMIME = "From: a@eas.test\r\nTo: visible@example.test\r\nBcc: hidden@example.test\r\nSubject: =?UTF-8?B?5rWL6K+V?=\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nhello=20device\r\n--x\r\nContent-Type: application/octet-stream; name=test.bin\r\nContent-Disposition: attachment; filename=test.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nAAECAw==\r\n--x--\r\n"

func TestSendMailMIME(t *testing.T) {
	msg, err := parseOutgoingMIME([]byte(deviceMIME))
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != "a@eas.test" || len(msg.Bcc) != 1 || msg.Subject != "测试" || !strings.Contains(msg.Text, "hello device") || len(msg.Attachments) != 1 || !bytes.Equal(msg.Attachments[0].Content, []byte{0, 1, 2, 3}) {
		t.Fatalf("decoded %+v", msg)
	}
	if _, err = parseOutgoingMIME([]byte(strings.Replace(deviceMIME, "From: a@eas.test", "From: a@eas.test, attacker@foreign.test", 1))); err == nil {
		t.Fatal("multiple senders accepted")
	}
}

type sendFake struct {
	calls int
	last  webmail.Outgoing
	err   error
}

func (f *sendFake) Send(_ context.Context, _, _ string, msg webmail.Outgoing) (webmail.Submission, error) {
	f.calls++
	f.last = msg
	return webmail.Submission{Raw: []byte("From: a@eas.test\r\n\r\nsent")}, f.err
}

type sentFake struct {
	webmail.Client
	original *webmail.Message
	uidv     uint32
	calls    int
	err      error
}

func (f *sentFake) SaveSentCopy(context.Context, []byte) (webmail.DraftSaveResult, error) {
	f.calls++
	return webmail.DraftSaveResult{}, f.err
}
func (f *sentFake) Close() error { return nil }

type sentDialer struct {
	client *sentFake
	err    error
}

func (d *sentDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return d.client, d.err
}

func TestSendMailDurableLifecycle(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA, Address: "a@eas.test", TokenHash: "send-vault"}
	r, s := newEASRouter(t, f, sess)
	vault := webmail.NewMemoryVault()
	vault.Put(sess.TokenHash, "password") //nolint:staticcheck // test adapter seeding, Put exists for this
	s.Vault = vault
	submit := &sendFake{}
	s.Submitter = submit
	allowed := true
	s.MaySend = func(context.Context, int64, int64) (bool, error) { return allowed, nil }
	sent := &sentFake{}
	dial := &sentDialer{client: sent}
	s.Dialer = dial
	post := func(id string, mime string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mailbox/eas?Cmd=SendMail&DeviceId=phone", bytes.NewReader(composeWire(id, []byte(mime))))
		req.Header.Set(headerVersion, "14.1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	status := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		n, e := Decode(w.Body.Bytes())
		if e != nil {
			t.Fatalf("response %d %x: %v", w.Code, w.Body.Bytes(), e)
		}
		return n.ChildText(tokComposeStatus)
	}
	w := post("send-1", deviceMIME)
	if w.Code != 200 || w.Body.Len() != 0 || submit.calls != 1 || sent.calls != 1 {
		t.Fatalf("success %d/%s sends=%d copies=%d", w.Code, w.Body.String(), submit.calls, sent.calls)
	}
	if status(post("send-1", deviceMIME)) != "118" || submit.calls != 1 || sent.calls != 1 {
		t.Fatal("duplicate delivered")
	}
	if status(post("send-1", strings.Replace(deviceMIME, "hello", "other", 1))) != "101" {
		t.Fatal("ClientId payload substitution")
	}
	allowed = false
	if status(post("blocked", deviceMIME)) != "167" || submit.calls != 1 {
		t.Fatal("billing gate bypass")
	}
	allowed = true
	if status(post("forged", strings.Replace(deviceMIME, "From: a@eas.test", "From: other@eas.test", 1))) != "112" {
		t.Fatal("forged From")
	}
	submit.err = webmail.ErrSubmitUncertain
	if status(post("uncertain", deviceMIME)) != "118" {
		t.Fatal("uncertain result")
	}
	submit.err = nil
	before := submit.calls
	if status(post("uncertain", deviceMIME)) != "118" || submit.calls != before {
		t.Fatal("uncertain replay delivered")
	}
	dial.err = webmail.ErrDialFailed
	w = post("sent-pending", deviceMIME)
	if w.Body.Len() != 0 {
		t.Fatal("SMTP accepted not success")
	}
	before = submit.calls
	copies := sent.calls
	dial.err = nil
	if status(post("sent-pending", deviceMIME)) != "118" || submit.calls != before || sent.calls != copies+1 {
		t.Fatal("Sent recovery resubmitted SMTP or missed copy")
	}
	// A second mailbox using the same DeviceId/ClientId cannot use this receipt.
	tx, e := f.app.MailboxTx(t.Context(), f.tenant, f.mailboxB)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	var count int
	e = tx.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND kind='eas_sendmail'", f.tenant, f.mailboxA).Scan(&count)
	if e != nil || count != 0 {
		t.Fatalf("RLS receipt leak count=%d err=%v", count, e)
	}
}

func TestSendMailSharedSMTPWire(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA, Address: "a@eas.test", TokenHash: "smtp"}
	r, s := newEASRouter(t, f, sess)
	vault := webmail.NewMemoryVault()
	vault.Put(sess.TokenHash, "secret") //nolint:staticcheck // test adapter seeding, Put exists for this
	s.Vault = vault
	s.MaySend = func(context.Context, int64, int64) (bool, error) { return true, nil }
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var mu sync.Mutex
	var recipients []string
	var data string
	done := make(chan error, 1)
	go func() {
		conn, e := ln.Accept()
		if e != nil {
			done <- e
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 localhost ESMTP\r\n")
		for {
			line, e := br.ReadString('\n')
			if e != nil {
				done <- e
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_, _ = io.WriteString(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH"):
				_, _ = io.WriteString(conn, "235 authenticated\r\n")
			case strings.HasPrefix(line, "MAIL FROM"):
				_, _ = io.WriteString(conn, "250 ok\r\n")
			case strings.HasPrefix(line, "RCPT TO"):
				mu.Lock()
				recipients = append(recipients, line)
				mu.Unlock()
				_, _ = io.WriteString(conn, "250 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				_, _ = io.WriteString(conn, "354 continue\r\n")
				var b strings.Builder
				for {
					part, e := br.ReadString('\n')
					if e != nil {
						done <- e
						return
					}
					if part == ".\r\n" {
						break
					}
					b.WriteString(part)
				}
				mu.Lock()
				data = b.String()
				mu.Unlock()
				_, _ = io.WriteString(conn, "250 queued\r\n")
			case strings.HasPrefix(line, "QUIT"):
				_, _ = io.WriteString(conn, "221 bye\r\n")
				done <- nil
				return
			default:
				_, _ = io.WriteString(conn, "250 ok\r\n")
			}
		}
	}()
	s.Submitter = webmail.NetSubmitter{Addr: ln.Addr().String(), Timeout: 5 * time.Second}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mailbox/eas?Cmd=SendMail&DeviceId=wire", bytes.NewReader(composeWire("wire-1", []byte(deviceMIME))))
	req.Header.Set(headerVersion, "14.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("send %d %x", w.Code, w.Body.Bytes())
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	defer mu.Unlock()
	decoded, parseErr := parseOutgoingMIME([]byte(data))
	if parseErr != nil || len(decoded.Bcc) != 0 || len(decoded.Attachments) != 1 || !bytes.Equal(decoded.Attachments[0].Content, []byte{0, 1, 2, 3}) {
		t.Fatalf("SMTP MIME bytes changed: %+v %v", decoded, parseErr)
	}
	if len(recipients) != 2 || !strings.Contains(strings.Join(recipients, ""), "hidden@example.test") || strings.Contains(strings.ToLower(data), "\r\nbcc:") || !strings.Contains(data, "test.bin") {
		t.Fatalf("SMTP recipients=%v data=%s", recipients, data)
	}
}

func (f *sentFake) FolderUIDValidity(context.Context, string) (uint32, error) { return f.uidv, nil }
func (f *sentFake) GetMessage(context.Context, string, uint32) (*webmail.Message, error) {
	return f.original, nil
}
func (f *sentFake) GetAttachment(context.Context, string, uint32, string) (string, string, []byte, error) {
	return "original.bin", "application/octet-stream", []byte{4, 5, 6}, nil
}

func TestSmartReplyForwardSourceAndComposition(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA, Address: "a@eas.test", TokenHash: "smart"}
	r, s := newEASRouter(t, f, sess)
	vault := webmail.NewMemoryVault()
	vault.Put(sess.TokenHash, "password") //nolint:staticcheck // test adapter seeding, Put exists for this
	s.Vault = vault
	submit := &sendFake{}
	s.Submitter = submit
	s.MaySend = func(context.Context, int64, int64) (bool, error) { return true, nil }
	original := &webmail.Message{MessageSummary: webmail.MessageSummary{UID: 42, MessageID: "<original@eas.test>"}, UIDValidity: 99, Text: "original plain", HTML: "<p>original html</p>", Attachments: []webmail.AttachmentMeta{{PartID: "2", Filename: "original.bin"}}}
	client := &sentFake{original: original, uidv: 99}
	s.Dialer = &sentDialer{client: client}
	if err := UpsertDevice(t.Context(), f.platform, sess, Device{DeviceID: "phone", ProtocolVersion: "14.1"}); err != nil {
		t.Fatal(err)
	}
	epoch, err := store.New(f.platform.Pool).PlatformRestoreSyncEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = saveCollectionState(t.Context(), f.platform, sess, "phone", "e:INBOX", collectionState{Epoch: epoch, UIDValidity: 99, ChangeSeq: 1}); err != nil {
		t.Fatal(err)
	}
	tx, err := f.platform.MailboxTx(t.Context(), f.tenant, f.mailboxA)
	if err != nil {
		t.Fatal(err)
	}
	err = replaceItemsTx(t.Context(), tx, sess, "phone", "e:INBOX", map[string]itemRow{"42": {ItemID: "42", Fingerprint: "source"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	post := func(cmd, id, folder string, replace bool) *httptest.ResponseRecorder {
		n, err := Decode(composeWire(id, []byte(deviceMIME)))
		if err != nil {
			t.Fatal(err)
		}
		n.Token = composeCommandToken(cmd)
		n.Add(Element(21, 0x0B, Text(21, 0x0C, folder), Text(21, 0x0D, "42")))
		if replace {
			n.Add(Element(21, 0x09))
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mailbox/eas?Cmd="+cmd+"&DeviceId=phone", bytes.NewReader(Encode(n)))
		req.Header.Set(headerVersion, "14.1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	w := post(CmdSmartReply, "reply", "e:INBOX", false)
	if w.Code != 200 || w.Body.Len() != 0 || submit.calls != 1 {
		t.Fatalf("reply %d %x", w.Code, w.Body.Bytes())
	}
	if !strings.Contains(submit.last.Text, "original plain") || submit.last.InReplyTo != "<original@eas.test>" || len(submit.last.Attachments) != 1 {
		t.Fatalf("reply %+v", submit.last)
	}
	w = post(CmdSmartForward, "forward", "e:INBOX", false)
	if w.Body.Len() != 0 || len(submit.last.Attachments) != 2 || !bytes.Equal(submit.last.Attachments[1].Content, []byte{4, 5, 6}) {
		t.Fatalf("forward %x %+v", w.Body.Bytes(), submit.last)
	}
	w = post(CmdSmartForward, "replace", "e:INBOX", true)
	if w.Body.Len() != 0 || strings.Contains(submit.last.Text, "original plain") || len(submit.last.Attachments) != 1 {
		t.Fatal("ReplaceMime merged source")
	}
	before := submit.calls
	w = post(CmdSmartReply, "foreign", "e:OTHER", false)
	n, e := Decode(w.Body.Bytes())
	if e != nil || n.Token != 0x07 || n.ChildText(tokComposeStatus) != "106" || submit.calls != before {
		t.Fatalf("source boundary %x %v", w.Body.Bytes(), e)
	}
	client.uidv = 100
	w = post(CmdSmartForward, "reused-uid", "e:INBOX", false)
	n, e = Decode(w.Body.Bytes())
	if e != nil || n.ChildText(tokComposeStatus) != "106" || submit.calls != before {
		t.Fatal("UIDVALIDITY bypass")
	}
}
