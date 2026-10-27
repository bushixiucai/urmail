package eas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"golang.org/x/net/html/charset"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

const pageComposeMail = byte(21)
const tokSendMail = byte(0x05)
const tokComposeClientID = byte(0x11)
const tokComposeMIME = byte(0x10)
const tokSaveInSentItems = byte(0x08)
const tokComposeStatus = byte(0x12)

type sendReceipt struct {
	Epoch    int64  `json:"epoch"`
	Digest   string `json:"digest"`
	SentCopy string `json:"sent_copy,omitempty"`
	Raw      []byte `json:"raw,omitempty"`
}

func sendFailure(c *gin.Context, status string) {
	token := composeCommandToken(c.Query("Cmd"))
	c.Data(http.StatusOK, ContentTypeWBXML, Encode(Element(pageComposeMail, token, Text(pageComposeMail, tokComposeStatus, status))))
}

// handleSendMail uses the same SMTP submitter and Sent writer as native Webmail.
// The receipt commits before SMTP, so an interrupted DATA exchange is never
// automatically retried even if no final receipt could be recorded.
func (s *Server) handleSendMail(c *gin.Context, sess Session, deviceID string, req *Node) {
	if req.Page != pageComposeMail || req.Token != composeCommandToken(c.Query("Cmd")) {
		sendFailure(c, "101")
		return
	}
	id := req.ChildText(tokComposeClientID)
	mimeNode := req.Child(tokComposeMIME)
	if id == "" || len(id) > 40 || mimeNode == nil {
		sendFailure(c, "101")
		return
	}
	for _, n := range req.Children {
		if n.Page != pageComposeMail {
			sendFailure(c, "101")
			return
		}
		if n.Token == 0x13 && n.Text != "" {
			sendFailure(c, "166")
			return
		}
	}
	raw := mimeNode.Opaque
	if raw == nil {
		raw = []byte(mimeNode.Text)
	}
	outgoing, err := parseOutgoingMIME(raw)
	if err != nil {
		sendFailure(c, "107")
		return
	}
	if len(outgoing.To)+len(outgoing.Cc)+len(outgoing.Bcc) == 0 {
		sendFailure(c, "119")
		return
	}
	if s.Submitter == nil || s.Vault == nil || s.MaySend == nil {
		sendFailure(c, "110")
		return
	}
	if !strings.EqualFold(outgoing.From, sess.Address) {
		if s.SenderAllowed == nil {
			sendFailure(c, "112")
			return
		}
		allowed, e := s.SenderAllowed(c.Request.Context(), sess.TenantID, sess.Address, outgoing.From)
		if e != nil {
			sendFailure(c, "111")
			return
		}
		if !allowed {
			sendFailure(c, "112")
			return
		}
	}
	allowed, err := s.MaySend(c.Request.Context(), sess.TenantID, sess.MailboxID)
	if err != nil {
		sendFailure(c, "111")
		return
	}
	if !allowed {
		sendFailure(c, "167")
		return
	}
	secret, ok := s.Vault.Get(sess.TokenHash)
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	if req.Token != tokSendMail {
		if status := s.prepareSmart(c.Request.Context(), sess, deviceID, secret, req, &outgoing); status != "" {
			sendFailure(c, status)
			return
		}
	}
	digest := sha256.Sum256(Encode(req))
	key := sha256.Sum256([]byte(deviceID + "\x00" + id))
	if outgoing.MessageID == "" {
		_, domain, _ := strings.Cut(sess.Address, "@")
		outgoing.MessageID = "<eas-" + hex.EncodeToString(key[:]) + "@" + domain + ">"
	}
	epoch, err := store.New(s.Pool.Pool).PlatformRestoreSyncEpoch(c.Request.Context())
	if err != nil {
		sendFailure(c, "111")
		return
	}
	receipt := sendReceipt{Digest: hex.EncodeToString(digest[:]), Epoch: epoch}
	payload, _ := json.Marshal(receipt)
	ctx := c.Request.Context()
	tx, err := s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		sendFailure(c, "111")
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	jobID, err := q.ClaimEASSend(ctx, store.ClaimEASSendParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, IdempotencyKey: hex.EncodeToString(key[:]), Payload: payload})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, e := q.GetEASSend(ctx, store.GetEASSendParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, IdempotencyKey: hex.EncodeToString(key[:])})
		if e != nil {
			sendFailure(c, "111")
			return
		}
		var old sendReceipt
		if json.Unmarshal(existing.Payload, &old) != nil || old.Digest != receipt.Digest {
			sendFailure(c, "101")
			return
		}
		if existing.Status == "failed" {
			sendFailure(c, "120")
			return
		}
		if existing.Status == "done" && old.SentCopy == "pending" && old.Epoch == epoch {
			if tx.Commit(ctx) == nil {
				s.recoverSentCopy(sess, secret, existing.ID, old)
			}
		}
		// An uncertain attempt may already have reached the recipient. Status 118
		// prevents clients from retrying the same ClientId as another SMTP message.
		sendFailure(c, "118")
		return
	}
	if err != nil {
		sendFailure(c, "111")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		sendFailure(c, "111")
		return
	}
	submission, err := s.Submitter.Send(ctx, sess.Address, secret, outgoing)
	if err != nil {
		status := "failed"
		if errors.Is(err, webmail.ErrSubmitUncertain) {
			status = "uncertain"
		}
		_ = s.finishSend(sess, jobID, status, receipt)
		if status == "uncertain" {
			sendFailure(c, "118")
		} else {
			sendFailure(c, "120")
		}
		return
	}
	// SMTP success is persisted independently of the request cancellation. A
	// failure here retains the pre-send uncertain receipt, still preventing resend.
	receipt.SentCopy = "not_requested"
	if req.Child(tokSaveInSentItems) != nil {
		receipt.SentCopy = "pending"
		receipt.Raw = submission.Raw
	}
	if err = s.finishSend(sess, jobID, "done", receipt); err != nil {
		s.logWarn("SMTP accepted; receipt uncertain", "job", jobID, "err", err)
	} else if receipt.SentCopy == "pending" {
		s.recoverSentCopy(sess, secret, jobID, receipt)
	}

	// MS-ASCMD SendMail succeeds with an empty 200, not a Status=1 document.
	c.Status(http.StatusOK)
}

