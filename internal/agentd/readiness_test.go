package agentd

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/certs"
)

func greetingFixture(t *testing.T, certificate *tls.Certificate, greeting string, silent bool) string {
	t.Helper()
	var listenerConfig net.ListenConfig
	listener, err := listenerConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if certificate != nil {
		listener = tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*certificate}})
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		if silent {
			buffer := make([]byte, 1)
			_, _ = connection.Read(buffer)
			return
		}
		_, _ = connection.Write([]byte(greeting))
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return listener.Addr().String()
}

func readinessCertificate(t *testing.T, hostname string, validFor time.Duration) (tls.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := certs.GenerateSelfSignedCert(key, hostname, []string{hostname}, validFor)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certificate, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return pair, certificate
}

func TestReadinessRequiresActualApplicationGreeting(t *testing.T) {
	for _, tc := range []struct {
		name, banner, greeting string
		silent                 bool
		wantError              bool
	}{
		{"smtp", "220", "220 host ESMTP ready\r\n", false, false},
		{"submission", "220", "220-host ESMTP ready\r\n", false, false},
		{"dead listener accepts TCP", "220", "", true, true},
		{"worker closes without greeting", "220", "", false, true},
		{"wrong service", "220", "HTTP/1.1 200 OK\r\n", false, true},
		{"false prefix", "220", "2200 fake greeting\r\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address := greetingFixture(t, nil, tc.greeting, tc.silent)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			err := probeGreeting(ctx, address, tc.banner, nil, nil)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}

func TestReadinessStrictTLSAndInstalledConsumerIdentity(t *testing.T) {
	pair, certificate := readinessCertificate(t, "box.example.test", 2*time.Hour)
	config, expected, err := installedTLSConfig(certificate, "box.example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, banner, greeting   string
		wrongHost, wrongIdentity bool
		wantError                bool
	}{
		{name: "imaps", banner: "* OK", greeting: "* OK Dovecot ready\r\n"},
		{name: "pop3s", banner: "+OK", greeting: "+OK Dovecot ready\r\n"},
		{name: "wrong banner", banner: "* OK", greeting: "* BYE unavailable\r\n", wantError: true},
		{name: "SAN mismatch", banner: "* OK", greeting: "* OK Dovecot ready\r\n", wrongHost: true, wantError: true},
		{name: "stale consumer identity", banner: "* OK", greeting: "* OK Dovecot ready\r\n", wrongIdentity: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address := greetingFixture(t, &pair, tc.greeting, false)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cfg := config.Clone()
			identity := expected
			if tc.wrongHost {
				cfg.ServerName = "wrong.example.test"
			}
			if tc.wrongIdentity {
				identity = []byte("old installed certificate")
			}
			err := probeGreeting(ctx, address, tc.banner, cfg, identity)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v wantError=%v", err, tc.wantError)
			}
		})
	}
	if _, _, err = installedTLSConfig(certificate, "wrong.example.test"); err == nil {
		t.Fatal("installed certificate with wrong SAN accepted")
	}
}

func TestReadinessProbeFailureCannotPublishAppliedGeneration(t *testing.T) {
	m := testManager(t)
	m.readinessProbe = func(context.Context) error { return context.DeadlineExceeded }
	if _, _, err := m.Apply(emptyApply()); err == nil {
		t.Fatal("readiness failure accepted")
	}
	if m.status.Ready || m.status.Applied != "" {
		t.Fatalf("false ready %+v", m.status)
	}
}

func TestReadinessRejectsExpiredInstalledCertificate(t *testing.T) {
	pair, certificate := readinessCertificate(t, "box.example.test", -time.Hour)
	cfg, expected, err := installedTLSConfig(certificate, "box.example.test")
	if err != nil {
		t.Fatal(err)
	}
	address := greetingFixture(t, &pair, "* OK ready\r\n", false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = probeGreeting(ctx, address, "* OK", cfg, expected); err == nil {
		t.Fatal("expired bootstrap certificate accepted")
	}
}
func TestReadinessUnchangedGenerationStillChecksWorkers(t *testing.T) {
	m := testManager(t)
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	old := m.status.Applied
	m.readinessProbe = func(context.Context) error { return context.DeadlineExceeded }
	if _, _, err := m.Apply(emptyApply()); err == nil {
		t.Fatal("unchanged generation skipped protocol readiness")
	}
	if m.status.Ready || m.status.Applied != old || m.status.Error == "" {
		t.Fatalf("false deduplicated ready %+v", m.status)
	}
}
