package postfix_test

import (
	"flag"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

var update = flag.Bool("update", false, "update golden files")

func defaultNodeConfig() postfix.NodeConfig {
	return postfix.NodeConfig{
		Hostname:       "box.urmail.cn",
		PrimaryDomain:  "urmail.cn",
		StorageRoot:    "/var/mail",
		TLSCertFile:    "/var/mail/ssl/box.urmail.cn.cert",
		TLSKeyFile:     "/var/mail/ssl/box.urmail.cn.key",
		PolicyEnabled:  false,
		PolicySocket:   "private/policy",
		ArchiveEnabled: false,
		ArchiveSocket:  "private/urmail-archive",
	}
}

// TestPureFunctionConstraint guarantees that the postfix package does not import
// database/sql or os/exec.
func TestPureFunctionConstraint(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse postfix package: %v", err)
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

// TestGoldenCases tests the 6 standard golden cases.
func TestGoldenCases(t *testing.T) {
	cases := []struct {
		name string
		node postfix.NodeConfig
		snap postfix.Snapshot
	}{
		{
			name: "empty",
			node: defaultNodeConfig(),
			snap: postfix.Snapshot{},
		},
		{
			name: "single_tenant",
			node: defaultNodeConfig(),
			snap: postfix.Snapshot{
				Domains: []postfix.Domain{
					{FQDN: "example.com", Status: "active"},
				},
				Mailboxes: []postfix.Mailbox{
					{Address: "alice@example.com", Status: "active"},
					{Address: "bob@example.com", Status: "active"},
				},
				Aliases: []postfix.Alias{
					{Domain: "example.com", LocalPart: "info", Target: "alice@example.com", Kind: "alias"},
				},
			},
		},
		{
			name: "multi_tenant",
			node: defaultNodeConfig(),
			snap: postfix.Snapshot{
				Domains: []postfix.Domain{
					{FQDN: "tenant-c.org", Status: "active"},
					{FQDN: "tenant-a.com", Status: "active"},
					{FQDN: "tenant-b.net", Status: "active"},
				},
				Mailboxes: []postfix.Mailbox{
					{Address: "admin@tenant-b.net", Status: "active"},
					{Address: "user@tenant-a.com", Status: "active"},
					{Address: "root@tenant-c.org", Status: "active"},
					{Address: "admin@tenant-a.com", Status: "active"},
				},
				Aliases: []postfix.Alias{
					{Domain: "tenant-a.com", LocalPart: "contact", Target: "admin@tenant-a.com", Kind: "alias"},
					{Domain: "tenant-b.net", LocalPart: "support", Target: "admin@tenant-b.net", Kind: "alias"},
				},
			},
		},
		{
			name: "catchall",
			node: defaultNodeConfig(),
			snap: postfix.Snapshot{
				Domains: []postfix.Domain{
					{FQDN: "example.com", Status: "active"},
				},
				Mailboxes: []postfix.Mailbox{
					{Address: "admin@example.com", Status: "active"},
					{Address: "staff@example.com", Status: "active"},
				},
				Aliases: []postfix.Alias{
					{Domain: "example.com", LocalPart: "admin-alias", Target: "admin@example.com", Kind: "alias"},
					{Domain: "example.com", LocalPart: "forwarder", Target: "external@gmail.com", Kind: "forward"},
					{Domain: "example.com", LocalPart: "", Target: "staff@example.com", Kind: "catchall"},
				},
			},
		},
		{
			name: "policy_on",
			node: func() postfix.NodeConfig {
				n := defaultNodeConfig()
				n.PolicyEnabled = true
				n.PolicySocket = "private/urmail-policy"
				return n
			}(),
			snap: postfix.Snapshot{
				Domains: []postfix.Domain{
					{FQDN: "example.com", Status: "active"},
				},
				Mailboxes: []postfix.Mailbox{
					{Address: "user@example.com", Status: "active"},
				},
			},
		},
		{
			name: "policy_off",
			node: func() postfix.NodeConfig {
				n := defaultNodeConfig()
				n.PolicyEnabled = false
				return n
			}(),
			snap: postfix.Snapshot{
				Domains: []postfix.Domain{
					{FQDN: "example.com", Status: "active"},
				},
				Mailboxes: []postfix.Mailbox{
					{Address: "user@example.com", Status: "active"},
				},
			},
		},
	}

	expectedFiles := []struct {
		key      string
		filename string
	}{
		{postfix.FileMainCF, "main.cf"},
		{postfix.FileMasterCF, "master.cf"},
		{postfix.FileVirtualMailboxMap, "virtual_mailbox.map"},
		{postfix.FileVirtualAliasMap, "virtual_alias.map"},
		{postfix.FileSenderLoginMap, "sender_login.map"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := postfix.Generate(tc.node, tc.snap)
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}

			// Key set check: exactly 5 keys
			if len(out) != 5 {
				t.Fatalf("expected exactly 5 files, got %d", len(out))
			}

			goldenDir := filepath.Join("testdata", "golden", tc.name)
			if *update {
				if err := os.MkdirAll(goldenDir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", goldenDir, err)
				}
			}

			for _, ef := range expectedFiles {
				content, ok := out[ef.key]
				if !ok {
					t.Errorf("missing output key %q", ef.key)
					continue
				}

				// Common structure checks
				if !strings.HasSuffix(content, "\n") {
					t.Errorf("%s does not end with newline", ef.key)
				}
				if !strings.HasPrefix(content, "# Generated by urmaild. DO NOT EDIT.\n") {
					t.Errorf("%s missing header banner", ef.key)
				}

				goldenFile := filepath.Join(goldenDir, ef.filename)
				if *update {
					if err := os.WriteFile(goldenFile, []byte(content), 0o644); err != nil {
						t.Fatalf("write golden file %s: %v", goldenFile, err)
					}
				} else {
					expected, err := os.ReadFile(goldenFile)
					if err != nil {
						t.Fatalf("read golden file %s: %v (run with -update to generate)", goldenFile, err)
					}
					if string(expected) != content {
						t.Errorf("content mismatch in %s for case %s:\n--- Expected ---\n%s\n--- Got ---\n%s", ef.filename, tc.name, string(expected), content)
					}
				}
			}
		})
	}
}

