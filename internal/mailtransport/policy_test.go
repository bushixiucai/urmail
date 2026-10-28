package mailtransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/maildnsreport"
)

type wireResolver struct {
	policy  maildnsreport.Publication
	private bool
	wrongID bool
}

func (r wireResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	if r.private {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("127.0.0.1")}, nil
	}
	return []net.IP{net.ParseIP("8.8.8.8")}, nil
}
func (r wireResolver) LookupTXT(context.Context, string) ([]string, error) {
	id := r.policy.Policy.ID
	if r.wrongID {
		id = "stale"
	}
	return []string{maildnsreport.MTASTSTXTRecord(id)}, nil
}
func (wireResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	return []*net.MX{{Host: "mail.platform.test.", Pref: 10}}, nil
}
func wireCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Transport wire fixture"}, DNSNames: []string{"mta-sts.transport.example.test", "mail.platform.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return certificate, roots
}
func TestPublicObserverActualHTTPSAndSTARTTLSWithControlledDNS(t *testing.T) {
	cert, roots := wireCertificate(t)
	publication := maildnsreport.Publication{Enabled: true, Policy: maildnsreport.MTASTSPolicy{ID: "stablewire1", Mode: maildnsreport.MTASTSTesting, MX: []string{"mail.platform.test"}, MaxAgeSeconds: 300}, RUA: "https://mail.platform.test/api/v1/tls-reports/transport.example.test"}
	body, err := maildnsreport.RenderWellKnown(publication.Policy)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "mta-sts.transport.example.test" || r.URL.Path != maildnsreport.WellKnownPath {
			t.Error("HTTP host/path not preserved")
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	}))
	web.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	web.StartTLS()
	defer web.Close()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
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
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				stream := textproto.NewConn(conn)
				_ = stream.PrintfLine("220 fixture ESMTP")
				if line, e := stream.ReadLine(); e != nil || !strings.HasPrefix(line, "EHLO ") {
					return
				}
				_ = stream.PrintfLine("250-fixture\r\n250 STARTTLS")
				if line, e := stream.ReadLine(); e != nil || line != "STARTTLS" {
					return
				}
				_ = stream.PrintfLine("220 Ready")
				secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if secure.HandshakeContext(t.Context()) != nil {
					return
				}
				tlsStream := textproto.NewConn(secure)
				if line, e := tlsStream.ReadLine(); e != nil || !strings.HasPrefix(line, "EHLO ") {
					return
				}
				_ = tlsStream.PrintfLine("250 fixture")
			}()
		}
	}()
	connections := &atomic.Int32{}
	observer := PublicObserver{resolver: wireResolver{policy: publication}, roots: roots, dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		connections.Add(1)
		target := web.Listener.Addr().String()
		if strings.HasSuffix(address, ":25") {
			target = listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}}
	checks, err := observer.Observe(t.Context(), "transport.example.test", "mail.platform.test", publication)
	if err != nil {
		t.Fatalf("verified wire checks %+v %v", checks, err)
	}
	for _, key := range []string{"dns_id", "mx", "https_policy", "smtp_tls"} {
		if !checks[key] {
			t.Fatalf("missing real check %s", key)
		}
	}
	observer.roots = x509.NewCertPool()
	checks, err = observer.Observe(t.Context(), "transport.example.test", "mail.platform.test", publication)
	if err == nil || checks["https_policy"] || checks["smtp_tls"] {
		t.Fatalf("untrusted certificate passed %+v", checks)
	}
	observer.resolver = wireResolver{policy: publication, private: true}
	before := connections.Load()
	checks, err = observer.Observe(t.Context(), "transport.example.test", "mail.platform.test", publication)
	if err == nil || connections.Load() != before {
		t.Fatalf("mixed private DNS reached transport %+v", checks)
	}
}
func TestPublicProbeSpecialUseAddressesAndPolicyCacheGuard(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.100.100.100", "0.2.3.4", "192.0.2.1", "198.18.0.1", "::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1"} {
		if publicIP(net.ParseIP(value)) {
			t.Fatalf("special-use address accepted %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"} {
		if !publicIP(net.ParseIP(value)) {
			t.Fatalf("public address rejected %s", value)
		}
	}
	now := time.Now().UTC()
	publication := maildnsreport.Publication{Enabled: true, Policy: maildnsreport.MTASTSPolicy{MX: []string{"old.example"}, Mode: maildnsreport.MTASTSEnforce}, CacheSafeAfter: now.Add(time.Hour)}
	if publication.CheckMXChange("new.example", now) == nil {
		t.Fatal("live enforcing cache allowed a receiving host change")
	}
	publication.Policy.Mode = maildnsreport.MTASTSNone
	if publication.CheckMXChange("new.example", now) == nil {
		t.Fatal("undrained old enforcement cache allowed a receiving host change")
	}
	if err := publication.CheckMXChange("new.example", now.Add(2*time.Hour)); err != nil {
		t.Fatal(fmt.Sprint(err))
	}
}
