package mail_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
	"github.com/bushixiucai/urmail/internal/mail/nginx"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

func TestRenderAll(t *testing.T) {
	ctx := mail.ConfigContext{
		PostfixNode: postfix.NodeConfig{
			Hostname:      "box.urmail.cn",
			PrimaryDomain: "urmail.cn",
			StorageRoot:   "/var/mail",
			TLSCertFile:   "/var/mail/ssl/box.urmail.cn.cert",
			TLSKeyFile:    "/var/mail/ssl/box.urmail.cn.key",
		},
		DovecotNode: dovecot.NodeConfig{
			Hostname:      "box.urmail.cn",
			PrimaryDomain: "urmail.cn",
			StorageRoot:   "/var/mail",
			DatabaseURL:   "host=127.0.0.1 dbname=urmail user=urmail_dovecot",
		},
		NSDNode: nsd.NodeConfig{
			Hostname:      "box.urmail.cn",
			PrimaryDomain: "urmail.cn",
			PublicIPv4:    "1.2.3.4",
			Nameservers:   []string{"ns1.urmail.cn"},
		},
		NginxNode: nginx.NodeConfig{
			PrimaryDomain: "urmail.cn",
			StorageRoot:   "/var/mail",
			BackendHTTP:   "127.0.0.1:8080",
		},
	}

	files, err := mail.RenderAll(ctx)
	if err != nil {
		t.Fatalf("RenderAll failed: %v", err)
	}

	expectedPrefixes := []string{
		"/etc/postfix/main.cf",
		"/etc/postfix/master.cf",
		"/etc/postfix/urmail/virtual_mailbox.map",
		"/etc/postfix/urmail/virtual_alias.map",
		"/etc/postfix/urmail/sender_login.map",
		"/etc/dovecot/conf.d/urmail/dovecot.conf",
		"/etc/dovecot/conf.d/urmail/dovecot-sql.conf.ext",
		"/etc/dovecot/conf.d/urmail/db.conf",
		"/etc/dovecot/conf.d/urmail/dovecot-mailboxes.conf.ext",
		"/etc/dovecot/conf.d/urmail/dovecot-quota.conf.ext",
		"/etc/nsd/nsd.conf.d/zones.conf",
		"/etc/nginx/conf.d/urmail/local.conf",
	}

	for _, exp := range expectedPrefixes {
		if _, ok := files[exp]; !ok {
			t.Errorf("missing expected output path: %s", exp)
		}
	}

	for p, content := range files {
		if !strings.HasSuffix(content, "\n") {
			t.Errorf("file %s does not end with newline", p)
		}
	}
}