// TestDeterminism ensures repeated runs on identical snapshot produce byte-for-byte identical output.
func TestDeterminism(t *testing.T) {
	node := defaultNodeConfig()
	snap := postfix.Snapshot{
		Domains: []postfix.Domain{
			{FQDN: "z.com", Status: "active"},
			{FQDN: "a.com", Status: "active"},
			{FQDN: "m.com", Status: "active"},
		},
		Mailboxes: []postfix.Mailbox{
			{Address: "z@z.com", Status: "active"},
			{Address: "a@a.com", Status: "active"},
			{Address: "b@a.com", Status: "active"},
		},
		Aliases: []postfix.Alias{
			{Domain: "z.com", LocalPart: "info", Target: "z@z.com", Kind: "alias"},
			{Domain: "a.com", LocalPart: "", Target: "a@a.com", Kind: "catchall"},
			{Domain: "a.com", LocalPart: "contact", Target: "b@a.com", Kind: "alias"},
		},
	}

	out1, err := postfix.Generate(node, snap)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}

	out2, err := postfix.Generate(node, snap)
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}

	for k, v1 := range out1 {
		v2 := out2[k]
		if v1 != v2 {
			t.Errorf("output %s is not deterministic", k)
		}
	}
}

// TestErrorPaths tests the error paths without golden files.
func TestErrorPaths(t *testing.T) {
	node := defaultNodeConfig()

	t.Run("invalid_config_hostname", func(t *testing.T) {
		badNode := node
		badNode.Hostname = "invalid_hostname"
		_, err := postfix.Generate(badNode, postfix.Snapshot{})
		if err == nil || !strings.Contains(err.Error(), postfix.ErrInvalidConfig.Error()) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("invalid_status_domain", func(t *testing.T) {
		snap := postfix.Snapshot{
			Domains: []postfix.Domain{
				{FQDN: "example.com", Status: "disabled"},
			},
		}
		_, err := postfix.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), postfix.ErrInvalidStatus.Error()) {
			t.Errorf("expected ErrInvalidStatus, got %v", err)
		}
	})

	t.Run("invalid_status_mailbox", func(t *testing.T) {
		snap := postfix.Snapshot{
			Domains: []postfix.Domain{
				{FQDN: "example.com", Status: "active"},
			},
			Mailboxes: []postfix.Mailbox{
				{Address: "user@example.com", Status: "suspended"},
			},
		}
		_, err := postfix.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), postfix.ErrInvalidStatus.Error()) {
			t.Errorf("expected ErrInvalidStatus, got %v", err)
		}
	})

	t.Run("alias_conflict_duplicate_alias", func(t *testing.T) {
		snap := postfix.Snapshot{
			Domains: []postfix.Domain{
				{FQDN: "example.com", Status: "active"},
			},
			Mailboxes: []postfix.Mailbox{
				{Address: "user@example.com", Status: "active"},
			},
			Aliases: []postfix.Alias{
				{Domain: "example.com", LocalPart: "info", Target: "user@example.com", Kind: "alias"},
				{Domain: "example.com", LocalPart: "info", Target: "ext@other.com", Kind: "forward"},
			},
		}
		_, err := postfix.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), postfix.ErrAliasConflict.Error()) {
			t.Errorf("expected ErrAliasConflict, got %v", err)
		}
	})

	t.Run("alias_conflict_with_mailbox", func(t *testing.T) {
		snap := postfix.Snapshot{
			Domains: []postfix.Domain{
				{FQDN: "example.com", Status: "active"},
			},
			Mailboxes: []postfix.Mailbox{
				{Address: "info@example.com", Status: "active"},
			},
			Aliases: []postfix.Alias{
				{Domain: "example.com", LocalPart: "info", Target: "other@example.com", Kind: "alias"},
			},
		}
		_, err := postfix.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), postfix.ErrAliasConflict.Error()) {
			t.Errorf("expected ErrAliasConflict, got %v", err)
		}
	})

	t.Run("alias_cycle_direct_and_chain", func(t *testing.T) {
		// A -> B -> C -> A
		snap := postfix.Snapshot{
			Domains: []postfix.Domain{
				{FQDN: "example.com", Status: "active"},
			},
			Aliases: []postfix.Alias{
				{Domain: "example.com", LocalPart: "a", Target: "b@example.com", Kind: "alias"},
				{Domain: "example.com", LocalPart: "b", Target: "c@example.com", Kind: "alias"},
				{Domain: "example.com", LocalPart: "c", Target: "a@example.com", Kind: "alias"},
			},
		}
		_, err := postfix.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), postfix.ErrAliasCycle.Error()) {
			t.Errorf("expected ErrAliasCycle, got %v", err)
		}
	})

	t.Run("alias_self_loop", func(t *testing.T) {
		snap := postfix.Snapshot{
			Domains: []postfix.Domain{
				{FQDN: "example.com", Status: "active"},
			},
			Aliases: []postfix.Alias{
				{Domain: "example.com", LocalPart: "self", Target: "self@example.com", Kind: "alias"},
			},
		}
		_, err := postfix.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), postfix.ErrAliasCycle.Error()) {
			t.Errorf("expected ErrAliasCycle, got %v", err)
		}
	})

	t.Run("invalid_catchall_local_part", func(t *testing.T) {
		snap := postfix.Snapshot{
			Domains: []postfix.Domain{
				{FQDN: "example.com", Status: "active"},
			},
			Aliases: []postfix.Alias{
				{Domain: "example.com", LocalPart: "notempty", Target: "target@example.com", Kind: "catchall"},
			},
		}
		_, err := postfix.Generate(node, snap)
		if err == nil || !strings.Contains(err.Error(), postfix.ErrInvalidAlias.Error()) {
			t.Errorf("expected ErrInvalidAlias, got %v", err)
		}
	})

	t.Run("empty_tls_cert_file", func(t *testing.T) {
		badNode := node
		badNode.TLSCertFile = ""
		_, err := postfix.Generate(badNode, postfix.Snapshot{})
		if err == nil || !strings.Contains(err.Error(), postfix.ErrInvalidConfig.Error()) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("empty_tls_key_file", func(t *testing.T) {
		badNode := node
		badNode.TLSKeyFile = "  "
		_, err := postfix.Generate(badNode, postfix.Snapshot{})
		if err == nil || !strings.Contains(err.Error(), postfix.ErrInvalidConfig.Error()) {
			t.Errorf("expected ErrInvalidConfig, got %v", err)
		}
	})
}
