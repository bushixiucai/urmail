// Package mailauth binds receive-time authentication facts to immutable mail
// content. An Authentication-Results header is never evidence of provenance.
package mailauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"
)

// Receive proof wire format and capture bounds are shared by the milter and reader.
const (
	HeaderName     = "X-UrMail-Receive-Authentication"
	MaxBodyBytes   = 16 << 20
	MaxHeaderBytes = 128 << 10
	MaxHeaders     = 512
	proofPurpose   = "urmail.receive-auth.v1"
	Source         = "urmail-receive-dkim"
)

// Header is one ordered message header observed by the receive milter.
type Header struct{ Name, Value string }

// Secrets is the existing platform authenticated-encryption interface.
type Secrets interface {
	EncryptString(string) (string, error)
	DecryptString(string) (string, error)
}

// Result describes DKIM verification at receipt. SPF is deliberately not
// inferred: no trusted SPF checker supplies evidence to this component.
type Result struct {
	Status    string `json:"status"`
	Source    string `json:"source,omitempty"`
	Reason    string `json:"reason"`
	Domain    string `json:"domain,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

type proof struct {
	Purpose string `json:"purpose"`
	Digest  string `json:"digest"`
	Result  Result `json:"result"`
}

// Receiver performs bounded verification in the existing receive milter. The
// platform secrets box authenticates the proof; no new key or database exists.
type Receiver struct {
	Secrets   Secrets
	LookupTXT func(context.Context, string) ([]string, error)
	Now       func() time.Time
}

var contentHeaders = []string{"From", "Sender", "To", "Cc", "Reply-To", "Subject", "Date", "Message-Id", "Mime-Version", "Content-Type", "Content-Transfer-Encoding", "Content-Disposition", "Content-Id"}

func digest(headers mail.Header, body []byte) string {
	type field struct {
		Name   string
		Values []string
	}
	fields := make([]field, 0, len(contentHeaders))
	for _, name := range contentHeaders {
		values := headers[name]
		canonical := make([]string, 0, len(values))
		for _, value := range values {
			canonical = append(canonical, strings.Join(strings.Fields(value), " "))
		}
		fields = append(fields, field{Name: name, Values: canonical})
	}
	encoded, _ := json.Marshal(fields)
	h := sha256.New()
	_, _ = h.Write(encoded)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(h.Sum(nil))
}

// Attest accepts the exact headers and complete body observed over the local
// Postfix milter protocol. Sender-supplied authentication headers are ignored.
// A missing key, incomplete capture or verification error never blocks delivery.
func (r *Receiver) Attest(ctx context.Context, headers []Header, body []byte) (string, error) {
	if r == nil || r.Secrets == nil || len(body) > MaxBodyBytes || len(headers) > MaxHeaders {
		return "", nil
	}
	var raw bytes.Buffer
	total := 0
	for _, header := range headers {
		total += len(header.Name) + len(header.Value) + 4
		if total > MaxHeaderBytes || strings.ContainsAny(header.Name, ":\r\n\x00") {
			return "", nil
		}
		raw.WriteString(header.Name + ": " + header.Value + "\r\n")
	}
	raw.WriteString("\r\n")
	raw.Write(body)
	message, err := mail.ReadMessage(bytes.NewReader(raw.Bytes()))
	if err != nil {
		return "", nil
	}
	result := Result{Status: "unknown", Source: Source, Reason: "dkim_missing"}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	result.CheckedAt = now.UTC().Format(time.RFC3339)
	from, fromErr := mail.ParseAddress(message.Header.Get("From"))
	if fromErr == nil && len(message.Header["From"]) == 1 {
		_, domain, ok := strings.Cut(from.Address, "@")
		if ok && domain != "" {
			lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			lookup := r.LookupTXT
			if lookup == nil {
				lookup = net.DefaultResolver.LookupTXT
			}
			checks, verifyErr := dkim.VerifyWithOptions(bytes.NewReader(raw.Bytes()), &dkim.VerifyOptions{
				MaxVerifications: 4,
				LookupTXT: func(name string) ([]string, error) {
					if len(name) > 253 {
						return nil, errors.New("DKIM name limit")
					}
					records, lookupErr := lookup(lookupCtx, name)
					totalBytes := 0
					for _, record := range records {
						totalBytes += len(record)
					}
					if len(records) > 16 || totalBytes > 65536 {
						return nil, errors.New("DKIM record limit")
					}
					return records, lookupErr
				},
			})
			if len(checks) > 0 {
				result.Reason = "dkim_unaligned"
			}
			temporary, failed := false, false
			for _, check := range checks {
				if check == nil || !strings.EqualFold(check.Domain, domain) {
					continue
				}
				result.Domain = strings.ToLower(domain)
				if check.Err == nil {
					result.Status, result.Reason = "pass", "dkim_verified"
					break
				}
				if dkim.IsTempFail(check.Err) {
					temporary = true
				} else {
					failed = true
				}
			}
			if result.Status != "pass" {
				if temporary {
					result.Status, result.Reason = "unknown", "dkim_temporary_error"
				} else if failed {
					result.Status, result.Reason = "fail", "dkim_verification_failed"
				}
			}
			if verifyErr != nil && result.Status != "pass" {
				result.Status, result.Reason = "unknown", "dkim_verification_incomplete"
			}
		}
	} else {
		result.Reason = "sender_ambiguous"
	}
	data, err := json.Marshal(proof{Purpose: proofPurpose, Digest: digest(message.Header, body), Result: result})
	if err != nil {
		return "", err
	}
	token, err := r.Secrets.EncryptString(string(data))
	// SecretsBox supports legacy plaintext elsewhere. Authentication proofs never do.
	if err != nil || !strings.HasPrefix(token, "enc:v1:") || len(token) > 2000 {
		return "", err
	}
	return token, nil
}

// Read verifies the sealed receive proof and its content binding. Importing or
// editing an arbitrary header, including Authentication-Results, cannot attest.
func Read(raw []byte, secrets Secrets) Result {
	unknown := Result{Status: "unknown", Reason: "receive_proof_missing"}
	if secrets == nil || len(raw) > MaxBodyBytes+MaxHeaderBytes {
		return unknown
	}
	boundary := bytes.Index(raw, []byte("\r\n\r\n"))
	if boundary < 0 {
		boundary = bytes.Index(raw, []byte("\n\n"))
	}
	if boundary < 0 || boundary > MaxHeaderBytes {
		return unknown
	}
	headerCount := 0
	for _, line := range bytes.Split(raw[:boundary], []byte("\n")) {
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			headerCount++
		}
	}
	if headerCount > MaxHeaders {
		return unknown
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return unknown
	}
	values := textproto.MIMEHeader(message.Header).Values(HeaderName)
	if len(values) != 1 {
		return unknown
	}
	token := strings.Join(strings.Fields(values[0]), "")
	if !strings.HasPrefix(token, "enc:v1:") || len(token) > 2000 {
		return Result{Status: "unknown", Reason: "receive_proof_invalid"}
	}
	data, err := secrets.DecryptString(token)
	if err != nil {
		return Result{Status: "unknown", Reason: "receive_proof_invalid"}
	}
	var claim proof
	if json.Unmarshal([]byte(data), &claim) != nil || claim.Purpose != proofPurpose || claim.Result.Source != Source || (claim.Result.Status != "pass" && claim.Result.Status != "fail" && claim.Result.Status != "unknown") {
		return Result{Status: "unknown", Reason: "receive_proof_invalid"}
	}
	body, err := io.ReadAll(io.LimitReader(message.Body, MaxBodyBytes+1))
	if err != nil || len(body) > MaxBodyBytes || claim.Digest != digest(message.Header, body) {
		return Result{Status: "unknown", Reason: "receive_proof_content_changed"}
	}
	return claim.Result
}
