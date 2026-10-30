package mailspam

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mailauth"
	"github.com/bushixiucai/urmail/internal/secrets"
)

func authenticationSession(t *testing.T) (*session, *secrets.Box) {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := &session{cfg: MilterConfig{Authentication: &mailauth.Receiver{Secrets: box}}.withDefaults()}
	s.startAuthenticationCapture()
	t.Cleanup(s.resetMessage)
	return s, box
}

func authenticationActions(t *testing.T, s *session) (removed int, token string) {
	t.Helper()
	var out bytes.Buffer
	if err := s.finishMessage(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	for out.Len() > 0 {
		var header [4]byte
		if _, err := io.ReadFull(&out, header[:]); err != nil {
			t.Fatal(err)
		}
		payload := make([]byte, binary.BigEndian.Uint32(header[:]))
		if _, err := io.ReadFull(&out, payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) > 5 {
			values := strings.Split(string(payload[5:]), "\x00")
			if len(values) >= 2 && values[0] == mailauth.HeaderName {
				if payload[0] == resChgHeader {
					removed++
				}
				if payload[0] == resInsHeader {
					token = values[1]
				}
			}
		}
	}
	return removed, token
}

func TestReceiveMilterDeletesForgedProofAndSealsOwnEvidence(t *testing.T) {
	s, box := authenticationSession(t)
	s.dirty = true
	headers := [][2]string{{"From", "alice@example.test"}, {"Subject", "Original"}, {mailauth.HeaderName, "forged-pass"}, {strings.ToLower(mailauth.HeaderName), "second-forgery"}, {"Authentication-Results", "box.example.test; dkim=pass; spf=pass"}}
	var ignored bytes.Buffer
	for _, h := range headers {
		if _, err := s.handlePacket(t.Context(), pkt(cmdHeader, h[0], h[1]), &ignored); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.handlePacket(t.Context(), append([]byte{cmdBody}, []byte("body\r\n")...), &ignored); err != nil {
		t.Fatal(err)
	}
	removed, token := authenticationActions(t, s)
	if removed != 2 || token == "" {
		t.Fatalf("forgeries=%d replacement=%q", removed, token)
	}
	raw := []byte(mailauth.HeaderName + ": " + token + "\r\nFrom: alice@example.test\r\nSubject: Original\r\nAuthentication-Results: box.example.test; dkim=pass; spf=pass\r\n\r\nbody\r\n")
	if result := mailauth.Read(raw, box); result.Status != "unknown" || result.Reason != "dkim_missing" || result.Source != mailauth.Source {
		t.Fatalf("forged auth headers trusted: %+v", result)
	}
}

func TestReceiveMilterDoesNotAttestIncompleteBodyOrHeaders(t *testing.T) {
	for _, name := range []string{"body", "headers"} {
		t.Run(name, func(t *testing.T) {
			s, _ := authenticationSession(t)
			s.dirty = true
			var out bytes.Buffer
			if name == "body" {
				chunk := append([]byte{cmdBody}, bytes.Repeat([]byte("a"), 1<<20)...)
				for i := 0; i < 17; i++ {
					if _, err := s.handlePacket(context.Background(), chunk, &out); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if _, err := s.handlePacket(t.Context(), pkt(cmdHeader, "Subject", strings.Repeat("a", mailauth.MaxHeaderBytes+1)), &out); err != nil {
					t.Fatal(err)
				}
			}
			if !s.authenticationIncomplete {
				t.Fatal("capture overflow was not recorded")
			}
			_, token := authenticationActions(t, s)
			if token != "" {
				t.Fatal("incomplete content received authentication proof")
			}
		})
	}
}
