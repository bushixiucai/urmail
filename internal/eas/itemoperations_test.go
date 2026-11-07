package eas

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

func (f *sentFake) GetRawMessage(context.Context, string, uint32) ([]byte, uint32, error) {
	return []byte(deviceMIME), f.uidv, nil
}

func TestItemOperationsNormativeRequest(t *testing.T) {
	// Page20 ItemOperations/Fetch/Store then page0 CollectionId/ServerId;
	// independently transcribed from MS-ASWBXML, not this codec's constants.
	raw, _ := hex.DecodeString("03016a000014454647034d61696c626f78000100005203653a494e424f5800014d03343200010101")
	req, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.Page != 20 || req.Token != 5 || pageText(req.Children[0], 0, 0x12) != "e:INBOX" || pageText(req.Children[0], 0, 0x0D) != "42" {
		t.Fatalf("normative request %+v", req)
	}
	if !bytes.Equal(Encode(req), raw) {
		t.Fatal("wire drift")
	}
}

func TestItemOperationsFullBodyAttachmentsAndIsolation(t *testing.T) {
	f := newPGFixture(t)
	sess := webmail.Session{TenantID: f.tenant, MailboxID: f.mailboxA, Address: "a@eas.test", TokenHash: "fetch"}
	r, s := newEASRouter(t, f, sess)
	vault := webmail.NewMemoryVault()
	vault.Put(sess.TokenHash, "password") //nolint:staticcheck // test adapter seeding, Put exists for this
	s.Vault = vault
	original := &webmail.Message{MessageSummary: webmail.MessageSummary{UID: 42, MessageID: "<original@eas.test>", Subject: "full"}, UIDValidity: 99, Text: strings.Repeat("complete body ", 200), HTML: "<p>complete html</p>", Attachments: []webmail.AttachmentMeta{{PartID: "2", Filename: "original.bin", Size: 3, ContentType: "application/octet-stream"}}}
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
	post := func(fetch *Node) *Node {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/mailbox/eas?Cmd=ItemOperations&DeviceId=phone", bytes.NewReader(Encode(Element(20, 5, fetch))))
		req.Header.Set(headerVersion, "14.1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		n := decodeResponse(t, w)
		response := nodeOnPage(n, 20, 0x0E)
		if response == nil || len(response.Children) != 1 {
			t.Fatalf("missing fetch %x", w.Body.Bytes())
		}
		return response.Children[0]
	}
	fetchBody := func(typ string) *Node {
		options := Element(20, 8, Element(17, 5, Text(17, 6, typ)))
		if typ == "4" {
			options.Add(Text(0, 0x22, "2"))
		}
		return Element(20, 6, Text(20, 7, "Mailbox"), Text(0, 0x12, "e:INBOX"), Text(0, 0x0D, "42"), options)
	}
	response := post(fetchBody("1"))
	if pageText(response, 20, 0x0D) != "1" {
		t.Fatalf("body status %+v", response)
	}
	props := nodeOnPage(response, 20, 0x0B)
	body := nodeOnPage(props, 17, 0x0A)
	if pageText(body, 17, 0x0B) != original.Text || pageText(body, 17, 0x0D) != "0" {
		t.Fatal("full text truncated")
	}
	attachments := nodeOnPage(props, 17, 0x0E)
	if attachments == nil || len(attachments.Children) != 1 {
		t.Fatal("attachment metadata missing")
	}
	ref := pageText(attachments.Children[0], 17, 0x11)
	response = post(fetchBody("2"))
	body = nodeOnPage(nodeOnPage(response, 20, 0x0B), 17, 0x0A)
	if pageText(body, 17, 0x0B) != original.HTML {
		t.Fatal("HTML changed")
	}
	response = post(fetchBody("4"))
	body = nodeOnPage(nodeOnPage(response, 20, 0x0B), 17, 0x0A)
	if !bytes.Equal(nodeOnPage(body, 17, 0x0B).Opaque, []byte(deviceMIME)) {
		t.Fatal("original MIME bytes changed")
	}
	fetchAttachment := func(value string) *Node { return Element(20, 6, Text(20, 7, "Mailbox"), Text(17, 0x11, value)) }
	response = post(fetchAttachment(ref))
	data := nodeOnPage(nodeOnPage(response, 20, 0x0B), 20, 0x0C)
	if pageText(response, 20, 0x0D) != "1" || data == nil || !bytes.Equal(data.Opaque, []byte{4, 5, 6}) {
		t.Fatal("attachment bytes changed")
	}
	ranged := fetchAttachment(ref).Add(Element(20, 8, Text(20, 9, "1-2")))
	response = post(ranged)
	data = nodeOnPage(nodeOnPage(response, 20, 0x0B), 20, 0x0C)
	if data == nil || !bytes.Equal(data.Opaque, []byte{5, 6}) {
		t.Fatal("range bytes changed")
	}
	badRange := fetchAttachment(ref).Add(Element(20, 8, Text(20, 9, "5-10")))
	if pageText(post(badRange), 20, 0x0D) != "8" {
		t.Fatal("invalid range accepted")
	}
	foreign := encodeFileReference(fileReference{Tenant: f.tenant, Mailbox: f.mailboxB, Device: "phone", Folder: "e:INBOX", UID: 42, UIDValidity: 99, Epoch: epoch, Part: "2"})
	if pageText(post(fetchAttachment(foreign)), 20, 0x0D) != "15" {
		t.Fatal("cross-mailbox reference accepted")
	}
	foreign = encodeFileReference(fileReference{Tenant: f.tenant + 1, Mailbox: f.mailboxA, Device: "phone", Folder: "e:INBOX", UID: 42, UIDValidity: 99, Epoch: epoch, Part: "2"})
	if pageText(post(fetchAttachment(foreign)), 20, 0x0D) != "15" {
		t.Fatal("cross-tenant reference accepted")
	}
	limited := s.fetchMailItem(t.Context(), sess, "phone", client, fetchAttachment(ref), 10)
	if pageText(limited, 20, 0x0D) != "11" {
		t.Fatal("response budget bypass")
	}
	client.uidv = 100
	if pageText(post(fetchAttachment(ref)), 20, 0x0D) != "6" {
		t.Fatal("stale UIDVALIDITY attachment accepted")
	}
}
