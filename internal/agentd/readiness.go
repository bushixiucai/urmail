package agentd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// probeAttemptBudget is long enough for one greeting/TLS attempt yet short
// enough that a few retries still fit inside the unchanged-generation budget.
const probeAttemptBudget = 2 * time.Second

// probeRetryBackoff absorbs brief Postfix/Dovecot restart races without
// masking a sustained SMTP outage (retries still exhaust the parent context).
var probeRetryBackoff = []time.Duration{150 * time.Millisecond, 350 * time.Millisecond, 700 * time.Millisecond}

// probeMailReadiness checks application greetings, not merely open master
// sockets. A live Dovecot master cannot establish usable login workers by itself.
func (m *ApplyManager) probeMailReadiness(ctx context.Context) error {
	certificate, err := os.ReadFile(certificateCertPath(m.certificatePaths(m.hostname()))) // #nosec G304 -- fixed root-owned installed hostname consumer certificate.
	if err != nil {
		return fmt.Errorf("read installed mail certificate: %w", err)
	}
	tlsConfig, expected, err := installedTLSConfig(certificate, m.hostname())
	if err != nil {
		return err
	}
	for _, probe := range []struct {
		name, address, banner string
		secure                bool
	}{
		{"smtp25", "127.0.0.1:25", "220", false},
		{"submission587", "127.0.0.1:587", "220", false},
		{"imaps993", "127.0.0.1:993", "* OK", true},
		{"pop3s995", "127.0.0.1:995", "+OK", true},
	} {
		m.status.Step = "probe:" + probe.name
		m.publish()
		var config *tls.Config
		if probe.secure {
			config = tlsConfig
		}
		if probe.name == "submission587" {
			err = probeWithRetry(ctx, func(attemptCtx context.Context) error {
				return probeSubmissionTLS(attemptCtx, probe.address, tlsConfig, expected)
			})
		} else {
			err = probeWithRetry(ctx, func(attemptCtx context.Context) error {
				return probeGreeting(attemptCtx, probe.address, probe.banner, config, expected)
			})
		}
		if err != nil {
			return fmt.Errorf("%s readiness: %w", probe.name, err)
		}
	}
	return nil
}

func probeWithRetry(ctx context.Context, attempt func(context.Context) error) error {
	var last error
	for i := 0; ; i++ {
		attemptCtx, cancel := context.WithTimeout(ctx, probeAttemptBudget)
		last = attempt(attemptCtx)
		cancel()
		if last == nil {
			return nil
		}
		if i >= len(probeRetryBackoff) {
			return last
		}
		timer := time.NewTimer(probeRetryBackoff[i])
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(last, context.DeadlineExceeded) || errors.Is(last, context.Canceled) {
				return last
			}
			return fmt.Errorf("%w: %w", ctx.Err(), last)
		case <-timer.C:
		}
	}
}

func installedTLSConfig(certificate []byte, hostname string) (*tls.Config, []byte, error) {
	block, _ := pem.Decode(certificate)
	if block == nil {
		return nil, nil, fmt.Errorf("invalid installed certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse installed certificate: %w", err)
	}
	if err = leaf.VerifyHostname(hostname); err != nil {
		return nil, nil, fmt.Errorf("installed certificate hostname: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return nil, nil, fmt.Errorf("invalid installed certificate trust")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname, RootCAs: roots}, leaf.Raw, nil
}

func probeGreeting(ctx context.Context, address, banner string, config *tls.Config, expected []byte) error {
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = raw.Close() }()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	if err = raw.SetDeadline(deadline); err != nil {
		return err
	}
	connection := raw
	if config != nil {
		secure := tls.Client(raw, config.Clone())
		if err = secure.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS handshake: %w", err)
		}
		peers := secure.ConnectionState().PeerCertificates
		if len(peers) == 0 || !bytes.Equal(peers[0].Raw, expected) {
			return fmt.Errorf("served certificate differs from installed consumer")
		}
		connection = secure
	}
	line, err := bufio.NewReader(io.LimitReader(connection, 4096)).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read greeting: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(strings.ToUpper(line), banner) || (len(line) > len(banner) && line[len(banner)] != ' ' && line[len(banner)] != '-') {
		return fmt.Errorf("unexpected protocol greeting")
	}
	return nil
}

func (m *ApplyManager) checkMailReadiness(ctx context.Context) error {
	m.status.Step = "probe:mail"
	m.publish()
	if m.readinessProbe != nil {
		return m.readinessProbe(ctx)
	}
	return m.probeMailReadiness(ctx)
}

func probeSubmissionTLS(ctx context.Context, address string, config *tls.Config, expected []byte) error {
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	if err = connection.SetDeadline(deadline); err != nil {
		return err
	}
	client, err := smtp.NewClient(connection, config.ServerName)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return fmt.Errorf("submission STARTTLS unavailable")
	}
	if err = client.StartTLS(config.Clone()); err != nil {
		return err
	}
	state, ok := client.TLSConnectionState()
	if !ok || len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, expected) {
		return fmt.Errorf("submission certificate differs from installed mail consumer")
	}
	return nil
}
