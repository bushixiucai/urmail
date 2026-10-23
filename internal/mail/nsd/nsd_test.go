package nsd_test

import (
	"flag"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/nsd"
)

var update = flag.Bool("update", false, "update golden files")

func defaultNodeConfig() nsd.NodeConfig {
	return nsd.NodeConfig{
		Hostname:      "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		PublicIPv4:    "1.2.3.4",
		PublicIPv6:    "2001:db8::1",
		Nameservers:   []string{"ns1.urmail.cn", "ns2.urmail.cn"},
	}
}

func TestPureFunctionConstraint(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly) //nolint:staticcheck // Source-only import policy intentionally includes all build variants.
	if err != nil {
		t.Fatalf("parse nsd package: %v", err)
	}

	disallowed := map[string]bool{
		"database/sql": true,
		"os/exec":      true,
		"os":           true,
	}

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			if strings.HasSuffix(filename, "_test.go") {
				continue
			}
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if disallowed[path] {
					t.Errorf("file %s imports disallowed package %s (generator must be pure)", filename, path)
				}
			}
		}
	}
}

func TestGoldenCases(t *testing.T) {
	cases := []struct {
		name string
		node nsd.NodeConfig
		snap nsd.Snapshot
	}{
		{
			name: "empty",
			node: defaultNodeConfig(),
			snap: nsd.Snapshot{},
		},
		{
			name: "managed_and_delegated",
			node: defaultNodeConfig(),
			snap: nsd.Snapshot{
				Zones: []nsd.Zone{
					{
						Domain: "customer-managed.com",
						Mode:   "managed",
						Serial: 2026102201,
						Records: []nsd.Record{
							{Name: "mail._domainkey", Type: "TXT", Value: "v=DKIM1; k=rsa; p=MIGfMA0GCS..."},
							{Name: "@", Type: "TXT", Value: "google-site-verification=xyz"},
						},
					},
					{
						Domain: "customer-delegated.org",
						Mode:   "delegated",
						Serial: 2026102201,
						Records: []nsd.Record{
							{Name: "@", Type: "MX", Prio: 10, Value: "box.urmail.cn."},
							{Name: "mail._domainkey", Type: "TXT", Value: "v=DKIM1; k=rsa; p=MIGf..."},
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := nsd.Generate(tc.node, tc.snap)
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}

			goldenDir := filepath.Join("testdata", "golden", tc.name)
			if *update {
				if err := os.MkdirAll(goldenDir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", goldenDir, err)
				}
			}

			for key, content := range out {
				if !strings.HasSuffix(content, "\n") {
					t.Errorf("%s does not end with newline", key)
				}
				safeFilename := strings.ReplaceAll(key, "/", "_")
				goldenFile := filepath.Join(goldenDir, safeFilename)
				if *update {
					if err := os.WriteFile(goldenFile, []byte(content), 0o644); err != nil {
						t.Fatalf("write golden file %s: %v", goldenFile, err)
					}
				} else {
					expected, err := os.ReadFile(goldenFile)
					if err != nil {
						t.Fatalf("read golden file %s: %v", goldenFile, err)
					}
					if string(expected) != content {
						t.Errorf("mismatch in %s:\n--- Expected ---\n%s\n--- Got ---\n%s", safeFilename, string(expected), content)
					}
				}
			}
		})
	}
}

func TestErrorPaths(t *testing.T) {
	node := defaultNodeConfig()

	t.Run("invalid_ipv4", func(t *testing.T) {
		bad := node
		bad.PublicIPv4 = "999.999.999.999"
		_, err := nsd.Generate(bad, nsd.Snapshot{})
		if err == nil || !strings.Contains(err.Error(), nsd.ErrInvalidConfig.Error()) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("invalid_zone_mode", func(t *testing.T) {
		snap := nsd.Snapshot{
			Zones: []nsd.Zone{{Domain: "example.com", Mode: "unknown"}},
		}
		_, err := nsd.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), nsd.ErrInvalidZone.Error()) {
			t.Errorf("expected ErrInvalidZone, got %v", err)
		}
	})

	t.Run("duplicate_zone", func(t *testing.T) {
		snap := nsd.Snapshot{
			Zones: []nsd.Zone{
				{Domain: "example.com", Mode: "managed"},
				{Domain: "EXAMPLE.COM.", Mode: "managed"},
			},
		}
		_, err := nsd.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), nsd.ErrInvalidZone.Error()) {
			t.Errorf("expected ErrInvalidZone, got %v", err)
		}
	})
}
func TestRecordValidationRejectsInjection(t *testing.T) {
	node := defaultNodeConfig()

	cases := []struct {
		name   string
		record nsd.Record
	}{
		{"bad_type", nsd.Record{Name: "@", Type: "SOA", Value: "ns1.example.com. hostmaster.example.com. 1"}},
		{"unknown_type", nsd.Record{Name: "@", Type: "PTR", Value: "x"}},
		{"name_with_dot_injection", nsd.Record{Name: "a.example.com. IN TXT \"x\"", Type: "A", Value: "1.2.3.4"}},
		{"name_empty_ok_but_long", nsd.Record{Name: string(make([]byte, 0)) + "a b", Type: "A", Value: "1.2.3.4"}},
		{"txt_with_quote", nsd.Record{Name: "@", Type: "TXT", Value: "v=spf1\" IN TXT \"evil"}},
		{"txt_with_newline", nsd.Record{Name: "@", Type: "TXT", Value: "line1\nline2"}},
		{"txt_empty", nsd.Record{Name: "@", Type: "TXT", Value: ""}},
		{"a_bad_ip", nsd.Record{Name: "@", Type: "A", Value: "999.1.1.1"}},
		{"aaaa_bad_ip", nsd.Record{Name: "@", Type: "AAAA", Value: "1.2.3.4"}},
		{"mx_bad_domain", nsd.Record{Name: "@", Type: "MX", Prio: 10, Value: "not a domain"}},
		{"cname_bad_domain", nsd.Record{Name: "www", Type: "CNAME", Value: "-bad..domain"}},
		{"srv_three_fields", nsd.Record{Name: "_sip._tcp", Type: "SRV", Value: "10 60 5060"}},
		{"srv_bad_port", nsd.Record{Name: "_sip._tcp", Type: "SRV", Value: "10 60 99999 sip.example.com."}},
		{"caa_two_fields", nsd.Record{Name: "@", Type: "CAA", Value: "0 issue"}},
		{"caa_bad_tag", nsd.Record{Name: "@", Type: "CAA", Value: "0 bogus letsencrypt.org"}},
		{"ns_bad_domain", nsd.Record{Name: "@", Type: "NS", Value: "ns..example.com"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := nsd.Snapshot{
				Zones: []nsd.Zone{{
					Domain:  "example.com",
					Mode:    "delegated",
					Serial:  1,
					Records: []nsd.Record{tc.record},
				}},
			}
			_, err := nsd.Generate(node, snap)
			if err == nil {
				t.Fatalf("record %+v must be rejected", tc.record)
			}
		})
	}
}

