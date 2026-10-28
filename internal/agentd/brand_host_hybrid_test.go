package agentd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/nginx"
)

func TestBrandHybridNginxOwnershipConflicts(t *testing.T) {
	domains := []nginx.DomainConfig{{FQDN: "brand.customer.test"}}
	for _, name := range []string{"brand.customer.test", "other.test brand.customer.test", "*.customer.test", ".customer.test", "brand.*", "~^brand\\.customer\\.test$", "~* ^BRAND\\.CUSTOMER\\.TEST$", `brand\.customer.test`, `$hostname`} {
		raw := "# configuration file /etc/nginx/operator.conf:\nserver { server_name " + name + "; }"
		if err := nginxBrandConflicts(raw, "/test/owned", domains); err == nil {
			t.Fatalf("existing name accepted: %s", name)
		}
	}
	for _, name := range []string{"other.customer.test", "_", "other.*", "*.another.test"} {
		if err := nginxBrandConflicts("server { server_name "+name+"; }", "/test/owned", domains); err != nil {
			t.Fatal(name, err)
		}
	}
	owned := "# configuration file " + brandManagedNginxPath + ":\nserver { server_name brand.customer.test; }"
	if err := nginxBrandConflicts(owned, "/test/owned", domains); err != nil {
		t.Fatal("own exclusive include collided", err)
	}
}

// This opt-in test starts and reloads a real isolated nginx master. It never
// runs against the host machine or an existing production nginx instance.
func TestBrandHybridNginxRealTools(t *testing.T) {
	if os.Getenv("URMAIL_REAL_BRAND_NGINX") != "1" {
		t.Skip("isolated real nginx not requested")
	}
	if os.Geteuid() != 0 {
		t.Fatal("fixture needs container root")
	}
	m := testManager(t)
	m.targetRoot = ""
	m.cfg.PreserveExternalWeb = true
	var logs strings.Builder
	failReload := false
	m.run = func(ctx context.Context, name string, args ...string) error {
		if name == "/usr/bin/systemctl" {
			if failReload {
				failReload = false
				return errors.New("injected reload failure")
			}
			name = "/usr/sbin/nginx"
			args = []string{"-s", "reload"}
		}
		data, e := exec.CommandContext(ctx, name, args...).CombinedOutput()
		logs.Write(data)
		return e
	}
	for _, dir := range []string{"/etc/nginx/conf.d", m.cfg.StorageRoot} {
		if e := os.MkdirAll(dir, 0755); e != nil {
			t.Fatal(e)
		}
	}
	base := []byte("events {}\nhttp { include /etc/nginx/conf.d/*.conf; }\n")
	if e := os.WriteFile("/etc/nginx/nginx.conf", base, 0644); e != nil {
		t.Fatal(e)
	}
	operator := []byte("server { listen 18082; server_name unrelated.operator.test; location / { return 200 'operator'; } }\n")
	if e := os.WriteFile("/etc/nginx/conf.d/operator.conf", operator, 0644); e != nil {
		t.Fatal(e)
	}
	if data, e := exec.CommandContext(t.Context(), "/usr/sbin/nginx").CombinedOutput(); e != nil {
		t.Fatalf("start nginx: %v %s", e, data)
	}
	defer func() { _ = exec.CommandContext(context.Background(), "/usr/sbin/nginx", "-s", "quit").Run() }()
	operatorClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	assertOperator := func() {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:18082/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = "unrelated.operator.test"
		response, err := operatorClient.Do(request)
		if err != nil {
			t.Fatal("existing operator site unavailable", err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "operator" {
			t.Fatalf("existing operator response %d %q %v", response.StatusCode, body, readErr)
		}
	}
	assertOperator()
	domain := nginx.DomainConfig{FQDN: "brand.customer.test", TLSCertFile: filepath.Join(m.root, "unused.cert"), TLSKeyFile: filepath.Join(m.root, "unused.key"), ACMEOnly: true}
	if e := m.applyHybridBrandFiles([]nginx.DomainConfig{domain}); e != nil {
		t.Fatal(e, logs.String())
	}
	first, e := os.ReadFile(brandManagedNginxPath)
	if e != nil || !bytes.HasPrefix(first, []byte(brandManagedNginxMarker)) {
		t.Fatal(e)
	}
	if strings.Contains(string(first), "listen 443") {
		t.Fatal("HTTP proof bootstrap exposed TLS product routes")
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("brand-isolated-runtime")) }))
	defer backend.Close()
	m.cfg.ListenAddr = strings.TrimPrefix(backend.URL, "http://")
	m.cfg.MyHostname = domain.FQDN
	if e = m.ensureBootstrap(ApplyParams{}); e != nil {
		t.Fatal(e)
	}
	paths := m.certificatePaths(domain.FQDN)
	domain.TLSCertFile = certificateCertPath(paths)
	domain.TLSKeyFile = certificateKeyPath(paths)
	domain.ACMEOnly = false
	if e = m.applyHybridBrandFiles([]nginx.DomainConfig{domain}); e != nil {
		t.Fatal(e, logs.String())
	}
	trust, e := os.ReadFile(domain.TLSCertFile)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(trust)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: domain.FQDN}}}
	request, e := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://127.0.0.1/", nil)
	if e != nil {
		t.Fatal(e)
	}
	request.Host = domain.FQDN
	response, e := client.Do(request)
	for deadline := time.Now().Add(3 * time.Second); e != nil && time.Now().Before(deadline); {
		time.Sleep(25 * time.Millisecond)
		response, e = client.Do(request)
	}
	if e != nil {
		t.Fatal("real nginx TLS proxy", e)
	}
	body, e := io.ReadAll(response.Body)
	response.Body.Close()
	if e != nil || string(body) != "brand-isolated-runtime" {
		t.Fatal("brand backend route", string(body), e)
	}
	assertOperator()
	first, e = os.ReadFile(brandManagedNginxPath)
	if e != nil {
		t.Fatal(e)
	}
	// A restart failure is injected after syntax validation; the actual rollback
	// reload still runs against the exact previous bytes.
	failReload = true
	domain.FQDN = "next.customer.test"
	if e = m.applyHybridBrandFiles([]nginx.DomainConfig{domain}); e == nil {
		t.Fatal("reload failure hidden")
	}
	restored, e := os.ReadFile(brandManagedNginxPath)
	if e != nil || !bytes.Equal(first, restored) {
		t.Fatal("managed file not restored exactly", e)
	}
	assertOperator()
	untouched, e := os.ReadFile("/etc/nginx/conf.d/operator.conf")
	if e != nil || !bytes.Equal(operator, untouched) {
		t.Fatal("operator site changed", e)
	}
	conflict := nginx.DomainConfig{FQDN: "unrelated.operator.test", TLSCertFile: domain.TLSCertFile, TLSKeyFile: domain.TLSKeyFile, ACMEOnly: true}
	if e = m.applyHybridBrandFiles([]nginx.DomainConfig{conflict}); e == nil {
		t.Fatal("existing operator hostname hijacked")
	}
	if e = os.WriteFile(brandManagedNginxPath, []byte("# operator-owned file\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e = m.applyHybridBrandFiles([]nginx.DomainConfig{domain}); e == nil {
		t.Fatal("unmanaged file overwritten")
	}
	t.Log("real nginx -T/-t/start/reload and exact failure rollback passed; unrelated site HTTP verified before/after publication and rollback, bytes unchanged")
}

