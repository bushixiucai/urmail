package agentd

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/certs"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
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

func TestMailReadinessUsesMailConsumerWhenPanelCertificateDiffers(t *testing.T) {
	m := testManager(t)
	panelPair, panelCertificate := readinessCertificate(t, m.hostname(), 2*time.Hour)
	mailPair, mailCertificate := readinessCertificate(t, m.hostname(), 2*time.Hour)
	for path, certificate := range map[string][]byte{
		filepath.Join(m.root, "certs/panel/fullchain.pem"):        panelCertificate,
		filepath.Join(m.root, "certs/mail", m.hostname()+".cert"): mailCertificate,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, certificate, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, expected, err := m.mailReadinessTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		pair      tls.Certificate
		wantError bool
	}{
		{"installed mail consumer", mailPair, false},
		{"panel identity on mail listener", panelPair, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address := greetingFixture(t, &tc.pair, "* OK ready\r\n", false)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := probeGreeting(ctx, address, "* OK", cfg, expected)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v wantError=%v", err, tc.wantError)
			}
		})
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

func TestReadinessUnchangedGenerationDoesNotFlapReadyWhileProbing(t *testing.T) {
	m := testManager(t)
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	sawReadyFalse := false
	m.readinessProbe = func(context.Context) error {
		if value := m.published.Load(); value != nil {
			status := value.(ApplyStatus)
			if !status.Ready {
				sawReadyFalse = true
			}
			if status.Step != "probe:mail" && status.Step != "ready" {
				t.Fatalf("unexpected mid-recheck step %q", status.Step)
			}
		}
		return nil
	}
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	if sawReadyFalse {
		t.Fatal("unchanged-generation recheck published ready:false before probe finished")
	}
	if !m.status.Ready || m.status.Step != "ready" {
		t.Fatalf("healthy recheck lost ready %+v", m.status)
	}
}

func TestProbeWithRetryRecoversFromTransientFailure(t *testing.T) {
	var attempts atomic.Int32
	err := probeWithRetry(context.Background(), func(context.Context) error {
		if attempts.Add(1) < 3 {
			return errors.New("connection refused")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts=%d want 3", got)
	}
}

func TestProbeWithRetryStillFailsAfterExhaustion(t *testing.T) {
	var attempts atomic.Int32
	err := probeWithRetry(context.Background(), func(context.Context) error {
		attempts.Add(1)
		return errors.New("connection refused")
	})
	if err == nil {
		t.Fatal("exhausted retries accepted")
	}
	if got := attempts.Load(); got != int32(len(probeRetryBackoff)+1) {
		t.Fatalf("attempts=%d want %d", got, len(probeRetryBackoff)+1)
	}
}

func TestSubmissionReadinessRequiresSTARTTLSAndInstalledSAN(t *testing.T) {
	pair, certificate := readinessCertificate(t, "mail.platform.test", 2*time.Hour)
	config, expected, err := installedTLSConfig(certificate, "mail.platform.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, wrongHost := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrong-SAN-%v", wrongHost), func(t *testing.T) {
			var listen net.ListenConfig
			listener, err := listen.Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				connection, e := listener.Accept()
				if e != nil {
					return
				}
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
				reader := bufio.NewReader(connection)
				_, _ = io.WriteString(connection, "220 fixture\r\n")
				if _, e = reader.ReadString('\n'); e != nil {
					return
				}
				_, _ = io.WriteString(connection, "250-fixture\r\n250 STARTTLS\r\n")
				if _, e = reader.ReadString('\n'); e != nil {
					return
				}
				_, _ = io.WriteString(connection, "220 Start TLS\r\n")
				secure := tls.Server(connection, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})
				if secure.HandshakeContext(t.Context()) != nil {
					return
				}
				reader = bufio.NewReader(secure)
				if _, e = reader.ReadString('\n'); e != nil {
					return
				}
				_, _ = io.WriteString(secure, "250 fixture\r\n")
			}()
			cfg := config.Clone()
			if wrongHost {
				cfg.ServerName = "different.example.test"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err = probeSubmissionTLS(ctx, listener.Addr().String(), cfg, expected)
			if (err != nil) != wrongHost {
				t.Fatalf("STARTTLS err=%v", err)
			}
			<-done
		})
	}
}

// --- OPS-008: new-service readiness probes ---

// TestCollabReadinessTracksMigration0084UpDown proves on real PostgreSQL that
// the DAV/EAS readiness probe follows the actual collaboration schema state:
// applied -> healthy, rolled back -> explicit failure naming the missing
// objects, re-applied -> healthy again.
func TestCollabReadinessTracksMigration0084UpDown(t *testing.T) {
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 84)
	platform, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	m := &ApplyManager{pool: platform}
	ctx := context.Background()

	if err = m.probeCollabSchema(ctx); err != nil {
		t.Fatalf("applied collaboration schema rejected: %v", err)
	}
	if m.status.Step != "probe:collab" {
		t.Fatalf("probe step not published: %q", m.status.Step)
	}

	// Down migration 0084 drops the tables; readiness must fail and name
	// the missing schema so an operator can act on the message alone.
	testdb.MigrateTo(t, dsn, 83)
	probeErr := m.probeCollabSchema(ctx)
	if probeErr == nil || !strings.Contains(probeErr.Error(), "migration 0084") || !strings.Contains(probeErr.Error(), "mailbox_events") {
		t.Fatalf("missing collaboration schema not reported explicitly: %v", probeErr)
	}

	// Re-applying the migration restores a healthy schema.
	testdb.MigrateTo(t, dsn, 84)
	if err = m.probeCollabSchema(ctx); err != nil {
		t.Fatalf("re-applied collaboration schema rejected: %v", err)
	}
}

