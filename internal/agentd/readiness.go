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
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/backupauto"
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
	tlsConfig, expected, err := m.mailReadinessTLSConfig()
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

func (m *ApplyManager) mailReadinessTLSConfig() (*tls.Config, []byte, error) {
	// Postfix and Dovecot consume this pair. The panel can have an independent
	// bootstrap certificate, so its preferred fullchain is not mail trust.
	path := filepath.Join(m.root, "certs/mail", m.hostname()+".cert")
	certificate, err := os.ReadFile(path) // #nosec G304 -- fixed root-owned installed mail consumer selected by validated hostname.
	if err != nil {
		return nil, nil, fmt.Errorf("read installed mail certificate: %w", err)
	}
	return installedTLSConfig(certificate, m.hostname())
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
	if err := m.probeMailReadiness(ctx); err != nil {
		return err
	}
	return m.probeNewServiceReadiness(ctx)
}

// probeNewServiceReadiness covers the services added after the original mail
// stack with database-state probes: the DAV (CardDAV/CalDAV) and EAS
// endpoints serve from the collaboration schema of migration 0084, and the
// automated offsite backup records its runs in backup_runs (0120). Database
// probes behave identically on a developer laptop and on a production host,
// unlike network probes that depend on listeners a test host does not have.
func (m *ApplyManager) probeNewServiceReadiness(ctx context.Context) error {
	if m.pool == nil {
		// Fixture managers carry no database; their readiness is injected
		// through readinessProbe instead.
		return nil
	}
	if m.cfg == nil || m.cfg.WebmailNative || m.cfg.CardDAVEnabled || m.cfg.CalDAVEnabled || m.cfg.EASEnabled {
		if err := m.probeCollabSchema(ctx); err != nil {
			return err
		}
	}
	return m.probeBackupOffsite(ctx)
}

// collabTables is the collaboration schema DAV and EAS read and write.
// Missing objects mean migration 0084 was not applied: the endpoints would
// mount and then serve nothing, so readiness must fail explicitly instead.
var collabTables = []string{
	"mailbox_addressbooks", "mailbox_contacts", "mailbox_calendars",
	"mailbox_events", "mailbox_changes", "mailbox_sync_cursors", "mailbox_jobs",
}

func (m *ApplyManager) probeCollabSchema(ctx context.Context) error {
	m.status.Step = "probe:collab"
	m.publish()
	rows, err := m.pool.Query(ctx, `SELECT t.name FROM unnest($1::text[]) AS t(name)
		WHERE NOT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relkind IN ('r','p') AND c.relname = t.name)`, collabTables)
	if err != nil {
		return fmt.Errorf("collaboration schema probe: %w", err)
	}
	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("collaboration schema probe: %w", err)
		}
		missing = append(missing, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("collaboration schema probe: %w", err)
	}
	rows.Close()
	if len(missing) > 0 {
		return fmt.Errorf("collaboration schema incomplete (DAV/EAS, migration 0084): missing tables %s; apply migrations before enabling the endpoints", strings.Join(missing, ", "))
	}
	return m.probeSchemaVersion(ctx)
}

// probeSchemaVersion fails when a migration interrupted mid-apply left
// schema_migrations dirty: configuration would converge onto a schema whose
// version is not actually known-good. A pre-migration database without the
// table is reported by the table probe above instead.
func (m *ApplyManager) probeSchemaVersion(ctx context.Context) error {
	var dirty bool
	err := m.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE dirty)").Scan(&dirty)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return nil
		}
		return fmt.Errorf("schema version probe: %w", err)
	}
	if dirty {
		return errors.New("schema_migrations is dirty: a migration did not finish; resolve it before enabling the new endpoints")
	}
	return nil
}

func (m *ApplyManager) probeBackupOffsite(ctx context.Context) error {
	m.status.Step = "probe:backup"
	m.publish()
	if err := m.probeBackupScheduler(ctx); err != nil {
		return err
	}
	health, err := (&backupauto.StateRecorder{Pool: m.pool}).LastRunHealth(ctx)
	if errors.Is(err, backupauto.ErrBackupOffsiteDisabled) {
		// No backup_runs table: automated offsite backup is not provisioned
		// on this installation, so there is no state to fail on.
		return nil
	}
	if err != nil {
		if errors.Is(err, backupauto.ErrNoRuns) {
			// Feature configured but the first scheduled run has not happened.
			return nil
		}
		return fmt.Errorf("backup offsite probe: %w", err)
	}
	if health.State == backupauto.StateFailed {
		return fmt.Errorf("last offsite backup run %s failed: %s", health.RunUID, health.Error)
	}
	if health.StaleRun(time.Now()) {
		return fmt.Errorf("offsite backup run %s is stuck in state %q since %s: the backup worker stopped making progress",
			health.RunUID, health.State, health.StartedAt.Format(time.RFC3339))
	}
	return nil
}

func (m *ApplyManager) probeBackupScheduler(ctx context.Context) error {
	if m.cfg.BackgroundWorkersAllowed() {
		if m.run == nil {
			return errors.New("backup scheduler probe has no command runner")
		}
		if err := m.run(ctx, "/usr/bin/systemctl", "cat", "urmail-backup-auto.service"); err != nil {
			return fmt.Errorf("complete offsite backup service is not installed: %w", err)
		}
		for _, action := range []string{"is-enabled", "is-active"} {
			if err := m.run(ctx, "/usr/bin/systemctl", action, "--quiet", "urmail-backup-auto.timer"); err != nil {
				return fmt.Errorf("complete offsite backup scheduler is not enabled and active: %w", err)
			}
		}
	}
	return nil
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