func TestRecordValidationAcceptsValidRecords(t *testing.T) {
	node := defaultNodeConfig()
	snap := nsd.Snapshot{
		Zones: []nsd.Zone{{
			Domain: "example.com",
			Mode:   "delegated",
			Serial: 1,
			Records: []nsd.Record{
				{Name: "@", Type: "A", Value: "192.0.2.1"},
				{Name: "@", Type: "AAAA", Value: "2001:db8::1"},
				{Name: "www", Type: "CNAME", Value: "example.com."},
				{Name: "@", Type: "MX", Prio: 10, Value: "box.urmail.cn."},
				{Name: "ns2", Type: "NS", Value: "ns2.example.com."},
				{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=none"},
				{Name: "@", Type: "SPF", Value: "v=spf1 mx -all"},
				{Name: "_sip._tcp", Type: "SRV", Value: "10 60 5060 sip.example.com."},
				{Name: "@", Type: "CAA", Value: "0 issue letsencrypt.org"},
				{Name: "x", Type: "CAA", Value: "128 iodef mailto:sec@example.com"},
				{Name: "", Type: "TXT", Value: "apex defaults to @"},
			},
		}},
	}
	out, err := nsd.Generate(node, snap)
	if err != nil {
		t.Fatalf("valid records must be accepted: %v", err)
	}
	zone := out["zones/example.com.txt"]
	for _, banned := range []string{"line1\nline2", "IN TXT \"evil"} {
		if strings.Contains(zone, banned) {
			t.Fatalf("zone file contains injected text %q", banned)
		}
	}
	if !strings.Contains(zone, "IN TXT \"v=DMARC1; p=none\"") {
		t.Fatalf("TXT value must be quoted: %s", zone)
	}
}

func TestLongTXTSplitsIntoCharacterStrings(t *testing.T) {
	node := defaultNodeConfig()
	snap := nsd.Snapshot{Zones: []nsd.Zone{{
		Domain: "long.example",
		Mode:   "managed",
		Serial: 2026102201,
		Records: []nsd.Record{
			{Name: "mail._domainkey", Type: "TXT", Value: "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 300)},
		},
	}}}
	out, err := nsd.Generate(node, snap)
	if err != nil {
		t.Fatal(err)
	}
	var content string
	for _, v := range out {
		if strings.Contains(v, "mail._domainkey") {
			content = v
		}
	}
	if content == "" {
		t.Fatal("zone content not generated")
	}
	line := ""
	for _, candidate := range strings.Split(content, "\n") {
		if strings.Contains(candidate, "mail._domainkey") {
			line = candidate
		}
	}
	if strings.Count(line, "\"") < 4 {
		t.Fatalf("long TXT was not split: %s", line)
	}
}
