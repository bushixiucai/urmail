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
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
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
						Records: []nsd.Record{
							{Name: "mail._domainkey", Type: "TXT", Value: "v=DKIM1; k=rsa; p=MIGfMA0GCS..."},
							{Name: "@", Type: "TXT", Value: "google-site-verification=xyz"},
						},
					},
					{
						Domain: "customer-delegated.org",
						Mode:   "delegated",
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
