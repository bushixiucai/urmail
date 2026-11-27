package agentd

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestCertSchedulerRunsWithJitterAndReloadsCompose(t *testing.T) {
	stateRoot := t.TempDir()
	renderDir := t.TempDir()

	var jitterCalled atomic.Bool
	var runs atomic.Int32
	sched := &CertScheduler{
		Interval:  15 * time.Millisecond,
		MaxJitter: 5 * time.Millisecond,
		Clock:     time.Now,
		RandInt64N: func(n int64) int64 {
			jitterCalled.Store(true)
			return 0
		},
		RenewFn: func(context.Context) error {
			runs.Add(1)
			return nil
		},
	}
	stop := sched.Start(t.Context())
	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if runs.Load() < 1 || !jitterCalled.Load() {
		t.Fatalf("expected CertScheduler to run with jitter (runs=%d jitter=%v)", runs.Load(), jitterCalled.Load())
	}

	// Verify atomic cert installation + compose reload stamps without systemd.
	mgr := &ApplyManager{
		root: stateRoot,
		cfg: &config.Config{
			MyHostname:        "mail.example.test",
			PrimaryDomain:     "example.test",
			RenderedConfigDir: renderDir,
		},
		run: func(context.Context, string, ...string) error {
			t.Fatal("compose cert reload must not invoke systemctl or nginx")
			return nil
		},
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mail.example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		DNSNames:     []string{"mail.example.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	if err := mgr.installCertificateFiles("mail.example.test", certPEM, keyPEM); err != nil {
		t.Fatalf("installCertificateFiles: %v", err)
	}
	if err := mgr.reloadComposeCertificates("mail.example.test"); err != nil {
		t.Fatalf("reloadComposeCertificates: %v", err)
	}
	for _, p := range []string{
		filepath.Join(stateRoot, "certs/mail/mail.example.test.cert"),
		filepath.Join(stateRoot, "certs/mail/mail.example.test.key"),
		filepath.Join(renderDir, "postfix/.cert-reload-stamp"),
		filepath.Join(renderDir, "dovecot/.cert-reload-stamp"),
	} {
		info, statErr := os.Stat(p)
		if statErr != nil || info.Size() == 0 {
			t.Fatalf("expected cert/reload artifact at %s: %v", p, statErr)
		}
	}
}
