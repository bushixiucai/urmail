package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// selfSignedPEM generates a short-lived certificate for pure-function probes.
func selfSignedPEM(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "probe.example.test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{"probe.example.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCertificateDaysRemainingPureFunction(t *testing.T) {
	now := time.Now()
	pemBytes := selfSignedPEM(t, now.Add(-time.Hour), now.Add(29*24*time.Hour))
	days, err := CertificateDaysRemaining(now, pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if days < 28.9 || days > 29.1 {
		t.Fatalf("days remaining = %f, want ~29", days)
	}
	expired := selfSignedPEM(t, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	days, err = CertificateDaysRemaining(now, expired)
	if err != nil {
		t.Fatal(err)
	}
	if days >= 0 {
		t.Fatalf("expired certificate days = %f, want negative", days)
	}
	if _, err = CertificateDaysRemaining(now, []byte("not pem")); err == nil {
		t.Fatal("garbage input must error")
	}
	if _, err = CertificateDaysRemaining(now, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})); err == nil {
		t.Fatal("non-certificate block must error")
	}
}

func TestCertificateExpirySeverityTiers(t *testing.T) {
	cases := []struct {
		days  float64
		level string
	}{
		{40, SeverityInfo},
		{30, SeverityInfo},
		{29.5, SeverityInfo},
		{14, SeverityWarning},
		{10, SeverityWarning},
		{3, SeverityCritical},
		{1, SeverityCritical},
		{-1, SeverityCritical},
	}
	for _, c := range cases {
		if got := CertificateExpirySeverity(c.days); got != c.level {
			t.Fatalf("CertificateExpirySeverity(%f) = %s, want %s", c.days, got, c.level)
		}
	}
}

func TestAlertSeverityMapping(t *testing.T) {
	if AlertSeverity("telemetry_backup_failed") != SeverityCritical {
		t.Fatal("backup failure must be critical")
	}
	if AlertSeverity("telemetry_backup_stale") != SeverityWarning {
		t.Fatal("backup staleness must be warning")
	}
	if AlertSeverity("totally_unknown") != SeverityInfo {
		t.Fatal("unknown kinds stay visible at info")
	}
}

func TestBackupConditionsThresholds(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Hour)
	base := BackupObservation{Now: now, AvailableState: true, MaxAgeSeconds: 26 * 3600, LatestState: "success", LastSuccessAt: &fresh}
	stale, failed := BackupConditions(base)
	if stale || failed {
		t.Fatalf("fresh backup fired: stale=%v failed=%v", stale, failed)
	}
	old := now.Add(-27 * time.Hour)
	stale, failed = BackupConditions(BackupObservation{Now: now, AvailableState: true, MaxAgeSeconds: 26 * 3600, LatestState: "success", LastSuccessAt: &old})
	if !stale || failed {
		t.Fatalf("27h-old success must fire stale: stale=%v failed=%v", stale, failed)
	}
	stale, failed = BackupConditions(BackupObservation{Now: now, AvailableState: true, MaxAgeSeconds: 26 * 3600, LatestState: "failed", LastSuccessAt: &fresh})
	if stale || !failed {
		t.Fatalf("failed latest run must fire failed only: stale=%v failed=%v", stale, failed)
	}
	stale, _ = BackupConditions(BackupObservation{Now: now, AvailableState: true, MaxAgeSeconds: 26 * 3600, LatestState: "running", LastSuccessAt: nil})
	if !stale {
		t.Fatal("no successful run must fire stale")
	}
	stale, failed = BackupConditions(BackupObservation{Now: now, AvailableState: false})
	if !stale || failed {
		t.Fatal("unavailable backup state must fire stale")
	}
	// Default threshold applies when unset.
	stale, _ = BackupConditions(BackupObservation{Now: now, AvailableState: true, LatestState: "success", LastSuccessAt: &old})
	if !stale {
		t.Fatal("default 26h threshold must apply")
	}
}

func TestConditionsIncludeBackupKinds(t *testing.T) {
	observation := healthyObservation()
	conditions, _ := Conditions(observation, DefaultThresholds())
	if conditions["telemetry_backup_stale"] || conditions["telemetry_backup_failed"] {
		t.Fatal("healthy evaluation fired backup alerts")
	}
	conditions, _ = Conditions(Observation{Now: time.Now()}, DefaultThresholds())
	if !conditions["telemetry_backup_stale"] {
		t.Fatal("missing backup facts must fire stale, not stay silent")
	}
}

func TestProbeCertificateDaysRemainingTLSEndpoint(t *testing.T) {
	now := time.Now()
	key, keyErr := rsa.GenerateKey(rand.Reader, 2048)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "probe.example.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(20 * 24 * time.Hour), DNSNames: []string{"probe.example.test"}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
			_ = connection.(*tls.Conn).HandshakeContext(context.Background())
			_ = connection.Close()
		}
	}()
	days, err := ProbeCertificateDaysRemaining(context.Background(), listener.Addr().String(), now)
	if err != nil {
		t.Fatal(err)
	}
	if days < 19.9 || days > 20.1 {
		t.Fatalf("probed days = %f, want ~20", days)
	}
	if _, err = ProbeCertificateDaysRemaining(context.Background(), "127.0.0.1:1", now); err == nil {
		t.Fatal("closed endpoint must error")
	}
}
