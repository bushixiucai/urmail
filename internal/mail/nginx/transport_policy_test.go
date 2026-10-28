package nginx_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/nginx"
)

func TestTransportHostExposesOnlyPolicyAndACME(t *testing.T) {
	files, err := nginx.Generate(defaultNodeConfig(), nginx.Snapshot{Domains: []nginx.DomainConfig{{FQDN: "mta-sts.customer.test", TLSCertFile: "/certs/mta.cert", TLSKeyFile: "/certs/mta.key", TransportOnly: true, EnableWebmail: true}}})
	if err != nil {
		t.Fatal(err)
	}
	text := files[nginx.FileLocalConf]
	for _, required := range []string{"server_name mta-sts.customer.test;", "location = /.well-known/mta-sts.txt", "location /.well-known/acme-challenge/", "location / { return 404; }", "proxy_set_header Host $host;"} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"location = /mail", "/Microsoft-Server-ActiveSync", "proxy_pass http://127.0.0.1:8888", "proxy_pass http://127.0.0.1:8889"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("transport host exposed %s", forbidden)
		}
	}
}
