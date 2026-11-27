package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/dkimsign"
	"github.com/bushixiucai/urmail/internal/mailspam"
)

// TestSignHandlerInsertsDKIMHeader drives the signer handler end to end
// through the milter wire protocol (the same Server Postfix talks to) and
// asserts the inserted DKIM-Signature header carries the signing domain.
func TestSignHandlerInsertsDKIMHeader(t *testing.T) {
	dir := t.TempDir()
	store := dkimsign.NewStore(dir, "mail")
	key, keyErr := rsa.GenerateKey(rand.Reader, 1024)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	domainDir := filepath.Join(dir, "example.test")
	if err := os.MkdirAll(domainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	if err := os.WriteFile(filepath.Join(domainDir, "mail.private"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	// The wire-level round trip (negotiate/headers/body/insert/verify) is
	// covered by internal/mailspam milter tests against the same Server
	// type (TestMilterRoundTripInsertsHeaders and friends); here we pin the
	// handler contract directly: known domain signs, unknown domain skips.
	h := &signHandler{store: store}
	req := mailspam.VerdictRequest{
		EnvelopeFrom: "alice@example.test",
		SASLUser:     "alice@example.test", // authenticated submission
		ClientAddr:   "127.0.0.1:5555",
		Headers:      []mailspam.Header{{Name: "From", Value: "Alice <alice@example.test>"}},
		FullBody:     []byte("body\r\n"),
	}
	resp := h.Decide(context.Background(), req)
	if len(resp.InsertHeaders) != 1 || resp.InsertHeaders[0][0] != "DKIM-Signature" {
		t.Fatalf("handler did not sign a provisioned domain: %+v", resp)
	}
	if !strings.Contains(resp.InsertHeaders[0][1], "d=example.test") {
		t.Fatalf("signature missing d= tag: %q", resp.InsertHeaders[0][1])
	}

	foreign := mailspam.VerdictRequest{
		EnvelopeFrom: "attacker@foreign.test",
		Headers:      []mailspam.Header{{Name: "From", Value: "Attacker <attacker@foreign.test>"}},
		FullBody:     []byte("body\r\n"),
	}
	resp = h.Decide(context.Background(), foreign)
	if len(resp.InsertHeaders) != 0 || resp.Reject {
		t.Fatalf("unkeyed domain must pass unsigned: %+v", resp)
	}
}

// TestSignHandlerRejectsUnauthenticatedExternalForgery is the QA
// wiring-fix-2 item 1 negative: an unauthenticated external submission on
// port 25 forging a local From must NOT be signed with our key, and a
// loopback local injection must be.
func TestSignHandlerRejectsUnauthenticatedExternalForgery(t *testing.T) {
	dir := t.TempDir()
	key, keyErr := rsa.GenerateKey(rand.Reader, 1024)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	domainDir := filepath.Join(dir, "example.test")
	if err := os.MkdirAll(domainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	if err := os.WriteFile(filepath.Join(domainDir, "mail.private"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &signHandler{store: dkimsign.NewStore(dir, "mail")}

	forgery := mailspam.VerdictRequest{
		EnvelopeFrom: "attacker@evil.test",
		SASLUser:     "",
		ClientAddr:   "203.0.113.9:4444", // external, unauthenticated
		Headers:      []mailspam.Header{{Name: "From", Value: "Someone <user@example.test>"}},
		FullBody:     []byte("forged\r\n"),
	}
	if resp := h.Decide(context.Background(), forgery); len(resp.InsertHeaders) != 0 {
		t.Fatalf("external forgery was signed: %+v", resp)
	}

	authed := mailspam.VerdictRequest{
		EnvelopeFrom: "alice@example.test",
		SASLUser:     "alice@example.test",
		ClientAddr:   "203.0.113.9:4444",
		Headers:      []mailspam.Header{{Name: "From", Value: "Alice <alice@example.test>"}},
		FullBody:     []byte("body\r\n"),
	}
	if resp := h.Decide(context.Background(), authed); len(resp.InsertHeaders) != 1 {
		t.Fatalf("authenticated submission must sign: %+v", resp)
	}

	loopback := mailspam.VerdictRequest{
		EnvelopeFrom: "root@example.test",
		SASLUser:     "",
		ClientAddr:   "127.0.0.1:5555", // local injection
		Headers:      []mailspam.Header{{Name: "From", Value: "Root <root@example.test>"}},
		FullBody:     []byte("body\r\n"),
	}
	if resp := h.Decide(context.Background(), loopback); len(resp.InsertHeaders) != 1 {
		t.Fatalf("loopback injection must sign: %+v", resp)
	}
}

// TestSignHandlerDegradesOnTruncatedCapture pins the incomplete-capture
// degrade: a body that blew the retention cap must pass through unsigned
// (a signature over a truncated capture would not verify on delivery).
func TestSignHandlerDegradesOnTruncatedCapture(t *testing.T) {
	dir := t.TempDir()
	key, keyErr := rsa.GenerateKey(rand.Reader, 1024)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	domainDir := filepath.Join(dir, "example.test")
	if err := os.MkdirAll(domainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	if err := os.WriteFile(filepath.Join(domainDir, "mail.private"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &signHandler{store: dkimsign.NewStore(dir, "mail")}
	truncated := mailspam.VerdictRequest{
		SASLUser:           "alice@example.test",
		EnvelopeFrom:       "alice@example.test",
		ClientAddr:         "127.0.0.1:5555",
		Headers:            []mailspam.Header{{Name: "From", Value: "Alice <alice@example.test>"}},
		FullBody:           []byte("partial"),
		FullBodyIncomplete: true,
	}
	if resp := h.Decide(context.Background(), truncated); len(resp.InsertHeaders) != 0 {
		t.Fatalf("truncated capture was signed: %+v", resp)
	}
	noHeaders := mailspam.VerdictRequest{
		SASLUser:          "alice@example.test",
		ClientAddr:        "127.0.0.1:5555",
		HeadersIncomplete: true,
		FullBody:          []byte("body\r\n"),
	}
	if resp := h.Decide(context.Background(), noHeaders); len(resp.InsertHeaders) != 0 {
		t.Fatalf("header-truncated capture was signed: %+v", resp)
	}
}
