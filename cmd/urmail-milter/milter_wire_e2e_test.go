package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/dkimsign"
	"github.com/emersion/go-msgauth/dkim"
)

// TestWireEndToEndSignsAuthenticatedSubmission drives the REAL milter TCP
// wire protocol end to end (QA wiring-fix-2 S-3 case ①): CONNECT from a
// loopback client → OPTNEG → authenticated MAIL with headers/body → EOM;
// the inserted DKIM-Signature must verify under the standards verifier
// (RFC 6376 — the same contract Perl Mail::DKIM enforces).
func TestWireEndToEndSignsAuthenticatedSubmission(t *testing.T) {
	dir := t.TempDir()
	pub := provisionWireKey(t, dir, "example.test")
	client := startWireSigner(t, dkimsign.NewStore(dir, "mail"))

	client.handshake()
	cmd, sig := client.deliverMail("alice@example.test", "bob@remote.test",
		[]byte{127, 0, 0, 1}, wireProbeHeaders(), "wire probe body\r\n")
	if cmd != 'c' {
		t.Fatalf("EOM action %q", string(cmd))
	}
	if sig == "" {
		t.Fatal("no DKIM-Signature inserted")
	}

	// Verify the signature against the published public key.
	signed := wireSignedMessage(sig)
	txt := txtForWire(pub)
	verifications, err := dkim.VerifyWithOptions(strings.NewReader(signed), &dkim.VerifyOptions{
		LookupTXT: func(domain string) ([]string, error) {
			if strings.HasPrefix(domain, "mail._domainkey.") {
				return []string{txt}, nil
			}
			return nil, os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatalf("verify failed hard: %v", err)
	}
	if len(verifications) != 1 || verifications[0].Err != nil {
		t.Fatalf("signature did not verify: %+v (sig=%q)", verifications, sig)
	}
	client.quit()
}

// TestWireEndToEndRejectsUnauthenticatedForgery is the S-3 security
// negative (case ②): an unauthenticated external client (public CONNECT
// address, no SASL) forging a local From gets NO DKIM-Signature insertion.
func TestWireEndToEndRejectsUnauthenticatedForgery(t *testing.T) {
	dir := t.TempDir()
	provisionWireKey(t, dir, "example.test")
	client := startWireSigner(t, dkimsign.NewStore(dir, "mail"))

	client.handshake()
	// External attacker: public IP (203.0.113.9), no SASL identity.
	cmd, _ := client.deliverMail("user@example.test", "bob@remote.test",
		[]byte{203, 0, 113, 9}, [][2]string{
			{"From", "Someone <user@example.test>"},
			{"To", "Bob <bob@remote.test>"},
			{"Subject", "forged"},
		}, "forged body\r\n")
	if cmd == 'i' {
		t.Fatal("external forgery WAS signed — key laundering")
	}
	client.quit()
}

// TestWireEndToEndSecondMailOnSameConnection keeps the S-2b regression
// anchor over the wire: the second mail on the same TCP connection still
// resolves the connection-level peer address and signs.
func TestWireEndToEndSecondMailOnSameConnection(t *testing.T) {
	dir := t.TempDir()
	provisionWireKey(t, dir, "example.test")
	client := startWireSigner(t, dkimsign.NewStore(dir, "mail"))

	client.handshake()
	for i := 1; i <= 2; i++ {
		cmd, sig := client.deliverMail("alice@example.test", "bob@remote.test",
			[]byte{127, 0, 0, 1}, wireProbeHeaders(), fmt.Sprintf("body %d\r\n", i))
		if cmd != 'c' || sig == "" || !strings.Contains(sig, "d=example.test") {
			t.Fatalf("mail %d: action %q sig %q", i, cmd, sig)
		}
	}
	client.quit()
}

func wireProbeHeaders() [][2]string {
	return [][2]string{
		{"From", "Alice <alice@example.test>"},
		{"To", "Bob <bob@remote.test>"},
		{"Subject", "wire probe"},
		{"Date", "Thu, 27 Nov 2026 00:00:00 +0000"},
		{"Message-ID", "<m1@example.test>"},
		{"MIME-Version", "1.0"},
	}
}
