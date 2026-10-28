package collab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	domain "github.com/bushixiucai/urmail/internal/domain/collab"
)

// SchedulingMail is an invitation parsed from the actual mailbox RFC822 bytes.
// Verified means a complete-body, strictly aligned DKIM signature protects the
// critical MIME/routing headers. Authentication-Results is never trusted.
type SchedulingMail struct {
	Message                 domain.SchedulingMessage
	Digest, From, MessageID string
	Recipients              []string
	Verified                bool
	VerificationReason      string
}

// TXTLookup permits a controlled DNS resolver in isolated cryptographic tests.
// Production defaults to the system resolver bound to the request deadline.
type TXTLookup func(context.Context, string) ([]string, error)

// ParseSchedulingMail limits MIME nesting, part count and bytes before parsing
// an iTIP component. It does not execute alarm actions or remote resources.
func ParseSchedulingMail(ctx context.Context, raw []byte, lookup TXTLookup) (SchedulingMail, error) {
	var out SchedulingMail
	if len(raw) == 0 || len(raw) > 2<<20 {
		return out, errors.New("scheduling message exceeds limit")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	headers := textproto.MIMEHeader(msg.Header)
	for _, key := range []string{"From", "To", "Content-Type", "Message-Id"} {
		if len(headers.Values(key)) != 1 {
			return out, errors.New("ambiguous scheduling headers")
		}
	}
	for _, key := range []string{"Cc", "Content-Transfer-Encoding", "Mime-Version"} {
		if len(headers.Values(key)) > 1 {
			return out, errors.New("ambiguous MIME headers")
		}
	}
	from, err := mail.ParseAddressList(msg.Header.Get("From"))
	if err != nil || len(from) != 1 {
		return out, errors.New("invalid scheduling sender")
	}
	out.From = strings.ToLower(from[0].Address)
	to, err := mail.ParseAddressList(msg.Header.Get("To"))
	if err != nil {
		return out, errors.New("invalid scheduling recipients")
	}
	for _, address := range to {
		out.Recipients = append(out.Recipients, strings.ToLower(address.Address))
	}
	out.MessageID = msg.Header.Get("Message-Id")
	if len(out.MessageID) > 998 || !strings.HasPrefix(out.MessageID, "<") || !strings.HasSuffix(out.MessageID, ">") || strings.ContainsAny(out.MessageID[1:len(out.MessageID)-1], "<> \t\r\n\x00") {
		return out, errors.New("invalid scheduling Message-ID")
	}
	var calendar []byte
	parts := 0
	var walk func(textproto.MIMEHeader, io.Reader, int) error
	walk = func(header textproto.MIMEHeader, body io.Reader, depth int) error {
		parts++
		if depth > 8 || parts > 100 {
			return errors.New("MIME complexity limit")
		}
		media, params, e := mime.ParseMediaType(header.Get("Content-Type"))
		if e != nil {
			return e
		}
		switch strings.ToLower(header.Get("Content-Transfer-Encoding")) {
		case "base64":
			body = base64.NewDecoder(base64.StdEncoding, body)
		case "quoted-printable":
			body = quotedprintable.NewReader(body)
		case "", "7bit", "8bit", "binary":
		default:
			return errors.New("unsupported MIME encoding")
		}
		if strings.HasPrefix(media, "multipart/") {
			if params["boundary"] == "" {
				return errors.New("missing MIME boundary")
			}
			reader := multipart.NewReader(body, params["boundary"])
			for {
				part, e := reader.NextPart()
				if errors.Is(e, io.EOF) {
					break
				}
				if e != nil {
					return e
				}
				if e = walk(part.Header, part, depth+1); e != nil {
					return e
				}
			}
			return nil
		}
		if media != "text/calendar" {
			return nil
		}
		if len(calendar) > 0 {
			return errors.New("ambiguous calendar parts")
		}
		calendar, e = io.ReadAll(io.LimitReader(body, 200001))
		if e != nil {
			return e
		}
		if len(calendar) > 200000 {
			return errors.New("calendar exceeds limit")
		}
		return nil
	}
	if err = walk(headers, msg.Body, 0); err != nil {
		return out, err
	}
	if len(calendar) == 0 {
		return out, errors.New("calendar part not found")
	}
	out.Message, err = domain.ParseSchedulingMessage(string(calendar))
	if err != nil {
		return out, err
	}
	expected := out.Message.Organizer
	if out.Message.Method == domain.MethodReply {
		expected = out.Message.Reply.Replier
	}
	if !strings.EqualFold(out.From, expected) {
		return out, errors.New("scheduling sender differs from claimed identity")
	}
	sum := sha256.Sum256(raw)
	out.Digest = hex.EncodeToString(sum[:])
	out.VerificationReason = "DKIM_UNVERIFIED"
	dnsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if lookup == nil {
		lookup = func(ctx context.Context, name string) ([]string, error) {
			return net.DefaultResolver.LookupTXT(ctx, name)
		}
	}
	verifications, verifyErr := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{MaxVerifications: 4, LookupTXT: func(name string) ([]string, error) {
		if len(name) > 253 {
			return nil, errors.New("invalid DKIM DNS name")
		}
		records, e := lookup(dnsCtx, name)
		if len(records) > 16 {
			return nil, errors.New("DKIM TXT record limit")
		}
		size := 0
		for _, record := range records {
			size += len(record)
		}
		if size > 65536 {
			return nil, errors.New("DKIM TXT byte limit")
		}
		return records, e
	}})
	if verifyErr != nil {
		return out, nil
	}
	_, domainName, ok := strings.Cut(out.From, "@")
	if !ok {
		return out, errors.New("invalid sender domain")
	}
	for _, verification := range verifications {
		if verification == nil || verification.Err != nil || !strings.EqualFold(verification.Domain, domainName) {
			continue
		}
		signed := map[string]bool{}
		for _, key := range verification.HeaderKeys {
			signed[strings.ToLower(key)] = true
		}
		complete := true
		for _, key := range []string{"from", "to", "content-type", "message-id", "content-transfer-encoding", "mime-version", "cc"} {
			if headers.Get(key) != "" && !signed[key] {
				complete = false
			}
		}
		if complete {
			out.Verified = true
			out.VerificationReason = "DKIM_VERIFIED"
			break
		}
	}
	return out, nil
}
