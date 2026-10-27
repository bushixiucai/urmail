package backupauto

import (
	"bytes"
	"strings"
	"testing"

	"filippo.io/age"
)

func generateIdentity(t *testing.T) (string, string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.String(), id.Recipient().String()
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	priv, pub := generateIdentity(t)
	identity, err := age.ParseX25519Identity(priv)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := bytes.Repeat([]byte("urmail backup payload\x00\xff"), 10000)

	var ciphertext bytes.Buffer
	if err := EncryptTo(&ciphertext, bytes.NewReader(plaintext), mustRecipient(t, pub)); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if ciphertext.Len() == 0 {
		t.Fatal("ciphertext is empty")
	}
	if bytes.Contains(ciphertext.Bytes(), []byte("urmail backup payload")) {
		t.Fatal("ciphertext leaks plaintext bytes")
	}

	var decrypted bytes.Buffer
	if err := DecryptFrom(&decrypted, bytes.NewReader(ciphertext.Bytes()), identity); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(decrypted.Bytes(), plaintext) {
		t.Fatal("round trip mismatch")
	}
}

func TestDecryptRejectsWrongIdentityAndTampering(t *testing.T) {
	priv, pub := generateIdentity(t)
	wrongPriv, _ := generateIdentity(t)
	wrong, err := age.ParseX25519Identity(wrongPriv)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext bytes.Buffer
	if err := EncryptTo(&ciphertext, strings.NewReader("secret"), mustRecipient(t, pub)); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := DecryptFrom(&out, bytes.NewReader(ciphertext.Bytes()), wrong); err == nil {
		t.Fatal("decrypt with wrong identity must fail")
	}

	right, err := age.ParseX25519Identity(priv)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), ciphertext.Bytes()...)
	tampered[len(tampered)-1] ^= 0xff
	if err := DecryptFrom(&out, bytes.NewReader(tampered), right); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

func TestParseRecipientRejectsPrivateKey(t *testing.T) {
	priv, _ := generateIdentity(t)
	if _, err := ParseRecipient(priv); err == nil {
		t.Fatal("a private key must not be accepted as a recipient")
	}
}

func mustRecipient(t *testing.T, publicKey string) age.Recipient {
	t.Helper()
	r, err := ParseRecipient(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