func (s *Server) finishSend(sess Session, id int64, status string, receipt sendReceipt) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	payload, _ := json.Marshal(receipt)
	err = store.New(tx).FinishEASSend(ctx, store.FinishEASSendParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: id, Status: status, Payload: payload})
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// parseOutgoingMIME decodes the device's RFC 5322 MIME into the shared SMTP
// model. Bcc is an envelope recipient only: NetSubmitter omits its header.
func parseOutgoingMIME(raw []byte) (webmail.Outgoing, error) {
	var out webmail.Outgoing
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	from, err := mail.ParseAddressList(msg.Header.Get("From"))
	if err != nil || len(from) != 1 {
		return out, errors.New("one From required")
	}
	out.From = from[0].Address
	for _, header := range []string{"Sender", "Resent-From", "Resent-Sender"} {
		if msg.Header.Get(header) != "" {
			return out, errors.New("unsupported alternate sender")
		}
	}
	for _, field := range []struct {
		name   string
		target *[]string
	}{{"To", &out.To}, {"Cc", &out.Cc}, {"Bcc", &out.Bcc}} {
		if msg.Header.Get(field.name) == "" {
			continue
		}
		values, e := mail.ParseAddressList(msg.Header.Get(field.name))
		if e != nil {
			return out, e
		}
		for _, v := range values {
			*field.target = append(*field.target, v.Address)
		}
	}
	decoder := &mime.WordDecoder{CharsetReader: charset.NewReaderLabel}
	out.Subject, err = decoder.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		return out, err
	}
	out.MessageID = msg.Header.Get("Message-ID")
	out.InReplyTo = msg.Header.Get("In-Reply-To")
	out.References = msg.Header.Get("References")
	parts := 0
	err = decodeMIMEPart(msg.Header, msg.Body, &out, 0, &parts)
	return out, err
}

func decodeMIMEPart(h mail.Header, r io.Reader, out *webmail.Outgoing, depth int, parts *int) error {
	*parts++
	if depth > 12 || *parts > 100 {
		return errors.New("MIME nesting limit")
	}
	contentType := h.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return err
	}
	if strings.HasPrefix(media, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return errors.New("missing MIME boundary")
		}
		mr := multipart.NewReader(r, boundary)
		for {
			part, e := mr.NextRawPart()
			if errors.Is(e, io.EOF) {
				return nil
			}
			if e != nil {
				return e
			}
			e = decodeMIMEPart(mail.Header(part.Header), part, out, depth+1, parts)
			_ = part.Close()
			if e != nil {
				return e
			}
		}
	}
	switch strings.ToLower(h.Get("Content-Transfer-Encoding")) {
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	case "", "7bit", "8bit", "binary":
	default:
		return errors.New("unsupported transfer encoding")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxRequestBodySize+1))
	if err != nil {
		return err
	}
	if len(data) > maxRequestBodySize {
		return errors.New("MIME part too large")
	}
	disposition, dp, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	filename := dp["filename"]
	if filename == "" {
		filename = params["name"]
	}
	if disposition == "attachment" || filename != "" || (media != "text/plain" && media != "text/html") {
		if filename == "" {
			filename = "attachment"
		}
		out.Attachments = append(out.Attachments, webmail.OutgoingAttachment{Filename: filename, ContentType: media, Content: data, ContentID: strings.Trim(h.Get("Content-ID"), "<>"), Inline: disposition == "inline"})
		return nil
	}
	if label := params["charset"]; label != "" {
		reader, e := charset.NewReaderLabel(label, bytes.NewReader(data))
		if e != nil {
			return e
		}
		data, e = io.ReadAll(io.LimitReader(reader, maxRequestBodySize+1))
		if e != nil {
			return e
		}
		if len(data) > maxRequestBodySize {
			return errors.New("decoded MIME part too large")
		}
	}
	if media == "text/html" {
		out.HTML += string(data)
	} else {
		out.Text += string(data)
	}
	return nil
}

