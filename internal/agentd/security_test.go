package agentd

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWriteFileAtomicRejectsParentSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := writeFileAtomic(filepath.Join(root, "escape", "owned"), []byte("pwn"), 0o644, []string{root})
	if err == nil {
		t.Fatal("a write through an intermediate symlink must be rejected")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected a rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "owned")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the symlink target must not be written, stat err=%v", statErr)
	}
}

func TestWriteFileAtomicReplacesFinalSymlinkWithoutFollowing(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	link := filepath.Join(root, "conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	written, err := writeFileAtomic(link, []byte("replacement"), 0o600, []string{root})
	if err != nil {
		t.Fatalf("write through final symlink: %v", err)
	}
	if written != link {
		t.Fatalf("unexpected path %q", written)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if string(content) != "keep" {
		t.Fatalf("the symlink target must be untouched, got %q", content)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat destination: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the destination symlink must have been replaced by a regular file")
	}
	content, err = os.ReadFile(link)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(content) != "replacement" {
		t.Fatalf("unexpected destination content %q", content)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected destination mode 0600, got %04o", perm)
	}
}

func TestWriteFileAtomicReplacesAtomicallyAndPreservesMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "file.conf")

	if _, err := writeFileAtomic(path, []byte("first"), 0o640, []string{root}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := writeFileAtomic(path, []byte("second"), 0o600, []string{root}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != "second" {
		t.Fatalf("unexpected content %q", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected mode 0600, got %04o", perm)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "file.conf" {
		t.Fatalf("atomic write left temporary files behind: %v", entries)
	}
}

func TestWriteFileRejectsSetuidSetgidStickyModes(t *testing.T) {
	for _, mode := range []string{"4755", "2755", "1755", "07777"} {
		params := `{"path":"/etc/urmail/x","content":"y","mode":"` + mode + `"}`
		_, _, err := WriteFile(json.RawMessage(params))
		if err == nil {
			t.Fatalf("mode %q must be rejected", mode)
		}
		if !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("mode %q: expected a rejection, got %v", mode, err)
		}
	}
}

func TestParseOctalRejectsSetuidSetgidSticky(t *testing.T) {
	for _, value := range []string{"07777", "4755", "2755", "1755", "7777", "1000", "-1", "888", "0x1ff", ""} {
		if _, err := parseOctal(value); err == nil {
			t.Fatalf("mode %q must be rejected", value)
		}
	}

	for value, want := range map[string]os.FileMode{
		"0000": 0,
		"0600": 0o600,
		"0644": 0o644,
		"0777": 0o777,
		"644":  0o644,
	} {
		got, err := parseOctal(value)
		if err != nil {
			t.Fatalf("mode %q must be accepted: %v", value, err)
		}
		if got != want {
			t.Fatalf("mode %q: expected %04o, got %04o", value, want, got)
		}
	}
}

func TestValidatePostmapPathAllowlist(t *testing.T) {
	valid := []string{
		"/etc/postfix/urmail/virtual_mailbox.map",
		"/etc/postfix/urmail/virtual_alias_maps.map",
		"/etc/postfix/urmail/aliases.map",
	}
	for _, path := range valid {
		if _, err := validatePostmapPath(path, postmapRoot); err != nil {
			t.Fatalf("path %q must be allowed: %v", path, err)
		}
	}

	invalid := []string{
		"",
		"relative/x.map",
		"/tmp/x.map",
		"/etc/postfix/urmail/../urmail/x.map",
		"/etc/postfix/urmail/nested/x.map",
		"/etc/postfix/urmail/x.txt",
		"/etc/postfix/urmail/.map",
		"/etc/postfix/urmail/x.map.db",
		"/etc/postfix/urmail/x.map/",
		"/etc/postfix/urmail//x.map",
		"/etc/postfix/x.map",
		"/etc/postfix/urmail/-x.map",
	}
	for _, path := range invalid {
		if _, err := validatePostmapPath(path, postmapRoot); err == nil {
			t.Fatalf("path %q must be rejected", path)
		}
	}
}

func TestResolvePostmapFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	outsideFile := filepath.Join(outside, "evil.map")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "evil.map")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if _, err := resolvePostmapFile(filepath.Join(root, "evil.map"), root); err == nil {
		t.Fatal("a symlinked map must be rejected")
	} else if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected a rejection, got %v", err)
	}

	good := filepath.Join(root, "good.map")
	if err := os.WriteFile(good, []byte("x"), 0o644); err != nil {
		t.Fatalf("write good map: %v", err)
	}
	resolved, err := resolvePostmapFile(good, root)
	if err != nil {
		t.Fatalf("a regular map must be accepted: %v", err)
	}
	want, err := filepath.EvalSymlinks(good)
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}
	if resolved != want {
		t.Fatalf("unexpected resolved path %q, want %q", resolved, want)
	}
}

func TestPostmapRejectsDisallowedPathBeforeExecuting(t *testing.T) {
	_, _, err := Postmap(json.RawMessage(`{"path":"/tmp/evil.map"}`))
	if err == nil {
		t.Fatal("an outside path must be rejected")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected a rejection, got %v", err)
	}
}

func TestPeerPolicyAllowsOnlyConfiguredUIDs(t *testing.T) {
	policy := newPeerPolicy(1000, 2000)
	if !policy.allows(1000) || !policy.allows(2000) {
		t.Fatal("configured UIDs must be allowed")
	}
	for _, uid := range []uint32{0, 1, 999, 1001, 65534} {
		if policy.allows(uid) {
			t.Fatalf("uid %d must be rejected", uid)
		}
	}

	var nilPolicy *peerPolicy
	if nilPolicy.allows(1000) {
		t.Fatal("a nil policy must reject every peer")
	}
}

func TestLoadPeerPolicyAlwaysAllowsCurrentProcess(t *testing.T) {
	policy, _ := loadPeerPolicy()
	if !policy.allows(uint32(os.Geteuid())) {
		t.Fatal("the current process UID must always be allowed")
	}

	// When the mailinabox account exists it must also be allowed. This is the
	// production case; on a developer machine the lookup normally fails and
	// the policy above is all we can assert.
	account, err := user.Lookup("mailinabox")
	if err != nil {
		t.Logf("mailinabox account not present, skipping production UID assertion: %v", err)
		return
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		t.Fatalf("parse mailinabox uid: %v", err)
	}
	if !policy.allows(uint32(uid)) {
		t.Fatalf("the mailinabox uid %d must be allowed", uid)
	}
}

func TestReadRequestLineRejectsOversizedLine(t *testing.T) {
	newline := string(byte(10))
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("a", 100)+newline), 16)

	_, err := readRequestLine(reader, 64)
	if !errors.Is(err, errRequestTooLarge) {
		t.Fatalf("expected errRequestTooLarge, got %v", err)
	}
}

func TestReadRequestLineAcceptsBoundedLine(t *testing.T) {
	newline := string(byte(10))
	payload := `{"x":1}`
	reader := bufio.NewReaderSize(strings.NewReader(payload+newline), 16)

	line, err := readRequestLine(reader, 64)
	if err != nil {
		t.Fatalf("read request line: %v", err)
	}
	if string(line) != payload+newline {
		t.Fatalf("unexpected line %q", line)
	}
}
