package linkconfirm

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignerRoundTrip(t *testing.T) {
	signer := NewSigner([]byte("k"))
	token, err := signer.Sign(Payload{MailboxID: 7, UID: 42, URL: "https://evil.example/phish", From: "a@b.test"})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	p, err := signer.Verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.MailboxID != 7 || p.UID != 42 || p.URL != "https://evil.example/phish" || p.From != "a@b.test" {
		t.Fatalf("payload %+v", p)
	}
}

func TestSignerRejectsTamperedExpiredForeign(t *testing.T) {
	signer := NewSigner([]byte("k"))
	token, err := signer.Sign(Payload{MailboxID: 7, UID: 1, URL: "https://a.example/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Verify(token + "x"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered: %v", err)
	}
	expired, err := signer.Sign(Payload{MailboxID: 7, UID: 1, URL: "https://a.example/", Expires: time.Now().Add(-time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Verify(expired); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	other := NewSigner([]byte("other"))
	if _, err := other.Verify(token); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("foreign: %v", err)
	}
}

func TestNeedsConfirm(t *testing.T) {
	if !NeedsConfirm("https://example.com/a") || !NeedsConfirm("http://example.com") {
		t.Fatal("http(s) must confirm")
	}
	for _, href := range []string{"mailto:a@b.test", "tel:+123", "/relative", "javascript:alert(1)", ""} {
		if NeedsConfirm(href) {
			t.Fatalf("%q should not confirm", href)
		}
	}
}

func TestHostLabelsPerCharacter(t *testing.T) {
	got := HostLabels("аррӏе.com") // Cyrillic look-alike
	if len(got) == 0 {
		t.Fatal("empty")
	}
	if strings.Join(got, "") != "аррӏе.com" {
		t.Fatalf("joined %q", strings.Join(got, ""))
	}
	for i, ch := range got {
		if len([]rune(ch)) != 1 {
			t.Fatalf("label %d is not one rune: %q", i, ch)
		}
	}
}