func (s *Server) recoverSentCopy(sess Session, secret string, id int64, receipt sendReceipt) {
	if s.Dialer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	epoch, err := store.New(s.Pool.Pool).PlatformRestoreSyncEpoch(ctx)
	if err != nil || receipt.Epoch != epoch {
		return
	}
	client, err := s.Dialer.Dial(ctx, sess.Address, secret)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	tx, err := s.Pool.MailboxTx(ctx, sess.TenantID, sess.MailboxID)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	// Lock by receipt id as well as both principal dimensions.
	current, err := q.LockEASSendCopy(ctx, store.LockEASSendCopyParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: id})
	if err != nil || current.Status != "done" {
		return
	}
	if json.Unmarshal(current.Payload, &receipt) != nil || receipt.SentCopy != "pending" || len(receipt.Raw) == 0 {
		return
	}
	receipt.SentCopy = "appending"
	payload, _ := json.Marshal(receipt)
	if err = q.FinishEASSend(ctx, store.FinishEASSendParams{TenantID: sess.TenantID, MailboxID: sess.MailboxID, ID: id, Status: "done", Payload: payload}); err != nil {
		return
	}
	if tx.Commit(ctx) != nil {
		return
	}
	_, err = client.SaveSentCopy(ctx, receipt.Raw)
	if err != nil {
		receipt.SentCopy = "uncertain"
		s.logWarn("Sent APPEND requires reconciliation", "job", id, "err", err)
	} else {
		receipt.SentCopy = "saved"
		receipt.Raw = nil
	}
	if err = s.finishSend(sess, id, "done", receipt); err != nil {
		s.logWarn("Sent receipt requires reconciliation", "job", id, "err", err)
	}
}

func composeCommandToken(cmd string) byte {
	switch cmd {
	case "SmartReply":
		return 0x07
	case "SmartForward":
		return 0x06
	default:
		return tokSendMail
	}
}

func (s *Server) prepareSmart(ctx context.Context, sess Session, deviceID, secret string, req *Node, out *webmail.Outgoing) string {
	source := req.Child(0x0B)
	if source == nil || source.Page != pageComposeMail || source.Child(0x0E) != nil || source.Child(0x0F) != nil {
		return "106"
	}
	folderID, itemID := source.ChildText(0x0C), source.ChildText(0x0D)
	if !strings.HasPrefix(folderID, "e:") || !validUID(itemID) {
		return "106"
	}
	state, err := loadCollectionState(ctx, s.Pool, sess, deviceID, folderID)
	if err != nil || state.UIDValidity <= 0 {
		return "106"
	}

	items, err := loadItems(ctx, s.Pool, sess, deviceID, folderID)
	if err != nil {
		return "111"
	}
	if _, known := items[itemID]; !known {
		return "106"
	}
	if s.Dialer == nil {
		return "110"
	}
	client, err := s.Dialer.Dial(ctx, sess.Address, secret)
	if err != nil {
		return "111"
	}
	defer func() { _ = client.Close() }()
	folder := strings.TrimPrefix(folderID, "e:")
	uidv, err := client.FolderUIDValidity(ctx, folder)
	if err != nil {
		return "111"
	}
	if int64(uidv) != state.UIDValidity {
		return "106"
	}
	uid, _ := strconv.ParseUint(itemID, 10, 32)
	original, err := client.GetMessage(ctx, folder, uint32(uid))
	if err != nil || original == nil {
		return "106"
	}
	if original.UIDValidity != 0 && original.UIDValidity != uidv {
		return "106"
	}
	if original.MessageID != "" {
		out.InReplyTo = original.MessageID
		if out.References == "" {
			out.References = original.MessageID
		}
	}
	if req.Child(0x09) != nil {
		return ""
	}
	// Without ReplaceMime the server supplies the complete source body. Reply
	// recipients remain exactly those selected by the client (including ReplyAll).
	if original.Text != "" {
		out.Text += "\r\n\r\n" + original.Text
	}
	if out.HTML != "" || original.HTML != "" {
		if out.HTML == "" {
			out.HTML = "<pre>" + html.EscapeString(out.Text) + "</pre>"
		}
		body := original.HTML
		if body == "" {
			body = "<pre>" + html.EscapeString(original.Text) + "</pre>"
		}
		out.HTML += "<br><blockquote>" + body + "</blockquote>"
	}
	if req.Token == 0x06 {
		if original.AttachmentsCapped {
			return "121"
		}
		total := 0
		for _, att := range original.Attachments {
			name, ct, data, e := client.GetAttachment(ctx, folder, uint32(uid), att.PartID)
			if e != nil {
				return "121"
			}
			total += len(data)
			if total > maxRequestBodySize {
				return "113"
			}
			out.Attachments = append(out.Attachments, webmail.OutgoingAttachment{Filename: name, ContentType: ct, Content: data, ContentID: att.ContentID, Inline: att.ContentID != ""})
		}
	}
	return ""
}
