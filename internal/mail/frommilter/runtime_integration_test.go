package frommilter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/smtp"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestPostfixFromAuthorizationRuntime runs only in the network-isolated fixture
// owned by scripts/native-webmail-protocol-acceptance.py, never a deployed MTA.
func TestPostfixFromAuthorizationRuntime(t *testing.T) {
	if os.Getenv("URMAIL_FROMBIND_RUNTIME") != "1" {
		t.Skip("requires disposable Postfix/Dovecot runtime fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:12302")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = Serve(listener, fakeHandler{mapRaw: "alice@example.test\talice@example.test\n"}) }()
	fixedCommand := func(binary string, args ...string) {
		t.Helper()
		output, e := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if e != nil {
			t.Fatalf("fixture command %s: %v %s", binary, e, output)
		}
	}
	fixedCommand("/usr/sbin/postconf", "-e", "smtpd_milters=inet:127.0.0.1:12302", "milter_default_action=tempfail", "milter_protocol=6", "milter_mail_macros=i,{auth_authen}")
	fixedCommand("/usr/sbin/postfix", "reload")
	cert, err := os.ReadFile("/tmp/native-mail-runtime/cert.pem")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("fixture certificate unavailable")
	}
	tlsConfig := &tls.Config{RootCAs: roots, ServerName: "runtime.example.test", MinVersion: tls.VersionTLS12}
	for _, tc := range []struct {
		name, from string
		allowed    bool
	}{
		{"owner", "Alice <alice@example.test>", true},
		{"same_domain", "Other <other@example.test>", true},
		{"quoted_display", "\"Display: @ outside.test\" <alice@example.test>", true},
		{"foreign", "Alice <alice@outside.test>", false},
		{"list", "alice@example.test, foreign@outside.test", false},
		{"group", "Group: alice@example.test;", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", "127.0.0.1:587")
			if e != nil {
				t.Fatal(e)
			}
			if e = conn.SetDeadline(time.Now().Add(10 * time.Second)); e != nil {
				_ = conn.Close()
				t.Fatal(e)
			}
			client, e := smtp.NewClient(conn, "runtime.example.test")
			if e != nil {
				_ = conn.Close()
				t.Fatal(e)
			}
			defer client.Close()
			if e = client.StartTLS(tlsConfig); e != nil {
				t.Fatal(e)
			}
			if e = client.Auth(smtp.PlainAuth("", "alice@example.test", "isolated-fixture-password", "runtime.example.test")); e != nil {
				t.Fatal(e)
			}
			if e = client.Mail("alice@example.test"); e != nil {
				t.Fatal(e)
			}
			if e = client.Rcpt("bob@example.test"); e != nil {
				t.Fatal(e)
			}
			data, e := client.Data()
			if e != nil {
				t.Fatal(e)
			}
			_, writeErr := data.Write([]byte("From: " + tc.from + "\r\nTo: bob@example.test\r\nSubject: from-authorization-runtime\r\n\r\nlocal fixture body\r\n"))
			e = data.Close()
			if tc.allowed && (e != nil || writeErr != nil) {
				t.Fatalf("authorized single From rejected: %v %v", writeErr, e)
			}
			if !tc.allowed && e == nil {
				t.Fatal("unauthorized From structure accepted by real Postfix")
			}
			if !tc.allowed && !strings.HasPrefix(e.Error(), "550") {
				t.Fatalf("expected authorization rejection, got %v", e)
			}
		})
	}
	t.Log("PASS real Postfix SMTP AUTH + milter negotiation/EOH + strict single From domain authorization")
}
