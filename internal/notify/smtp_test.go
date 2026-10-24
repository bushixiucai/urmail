package notify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func localSMTPRelay(t *testing.T, ambiguous bool) (SMTPConfig, *x509.CertPool, *atomic.Int32) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	submitted := &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				stream := textproto.NewConn(conn)
				if stream.PrintfLine("220 localhost ESMTP fixture") != nil {
					return
				}
				for {
					line, err := stream.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						if stream.PrintfLine("250 localhost") != nil {
							return
						}
					case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						if stream.PrintfLine("250 accepted") != nil {
							return
						}
					case line == "DATA":
						if stream.PrintfLine("354 send message") != nil {
							return
						}
						raw, err := stream.ReadDotBytes()
						if err != nil {
							return
						}
						if !strings.Contains(string(raw), "Auto-Submitted: auto-generated") {
							return
						}
						submitted.Add(1)
						if ambiguous {
							return
						}
						if stream.PrintfLine("250 queued") != nil {
							return
						}
					case line == "QUIT":
						return
					default:
						return
					}
				}
			}()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	return SMTPConfig{Enabled: true, Host: "127.0.0.1", Port: port, From: "notice@example.test", ImplicitTLS: true}, roots, submitted
}

func TestSMTPConfirmedDATAAndAmbiguousSubmissionRemainDistinct(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(strconv.FormatBool(ambiguous), func(t *testing.T) {
			cfg, roots, submitted := localSMTPRelay(t, ambiguous)
			err := sendSMTP(context.Background(), cfg, Message{ID: 1, To: "verified@example.test", Subject: "Notice", Body: "Durable notification"}, roots)
			if ambiguous && !errors.Is(err, ErrSubmissionUnknown) {
				t.Fatalf("lost DATA acknowledgment should remain uncertain: %v", err)
			}
			if !ambiguous && err != nil {
				t.Fatalf("confirmed DATA followed by lost QUIT should succeed: %v", err)
			}
			if submitted.Load() != 1 {
				t.Fatalf("SMTP submissions=%d", submitted.Load())
			}
		})
	}
}

func TestSMTPProductionSenderRejectsUntrustedTLSBeforeSubmission(t *testing.T) {
	cfg, _, submitted := localSMTPRelay(t, false)
	if err := SendSMTP(context.Background(), cfg, Message{ID: 1, To: "verified@example.test", Subject: "Notice", Body: "Private content"}); err == nil {
		t.Fatal("untrusted local certificate was accepted")
	}
	if submitted.Load() != 0 {
		t.Fatal("content was submitted before TLS trust was established")
	}
}
