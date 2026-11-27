package postfix_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

func sampleSnapshot() postfix.Snapshot {
	return postfix.Snapshot{
		Domains: []postfix.Domain{{FQDN: "example.com", Status: "active"}},
		Mailboxes: []postfix.Mailbox{
			{Address: "user@example.com", Status: "active"},
		},
	}
}

// fileMapReference matches every file-based map spec in a rendered main.cf.
// Group 1 is the map type (hash/regexp/btree), group 2 the absolute path.
var fileMapReference = regexp.MustCompile(`(?m)^(?:virtual_mailbox_maps|virtual_alias_maps|recipient_bcc_maps|transport_maps|smtpd_sender_login_maps|header_checks)\s*=\s*(\w+):(.+)$`)

// TestConfigDirDrivesFileMapReferences pins the PM-S7 render-path contract:
// a compose deployment (URMAIL_RENDERED_CONFIG_DIR=/srv/urmail/config) must
// see every file map reference pointing at the rendered mount surface
// (<render root>/postfix/urmail/...), because Postfix resolves map specs by
// absolute path only and never rewrites them from -c. The empty ConfigDir
// keeps the historical host layout byte-for-byte.
func TestConfigDirDrivesFileMapReferences(t *testing.T) {
	node := defaultNodeConfig()
	node.ArchiveEnabled = true

	out, err := postfix.Generate(node, sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	mainCF := out[postfix.FileMainCF]
	for _, want := range []string{
		"hash:/etc/postfix/urmail/virtual_mailbox.map",
		"hash:/etc/postfix/urmail/virtual_alias.map",
		"hash:/etc/postfix/urmail/archive_bcc.map",
		"hash:/etc/postfix/urmail/archive_transport.map",
		"hash:/etc/postfix/urmail/sender_login.map",
		"regexp:/etc/postfix/urmail/spam-header-checks",
	} {
		if !strings.Contains(mainCF, want) {
			t.Fatalf("host layout lost %q:\n%s", want, mainCF)
		}
	}
	if strings.Contains(mainCF, "/srv/urmail/config/postfix") {
		t.Fatalf("host layout must not reference compose paths:\n%s", mainCF)
	}

	compose := node
	compose.ConfigDir = "/srv/urmail/config/postfix"
	out, err = postfix.Generate(compose, sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	mainCF = out[postfix.FileMainCF]
	for _, want := range []string{
		"hash:/srv/urmail/config/postfix/urmail/virtual_mailbox.map",
		"hash:/srv/urmail/config/postfix/urmail/virtual_alias.map",
		"hash:/srv/urmail/config/postfix/urmail/archive_bcc.map",
		"hash:/srv/urmail/config/postfix/urmail/archive_transport.map",
		"hash:/srv/urmail/config/postfix/urmail/sender_login.map",
		"regexp:/srv/urmail/config/postfix/urmail/spam-header-checks",
	} {
		if !strings.Contains(mainCF, want) {
			t.Fatalf("compose layout lost %q:\n%s", want, mainCF)
		}
	}
	if strings.Contains(mainCF, "/etc/postfix/urmail") {
		t.Fatalf("compose layout must not reference host paths:\n%s", mainCF)
	}
}

// TestMainCFReferencesMatchRenderedMaps is the mount-surface consistency
// assertion: every map main.cf references must exist in the generated file
// set under the same config dir, and every generated map must be referenced.
// A drift on either side is the exact class of production outage QA hit
// (referenced path absent from the mounted render surface).
func TestMainCFReferencesMatchRenderedMaps(t *testing.T) {
	node := defaultNodeConfig()
	node.ArchiveEnabled = true
	node.ConfigDir = "/srv/urmail/config/postfix"

	out, err := postfix.Generate(node, sampleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	// The generated map set, as absolute mount-surface paths.
	generated := map[string]bool{}
	for rel := range out {
		if strings.HasSuffix(rel, ".map") || rel == postfix.FileSpamHeaderChecks {
			generated[node.ConfigDir+"/"+rel] = true
		}
	}
	referenced := map[string]bool{}
	for _, m := range fileMapReference.FindAllStringSubmatch(out[postfix.FileMainCF], -1) {
		referenced[m[2]] = true
	}
	for ref := range referenced {
		if !generated[ref] {
			t.Fatalf("main.cf references %q but the generator does not render it", ref)
		}
	}
	for gen := range generated {
		if !referenced[gen] && gen != node.ConfigDir+"/"+postfix.FileSpamHeaderChecks {
			// header_checks is referenced via the regexp: spec; every .map must
			// be referenced by main.cf exactly once as a file map.
			t.Fatalf("generator renders %q but main.cf never references it", gen)
		}
	}
}

// TestConfigDirRejectsRelativePaths keeps the fail-closed posture: a relative
// config dir would silently produce map references that depend on the Postfix
// process working directory.
func TestConfigDirRejectsRelativePaths(t *testing.T) {
	node := defaultNodeConfig()
	node.ConfigDir = "relative/postfix"
	if _, err := postfix.Generate(node, sampleSnapshot()); err == nil {
		t.Fatal("relative ConfigDir must be rejected")
	}
}
