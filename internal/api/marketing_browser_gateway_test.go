package api_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/marketing"
)

// browserMarketingGateway is a real loopback TLS SMTP receiver with a read-only
// message-capture HTTP surface. It never connects to an external mail server.
func browserMarketingGateway(t *testing.T) (marketing.SMTPConfig, *x509.CertPool, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "urMail local browser SMTP"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(root)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	messages := make([]map[string]any, 0)
	var connections sync.Map
	t.Cleanup(func() {
		_ = listener.Close()
		connections.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			connections.Store(conn, true)
			go func() {
				defer func() { connections.Delete(conn); _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
				reader := bufio.NewReader(conn)
				write := func(s string) bool { _, err := fmt.Fprintf(conn, "%s\r\n", s); return err == nil }
				if !write("220 browser-local ESMTP") {
					return
				}
				for {
					line, readErr := reader.ReadString('\n')
					if readErr != nil {
						return
					}
					line = strings.TrimSpace(line)
					switch {
					case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
						if !write("250 browser-local") {
							return
						}
					case strings.HasPrefix(line, "MAIL FROM:") || strings.HasPrefix(line, "RCPT TO:"):
						if !write("250 Accepted") {
							return
						}
					case line == "DATA":
						if !write("354 End with dot") {
							return
						}
						body, dataErr := textproto.NewReader(reader).ReadDotBytes()
						if dataErr != nil {
							return
						}
						parsed, parseErr := mail.ReadMessage(bytes.NewReader(body))
						if parseErr != nil {
							return
						}
						hash := sha256.Sum256(body)
						state := conn.(*tls.Conn).ConnectionState()
						mu.Lock()
						messages = append(messages, map[string]any{"subject": parsed.Header.Get("Subject"), "message_id": parsed.Header.Get("Message-ID"), "unsubscribe_url": strings.Trim(parsed.Header.Get("List-Unsubscribe"), "<>"), "one_click": parsed.Header.Get("List-Unsubscribe-Post"), "sha256": hex.EncodeToString(hash[:]), "tls_verified_transport": state.HandshakeComplete, "tls_version": state.Version})
						mu.Unlock()
						if !write("250 queued-local") {
							return
						}
					case line == "QUIT":
						_ = write("221 Bye")
						return
					default:
						if !write("250 OK") {
							return
						}
					}
				}
			}()
		}
	}()
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": messages})
	}))
	t.Cleanup(capture.Close)
	host, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return marketing.SMTPConfig{Host: host, Port: port, ImplicitTLS: true, From: "sender@marketing-browser.example.test", Dedicated: true, FeedbackKey: strings.Repeat("f", 32)}, roots, capture.URL
}
