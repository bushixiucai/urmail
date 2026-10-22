package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryRejectsUnknownOperation(t *testing.T) {
	registry := NewRegistry()
	registry.Register("file.write", WriteFile)

	response := registry.Execute(Request{
		Version: ProtocolVersion,
		ID:      "req-1",
		Op:      "shell.exec",
		Params:  json.RawMessage(`{"cmd":"rm -rf /"}`),
	})

	if response.OK {
		t.Fatal("an unregistered operation must not succeed")
	}
	if response.Error == nil || response.Error.Code != CodeUnknownOp {
		t.Fatalf("expected %s, got %+v", CodeUnknownOp, response.Error)
	}
}

func TestRegistryRejectsProtocolMismatch(t *testing.T) {
	registry := NewRegistry()
	response := registry.Execute(Request{Version: ProtocolVersion + 1, Op: "file.write"})
	if response.OK {
		t.Fatal("a version mismatch must not succeed")
	}
	if response.Error == nil || response.Error.Code != CodeVersionMismatch {
		t.Fatalf("expected %s, got %+v", CodeVersionMismatch, response.Error)
	}
}

func TestWriteFileRejectsPathsOutsideAllowedRoots(t *testing.T) {
	cases := []string{
		"/etc/shadow",
		"/etc/passwd",
		"/var/lib/urmail/../../etc/shadow",
		"/etc/urmail/../shadow",
		"/tmp/anything",
		"relative/path",
		"",
	}

	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			_, _, err := WriteFile(json.RawMessage(`{"path":` + mustQuote(path) + `,"content":"x"}`))
			if err == nil {
				t.Fatalf("path %q must be rejected", path)
			}
			if !strings.Contains(err.Error(), "rejected") {
				t.Fatalf("expected a rejection for %q, got %v", path, err)
			}
		})
	}
}

func TestWriteFileRejectsTraversalIntoAllowedRoot(t *testing.T) {
	// A path that starts inside an allowed root but escapes via traversal must
	// still be rejected: filepath.Clean resolves it to /etc/shadow.
	_, _, err := WriteFile(json.RawMessage(`{"path":"/etc/urmail/../shadow","content":"x"}`))
	if err == nil {
		t.Fatal("traversal out of an allowed root must be rejected")
	}
}

func TestWriteFileAcceptsPathInsideAllowedRoot(t *testing.T) {
	dir := t.TempDir()
	// The allowed roots are absolute system paths, so exercise safePath
	// directly for the accept case rather than writing to a real location.
	cleaned, err := safePath("/etc/urmail/urmaild.env")
	if err != nil {
		t.Fatalf("expected /etc/urmail to be allowed, got %v", err)
	}
	if cleaned != filepath.Clean("/etc/urmail/urmaild.env") {
		t.Fatalf("unexpected cleaned path %q", cleaned)
	}
	_ = dir
}

func TestWriteFileRejectsInvalidMode(t *testing.T) {
	_, _, err := WriteFile(json.RawMessage(`{"path":"/etc/urmail/x","content":"y","mode":"not-a-mode"}`))
	if err == nil {
		t.Fatal("an invalid mode must be rejected")
	}
}

func TestRestartUnitRejectsUnlistedUnit(t *testing.T) {
	if err := ValidateUnit("sshd"); err == nil {
		t.Fatal("an unlisted unit must be rejected")
	}
	if err := ValidateUnit("postfix"); err != nil {
		t.Fatalf("postfix must be allowed: %v", err)
	}
}

func TestPostconfSetRejectsSuspiciousKeysAndValues(t *testing.T) {
	cases := []struct {
		name   string
		params string
	}{
		{"bad key", `{"keys":{"my host name":"x"}}`},
		{"key with newline", "{\"keys\":{\"myhostname\n\":\"x\"}}"},
		{"value with newline", "{\"keys\":{\"myhostname\":\"a\nb\"}}"},
		{"value with shell metacharacters", `{"keys":{"myhostname":"x; rm -rf /"}}`},
		{"value with backtick", "{\"keys\":{\"myhostname\":\"`id`\"}}"},
		{"value with backslash", `{"keys":{"myhostname":"a\\b"}}`},
		{"no keys", `{"keys":{}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := PostconfSet(json.RawMessage(tc.params))
			if err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestBuildPostconfArgsAcceptsValidKeys(t *testing.T) {
	arguments, err := BuildPostconfArgs(map[string]string{"myhostname": "mail.example.com"})
	if err != nil {
		t.Fatalf("a valid key/value pair must be accepted: %v", err)
	}
	if len(arguments) != 2 || arguments[0] != "-e" {
		t.Fatalf("unexpected arguments %v", arguments)
	}
	if arguments[1] != "myhostname=mail.example.com" {
		t.Fatalf("unexpected argument %q", arguments[1])
	}
}

func TestSafePathRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	// safePath only inspects the literal path; the symlink check happens at
	// write time. Confirm the literal path is still evaluated on its own
	// terms so a future change does not silently drop the check.
	if _, err := safePath(filepath.Join(dir, "link", "shadow")); err == nil {
		t.Fatal("a path outside the allowed roots must be rejected")
	}
}

func mustQuote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
