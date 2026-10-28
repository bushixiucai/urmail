package agentd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

func TestBrandHostTrustedCertificateLifecyclePG(t *testing.T) {
	for _, hybrid := range []bool{false, true} {
		t.Run(fmt.Sprintf("hybrid-%v", hybrid), func(t *testing.T) {
			platform, _ := testdb.OpenMigrated(t)
			q := store.New(platform)
			ctx := t.Context()
			m := testManager(t)
			m.pool = platform
			m.cfg.PreserveExternalWeb = hybrid
			if hybrid {
				m.output = func(_ context.Context, _ ...string) ([]byte, error) {
					base := "# configuration file /etc/nginx/operator.conf:\nserver { server_name unrelated.test; }\n"
					if raw, e := os.ReadFile(m.target(brandManagedNginxPath)); e == nil {
						base += "# configuration file " + m.target(brandManagedNginxPath) + ":\n" + string(raw)
					}
					return []byte(base), nil
				}
			}

			var brand int64
			if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('brand-issuer','Issuer') RETURNING id").Scan(&brand); err != nil {
				t.Fatal(err)
			}
			var actor int64
			if e := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('brand-agent-admin@example.test','fixture') RETURNING id").Scan(&actor); e != nil {
				t.Fatal(e)
			}
			if _, e := platform.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", actor); e != nil {
				t.Fatal(e)
			}
			const host = "brand.issuer.test"
			if _, err := q.PlatformBindBrandHost(ctx, store.PlatformBindBrandHostParams{Hostname: host, ID: brand}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.IssueCertificate(json.RawMessage(`{"domain":"brand.issuer.test"}`)); !errors.Is(err, ErrRejected) {
				t.Fatal("unverified host reached issuer", err)
			}
			if _, err := platform.Exec(ctx, "UPDATE brand_hosts SET status='verified',verified_at=now() WHERE hostname=$1", host); err != nil {
				t.Fatal(err)
			}
			before, err := q.PlatformGetBrandHostLifecycle(ctx, host)
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := m.render(ApplyParams{})
			if err != nil {
				t.Fatal(err)
			}
			config := rendered["/etc/nginx/conf.d/urmail/local.conf"]
			index := strings.Index(config, "server_name "+host)
			if index < 0 {
				t.Fatal("brand ACME vhost missing")
			}
			section := config[index:]
			if !strings.Contains(section, "acme-challenge") || strings.Contains(section, "ssl_certificate "+certificateCertPath(m.certificatePaths(host))) {
				t.Fatal("verified host exposed HTTPS before issuance")
			}
			ca, key, caPEM := certificateAuthority(t)
			caPath := filepath.Join(m.root, "brand-ca.pem")
			if err = os.WriteFile(caPath, caPEM, 0600); err != nil {
				t.Fatal(err)
			}
			m.cfg.ACMERootCA = caPath
			failReload := false
			revokeWhileIssuing := false
			requests := 0
			m.run = func(ctx context.Context, path string, args ...string) error {
				if path == "/usr/bin/certbot" {
					requests++
					values := map[string]string{}
					for i := 0; i+1 < len(args); i++ {
						values[args[i]] = args[i+1]
					}
					csr, e := os.ReadFile(values["--csr"])
					if e != nil {
						return e
					}
					if revokeWhileIssuing {
						if _, e = q.PlatformUnbindBrandHost(ctx, host); e != nil {
							return e
						}
					}
					return os.WriteFile(values["--fullchain-path"], signedCSR(t, csr, ca, key, caPEM, host, time.Now().Add(90*24*time.Hour)), 0600)
				}
				if failReload && path == "/usr/bin/systemctl" && len(args) > 1 && args[0] == "reload" && args[1] == "nginx" {
					return errors.New("injected reload failure")
				}
				return testCommand(m, path, args...)
			}
			raw, _ := json.Marshal(BrandHostParams{ActorID: actor, Hostname: host, BrandID: brand, VerifiedAt: before.VerifiedAt.Time})
			missing, _ := json.Marshal(BrandHostParams{Hostname: host, BrandID: brand, VerifiedAt: before.VerifiedAt.Time})
			if _, _, e := m.ActivateBrandHost(missing); !errors.Is(e, ErrRejected) {
				t.Fatal("missing actor accepted", e)
			}
			if _, _, err = m.ActivateBrandHost(raw); err != nil {
				t.Fatalf("%v; apply status: %+v", err, m.status)
			}
			live, err := q.PlatformGetBrandHostLifecycle(ctx, host)
			if err != nil || live.Status != "active" || !live.CertIssuedAt.Valid || !live.ActivatedAt.Valid {
				t.Fatal("not active after applied config", live, err)
			}
			certPath, keyPath := certificateCertPath(m.certificatePaths(host)), certificateKeyPath(m.certificatePaths(host))
			certificate, err := os.ReadFile(certPath)
			if err != nil {
				t.Fatal(err)
			}
			private, err := os.ReadFile(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = validateCertificate(certificate, private, host, caPath, time.Now()); err != nil {
				t.Fatal(err)
			}
			pair, err := tls.X509KeyPair(certificate, private)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				for {
					conn, e := listener.Accept()
					if e != nil {
						return
					}
					go func() {
						defer conn.Close()
						if c, ok := conn.(*tls.Conn); ok {
							_ = c.HandshakeContext(ctx)
						}
					}()
				}
			}()
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(caPEM)
			connection, err := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: roots}}).DialContext(ctx, "tcp", listener.Addr().String())
			if err != nil {
				t.Fatal("actual trusted TLS handshake", err)
			}
			connection.Close()
			if wrong, e := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "other.issuer.test", RootCAs: roots}}).DialContext(ctx, "tcp", listener.Addr().String()); e == nil {
				wrong.Close()
				t.Fatal("wrong name accepted")
			}
			failReload = true
			if _, _, err = m.IssueCertificate(json.RawMessage(`{"domain":"brand.issuer.test"}`)); err == nil {
				t.Fatal("reload failure hidden")
			}
			after, err := os.ReadFile(certPath)
			if err != nil || !bytes.Equal(certificate, after) {
				t.Fatal("last certificate lost on reload failure", err)
			}
			failReload = false
			revokeWhileIssuing = true
			if _, _, err = m.IssueCertificate(json.RawMessage(`{"domain":"brand.issuer.test"}`)); err == nil {
				t.Fatal("concurrent revocation accepted")
			}
			after, err = os.ReadFile(certPath)
			if err != nil || !bytes.Equal(certificate, after) {
				t.Fatal("revoked issuance replaced certificate", err)
			}
			if hybrid {
				if _, _, e := m.Apply(emptyApply()); e != nil {
					t.Fatal("revoke config convergence", e)
				}
				content, e := os.ReadFile(m.target(brandManagedNginxPath))
				if e != nil || strings.Contains(string(content), "server_name "+host) {
					t.Fatal("revoked hybrid route remains", e)
				}
			}
			if requests != 3 {
				t.Fatal("unexpected issuer calls", requests)
			}
			if m.certificateProvisioningAllowed(host) {
				t.Fatal("revoked brand can renew")
			}
			domains, err := m.brandNginxDomains()
			if err != nil || len(domains) != 0 {
				t.Fatal("revoked brand in nginx snapshot", domains, err)
			}
		})
	}
}
