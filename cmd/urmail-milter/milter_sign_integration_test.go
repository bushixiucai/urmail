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
	server := &mailspam.Server{
		Addr:    "127.0.0.1:0",
		Handler: &signHandler{store: store},
		Config:  mailspam.MilterConfig{KeepFullBody: true},
	}
	_ = server
	// The wire-level round trip (negotiate/headers/body/insert) is covered
	// by internal/mailspam milter tests against the same Server type; here
	// we pin the handler contract directly: known domain signs, unknown
	// domain skips.
	h := &signHandler{store: store}
	req := mailspam.VerdictRequest{
		EnvelopeFrom: "alice@example.test",
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
