package marketing

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

func localSMTP(t *testing.T, dropAfterData bool) (SMTPConfig, *x509.CertPool, <-chan string) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local-smtp"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local-smtp"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, public, key)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	message := make(chan string, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		reader := textproto.NewReader(bufio.NewReader(conn))
		write := func(text string) bool { _, writeErr := conn.Write([]byte(text + "\r\n")); return writeErr == nil }
		if !write("220 local-smtp ESMTP") {
			return
		}
		for {
			line, readErr := reader.ReadLine()
			if readErr != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				if !write("250-local-smtp\r\n250 STARTTLS") {
					return
				}
			case line == "STARTTLS":
				if !write("220 Begin TLS") {
					return
				}
				secured := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if handshakeErr := secured.HandshakeContext(context.Background()); handshakeErr != nil {
					return
				}
				conn = secured
				reader = textproto.NewReader(bufio.NewReader(conn))
			case strings.HasPrefix(line, "MAIL FROM:") || strings.HasPrefix(line, "RCPT TO:"):
				if !write("250 Accepted") {
					return
				}
			case line == "DATA":
				if !write("354 Continue") {
					return
				}
				body, bodyErr := reader.ReadDotBytes()
				if bodyErr != nil {
					return
				}
				message <- string(body)
				if dropAfterData {
					return
				}
				if !write("250 Message accepted") {
					return
				}
			case line == "QUIT":
				write("221 Bye")
				return
			default:
				if !write("500 Unsupported") {
					return
				}
			}
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return SMTPConfig{Host: host, Port: port, From: "sender@example.test", Dedicated: true}, roots, message
}

func TestSMTPGatewayVerifiesTLSAndWritesUnsubscribeHeaders(t *testing.T) {
	cfg, roots, payload := localSMTP(t, false)
	d := Delivery{Email: "recipient@example.test", Subject: "中文 Subject", Body: "Consented body", MessageID: "campaign-1@example.test", UnsubscribeURL: "https://panel.example.test/unsubscribe?token=signed"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sendSMTP(ctx, cfg, d, roots); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-payload:
		if !strings.Contains(body, "List-Unsubscribe-Post: List-Unsubscribe=One-Click") || !strings.Contains(body, d.UnsubscribeURL) || !strings.Contains(body, "Message-ID: <campaign-1@example.test>") {
			t.Fatalf("required delivery headers missing: %q", body)
		}
	case <-ctx.Done():
		t.Fatal("local SMTP did not receive payload")
	}
}

func TestSMTPAcceptanceDisconnectIsUncertain(t *testing.T) {
	cfg, roots, _ := localSMTP(t, true)
	err := sendSMTP(context.Background(), cfg, Delivery{Email: "recipient@example.test", Subject: "Subject", Body: "Body", MessageID: "campaign@example.test", UnsubscribeURL: "https://panel.example.test/unsubscribe"}, roots)
	if !domainMail.IsDeliveryUncertain(err) {
		t.Fatalf("DATA disconnect must remain uncertain: %v", err)
	}
}

func TestSMTPRefusesUntrustedCertificateBeforeSending(t *testing.T) {
	cfg, _, payload := localSMTP(t, false)
	err := SendSMTP(context.Background(), cfg, Delivery{Email: "recipient@example.test", Subject: "Subject", Body: "Body", MessageID: "campaign@example.test", UnsubscribeURL: "https://panel.example.test/unsubscribe"})
	if err == nil {
		t.Fatal("untrusted gateway certificate was accepted")
	}
	select {
	case <-payload:
		t.Fatal("message sent through untrusted gateway")
	default:
	}
}

func TestSMTPRevocationImmediatelyBeforeData(t *testing.T) {
	cfg, roots, messages := localSMTP(t, false)
	called := false
	d := Delivery{Email: "recipient@example.test", Subject: "Test", Body: "Body", MessageID: "check@example.test", UnsubscribeURL: "https://example.test/unsubscribe"}
	d.BeforeData = func(context.Context) error { called = true; return context.Canceled }
	if err := sendSMTP(context.Background(), cfg, d, roots); err == nil || domainMail.IsDeliveryUncertain(err) {
		t.Fatalf("revoked attempt outcome: %v", err)
	}
	if !called {
		t.Fatal("authorization not checked before DATA")
	}
	select {
	case payload := <-messages:
		t.Fatalf("revoked payload sent: %s", payload)
	default:
	}
}
