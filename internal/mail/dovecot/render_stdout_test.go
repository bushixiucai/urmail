package dovecot_test

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/dovecot"
)

// TestComposeDovecotLogsToStdout is the QA TC-12 regression for the dovecot
// side: with the compose IncludePrefix injected the rendered dovecot.conf
// must point all log streams at stdout (docker logs); host renders keep the
// syslog default.
func TestComposeDovecotLogsToStdout(t *testing.T) {
	compose := dovecot.NodeConfig{
		Hostname:      "box.example.test",
		PrimaryDomain: "example.test",
		StorageRoot:   "/srv/urmail/data/mail",
		TLSCertFile:   "/certs/mail.cert",
		TLSKeyFile:    "/certs/mail.key",
		AuthEngine:    "sqlite",
		AuthDBPath:    "/var/lib/urmail/dovecot-auth.db",
		IncludePrefix: "/srv/urmail/config/dovecot",
	}
	files, err := dovecot.Generate(compose)
	if err != nil {
		t.Fatal(err)
	}
	conf := files[dovecot.FileDovecotConf]
	for _, want := range []string{"log_path = /dev/stdout", "info_log_path = /dev/stdout", "debug_log_path = /dev/stdout"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("compose dovecot.conf missing %q:\n%s", want, conf)
		}
	}

	host := compose
	host.IncludePrefix = ""
	files, err = dovecot.Generate(host)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files[dovecot.FileDovecotConf], "log_path") {
		t.Fatalf("host dovecot.conf must keep the syslog default:\n%s", files[dovecot.FileDovecotConf])
	}
}