// TestCollaborationReadinessFollowsEnabledProtocols keeps optional protocols
// independent, while every enabled collaboration entry point requires its schema.
func TestCollaborationReadinessFollowsEnabledProtocols(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	ctx := context.Background()
	if _, err := platform.Exec(ctx, "ALTER TABLE mailbox_events RENAME TO mailbox_events_missing"); err != nil {
		t.Fatal(err)
	}
	disabled := &ApplyManager{pool: platform, cfg: &config.Config{}}
	if err := disabled.probeNewServiceReadiness(ctx); err != nil {
		t.Fatalf("disabled protocols require collaboration schema: %v", err)
	}
	for name, cfg := range map[string]*config.Config{
		"webmail": {WebmailNative: true},
		"carddav": {CardDAVEnabled: true},
		"caldav":  {CalDAVEnabled: true},
		"eas":     {EASEnabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			m := &ApplyManager{pool: platform, cfg: cfg}
			if err := m.probeNewServiceReadiness(ctx); err == nil || !strings.Contains(err.Error(), "mailbox_events") {
				t.Fatalf("enabled protocol accepted incomplete schema: %v", err)
			}
		})
	}
}

// TestCollabReadinessFailsOnDirtySchemaMigration proves readiness surfaces an
// interrupted migration: configuration must not converge onto a schema whose
// version is not known-good.
func TestCollabReadinessFailsOnDirtySchemaMigration(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	m := &ApplyManager{pool: platform}
	ctx := context.Background()
	if _, err := platform.Exec(ctx, "UPDATE schema_migrations SET dirty=true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = platform.Exec(context.Background(), "UPDATE schema_migrations SET dirty=false") })
	err := m.probeCollabSchema(ctx)
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty schema_migrations not reported: %v", err)
	}
}

func insertBackupRun(t *testing.T, platform *db.Pool, state string, startedHoursAgo float64, errorText string) string {
	t.Helper()
	var uid string
	if err := platform.QueryRow(context.Background(),
		`INSERT INTO backup_runs(run_uid,state,started_at,error) VALUES($1,$2,statement_timestamp()-make_interval(secs=>$3),$4) RETURNING run_uid`,
		"probe"+strings.ReplaceAll(t.Name(), "/", "")+state[:3], state, startedHoursAgo*3600, errorText).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	return uid
}

// TestBackupOffsiteReadinessFollowsLastRunState proves the automated offsite
// backup probe fails explicitly while the newest run is failed or abandoned,
// stays healthy on success, and ignores in-progress runs.
func TestBackupOffsiteReadinessFollowsLastRunState(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	m := &ApplyManager{pool: platform}
	ctx := context.Background()

	// Configured but the first scheduled run has not happened yet.
	if err := m.probeBackupOffsite(ctx); err != nil {
		t.Fatalf("run-less probe must not fail: %v", err)
	}

	insertBackupRun(t, platform, backupauto.StateFailed, 1, "upload denied by destination")
	err := m.probeBackupOffsite(ctx)
	if err == nil || !strings.Contains(err.Error(), "upload denied by destination") {
		t.Fatalf("failed run not surfaced with its error: %v", err)
	}
	if m.status.Step != "probe:backup" {
		t.Fatalf("probe step not published: %q", m.status.Step)
	}

	// A newer successful run restores readiness.
	insertBackupRun(t, platform, backupauto.StateSuccess, 0, "")
	if err = m.probeBackupOffsite(ctx); err != nil {
		t.Fatalf("successful run still unhealthy: %v", err)
	}

	// A run abandoned by a crash (running past the stale threshold) is
	// unhealthy; a fresh in-progress run is not.
	insertBackupRun(t, platform, backupauto.StateRunning, 25, "")
	err = m.probeBackupOffsite(ctx)
	if err == nil || !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("abandoned run not reported: %v", err)
	}
	insertBackupRun(t, platform, backupauto.StateVerifying, 1, "")
	if err = m.probeBackupOffsite(ctx); err != nil {
		t.Fatalf("in-progress run treated unhealthy: %v", err)
	}
}

// TestBackupOffsiteReadinessTreatsMissingTableAsDisabled proves a database
// without migration 0120 (feature not provisioned) reads as healthy-absent
// instead of failing every apply.
func TestBackupOffsiteReadinessTreatsMissingTableAsDisabled(t *testing.T) {
	dsn := testdb.New(t)
	testdb.MigrateTo(t, dsn, 119) // one version before 0120_backup_runs
	platform, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(platform.Close)
	m := &ApplyManager{pool: platform}
	if err = m.probeBackupOffsite(context.Background()); err != nil {
		t.Fatalf("feature-absent installation must not fail readiness: %v", err)
	}
}

// TestNewServiceProbesSkipFixtureManagersWithoutDatabase documents the fixture
// contract: managers without a platform pool are covered by the injected
// readinessProbe, so unit tests stay independent of PostgreSQL.
func TestNewServiceProbesSkipFixtureManagersWithoutDatabase(t *testing.T) {
	m := &ApplyManager{}
	if err := m.probeNewServiceReadiness(context.Background()); err != nil {
		t.Fatalf("nil pool must be skipped, got %v", err)
	}
}
