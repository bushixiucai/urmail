package agentd

import (
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestFilterPreservedExternalFilesSkipsNSDWebAndOpenDKIM(t *testing.T) {
	cfg := &config.Config{PreserveExternalNSD: true, PreserveExternalWeb: true, PreserveExternalOpenDKIM: true}
	files := map[string]string{
		"/etc/postfix/main.cf":            "postfix",
		"/etc/dovecot/dovecot.conf":       "dovecot",
		"/etc/nsd/nsd.conf.d/zones.conf":  "zones",
		"/etc/nsd/zones/example.test.txt": "zone",
		"/etc/nginx/conf.d/mail.conf":     "nginx",
		"/etc/opendkim.conf":              "dkim",
		"/etc/opendkim/KeyTable":          "keys",
	}
	filtered := filterPreservedExternalFiles(cfg, files)
	if _, ok := filtered["/etc/postfix/main.cf"]; !ok {
		t.Fatal("mail config must still apply")
	}
	for _, path := range []string{"/etc/nsd/nsd.conf.d/zones.conf", "/etc/nsd/zones/example.test.txt", "/etc/nginx/conf.d/mail.conf", "/etc/opendkim.conf", "/etc/opendkim/KeyTable"} {
		if _, ok := filtered[path]; ok {
			t.Fatalf("preserved path leaked into apply set: %s", path)
		}
	}
	units := configUnits(cfg)
	for _, unit := range units {
		if unit == "nsd" || unit == "nginx" || unit == "opendkim" {
			t.Fatalf("preserved unit still scheduled for reload: %s", unit)
		}
	}
	if !preservedExternalPath(cfg, "/etc/nsd/zones/example.test.txt") || preservedExternalPath(cfg, "/etc/postfix/main.cf") {
		t.Fatal("path classification incorrect")
	}
}
