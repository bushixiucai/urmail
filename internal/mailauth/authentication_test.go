package mailauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/bushixiucai/urmail/internal/secrets"
)

func fixture(t *testing.T, signingDomain string) (*Receiver, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: Alice <alice@example.test>\r\nTo: reader@urmail.test\r\nSubject: Authentication fixture\r\nDate: Fri, 30 Oct 2026 08:00:00 +0000\r\nMessage-ID: <auth-1@example.test>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nExact body\r\n"
	var signed bytes.Buffer
	if err = dkim.Sign(&signed, strings.NewReader(raw), &dkim.SignOptions{Domain: signingDomain, Selector: "test", Signer: private, HeaderKeys: []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Content-Type"}, HeaderCanonicalization: dkim.CanonicalizationRelaxed, BodyCanonicalization: dkim.CanonicalizationRelaxed}); err != nil {
		t.Fatal(err)
	}
	receiver := &Receiver{Secrets: box, Now: func() time.Time { return time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC) }, LookupTXT: func(_ context.Context, name string) ([]string, error) {
		if name != "test._domainkey."+signingDomain {
			return nil, errors.New("unexpected isolated DNS query")
		}
		return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(public)}, nil
	}}
	return receiver, signed.Bytes()
}

func attest(t *testing.T, receiver *Receiver, raw []byte) string {
	t.Helper()
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	headers := []Header{}
	for name, values := range parsed.Header {
		for _, value := range values {
			headers = append(headers, Header{Name: name, Value: value})
		}
	}
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatal(err)
	}
	token, err := receiver.Attest(t.Context(), headers, body)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func withProof(raw []byte, token string) []byte {
	return append([]byte(HeaderName+": "+token+"\r\n"), raw...)
}

func TestReceiveProofAuthenticatesAlignedDKIMAndSurvivesMailboxMoves(t *testing.T) {
	receiver, raw := fixture(t, "example.test")
	token := attest(t, receiver, raw)
	if token == "" {
		t.Fatal("missing receive proof")
	}
	original := withProof(raw, token)
	for _, payload := range [][]byte{original, append([]byte("Received: by box.urmail.test\r\nDelivered-To: reader@urmail.test\r\n"), original...)} {
		result := Read(payload, receiver.Secrets)
		if result.Status != "pass" || result.Domain != "example.test" || result.Source != Source || result.CheckedAt != "2026-10-30T08:00:00Z" {
			t.Fatalf("receive proof lost: %+v", result)
		}
	}
}

func TestReceiveProofRejectsForgeryChangesAndOtherKeys(t *testing.T) {
	receiver, raw := fixture(t, "example.test")
	token := attest(t, receiver, raw)
	wrongBox, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"historical auth results": append([]byte("Authentication-Results: box.urmail.test; dkim=pass; spf=pass\r\n"), raw...),
		"forged encrypted header": withProof(raw, "enc:v1:Zm9yZ2Vk"),
		"plaintext compatibility": withProof(raw, `{"purpose":"urmail.receive-auth.v1","result":{"status":"pass"}}`),
		"body changed":            bytes.Replace(withProof(raw, token), []byte("Exact body"), []byte("Forged body"), 1),
		"sender changed":          bytes.Replace(withProof(raw, token), []byte("alice@example.test"), []byte("ceo@example.test"), 1),
		"subject changed":         bytes.Replace(withProof(raw, token), []byte("Authentication fixture"), []byte("Forged subject"), 1),
		"duplicate proof":         withProof(withProof(raw, token), token),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if result := Read(payload, receiver.Secrets); result.Status != "unknown" {
				t.Fatalf("trusted attacker-controlled content: %+v", result)
			}
		})
	}
	if result := Read(withProof(raw, token), wrongBox); result.Status != "unknown" {
		t.Fatalf("wrong key trusted: %+v", result)
	}
}

func TestReceiveDKIMFailureUnalignedSignatureAndCaptureLimits(t *testing.T) {
	receiver, raw := fixture(t, "evil.test")
	if result := Read(withProof(raw, attest(t, receiver, raw)), receiver.Secrets); result.Status != "unknown" || result.Reason != "dkim_unaligned" {
		t.Fatalf("unaligned signature trusted: %+v", result)
	}
	receiver, raw = fixture(t, "example.test")
	changed := bytes.Replace(raw, []byte("Exact body"), []byte("Tampered body"), 1)
	if result := Read(withProof(changed, attest(t, receiver, changed)), receiver.Secrets); result.Status != "fail" {
		t.Fatalf("real DKIM mismatch not reported: %+v", result)
	}
	if token, err := receiver.Attest(t.Context(), []Header{{Name: "From", Value: "alice@example.test"}}, make([]byte, MaxBodyBytes+1)); err != nil || token != "" {
		t.Fatalf("oversized body attested: %q %v", token, err)
	}
	if token, err := receiver.Attest(t.Context(), make([]Header, MaxHeaders+1), nil); err != nil || token != "" {
		t.Fatalf("header storm attested: %q %v", token, err)
	}
	if result := Read(raw, nil); result.Status != "unknown" {
		t.Fatal(result)
	}
}