func TestBrandHybridInitialFailureRemovesOnlyOwnedInclude(t *testing.T) {
	for _, loaded := range []bool{false, true} {
		t.Run(fmt.Sprintf("loaded-%v", loaded), func(t *testing.T) {
			m := testManager(t)
			m.cfg.PreserveExternalWeb = true
			failedReload := false
			m.output = func(_ context.Context, _ ...string) ([]byte, error) {
				raw := "# configuration file /etc/nginx/operator.conf:\nserver { server_name unrelated.test; }\n"
				if loaded {
					if _, err := os.Stat(m.target(brandManagedNginxPath)); err == nil {
						raw += "# configuration file " + m.target(brandManagedNginxPath) + ":\n"
					}
				}
				return []byte(raw), nil
			}
			m.run = func(_ context.Context, name string, args ...string) error {
				if loaded && name == "/usr/bin/systemctl" && len(args) > 0 && args[0] == "reload" && !failedReload {
					failedReload = true
					return errors.New("injected first reload failure")
				}
				return nil
			}
			domain := nginx.DomainConfig{FQDN: "brand.initial.test", TLSCertFile: "/tmp/test.cert", TLSKeyFile: "/tmp/test.key", ACMEOnly: true}
			if err := m.applyHybridBrandFiles([]nginx.DomainConfig{domain}); !errors.Is(err, ErrFailed) {
				t.Fatal("initial failure not surfaced", err)
			}
			if _, err := os.Lstat(m.target(brandManagedNginxPath)); !os.IsNotExist(err) {
				t.Fatal("initial failed include was retained", err)
			}
			if m.status.Degraded {
				t.Fatal("successful rollback falsely reported degraded")
			}
		})
	}
}
