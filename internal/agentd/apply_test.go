package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

func testManager(t *testing.T) *ApplyManager {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	m := &ApplyManager{root: state, targetRoot: filepath.Join(root, "system"), webroot: filepath.Join(root, "acme"), cfg: &config.Config{MyHostname: "box.example.com", PrimaryDomain: "example.com", StorageRoot: filepath.Join(root, "mail"), DatabaseURLDovecot: "host=127.0.0.1 user=dovecot password=secret", ListenAddr: "127.0.0.1:8080", PublicIPv4: "192.0.2.1", Nameservers: []string{"ns.example.com"}}}
	m.run = func(_ context.Context, path string, args ...string) error { return testCommand(m, path, args...) }
	return m
}
func emptyApply() json.RawMessage {
	return json.RawMessage(`{"postfix":{"Domains":[],"Mailboxes":[],"Aliases":[]},"dns":{"Zones":[]},"dkim":{"Domains":[]}}`)
}

func TestConfigApplyRejectsPrivilegedBodies(t *testing.T) {
	m := testManager(t)
	for _, raw := range []string{`{"path":"/etc/urmail/agent.env","content":"LD_PRELOAD=x"}`, `{"postfix":{"Domains":[],"node":{"Hostname":"evil"}}}`, `{"postfix":{"Domains":[{"FQDN":"example.com\nload_module x;","Status":"active"}]}}`, `{} {}`} {
		if _, _, err := m.Apply(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted unsafe request %s", raw)
		}
	}
	registry := NewProductionRegistry(m)
	for _, op := range []string{"file.write", "postconf.set", "systemctl.restart", "postfix.postmap"} {
		response := registry.Execute(Request{Version: ProtocolVersion, Op: op, Params: json.RawMessage(`{}`)})
		if response.OK || response.Error.Code != CodeUnknownOp {
			t.Fatalf("unsafe operation %s exposed", op)
		}
	}
}

func TestConfigApplyChecksReloadsAndDeduplicates(t *testing.T) {
	m := testManager(t)
	var commands []string
	m.run = func(_ context.Context, path string, args ...string) error {
		commands = append(commands, path+" "+strings.Join(args, " "))
		return testCommand(m, path, args...)
	}
	generation, _, err := m.Apply(emptyApply())
	if err != nil {
		t.Fatal(err)
	}
	if !m.status.Ready || generation != m.status.Applied {
		t.Fatalf("status %+v", m.status)
	}
	for _, unit := range ConfigUnits {
		found := false
		for _, c := range commands {
			if c == "/usr/bin/systemctl restart "+unit {
				found = true
			}
		}
		if !found {
			t.Errorf("missing restart %s", unit)
		}
	}
	local, err := os.ReadFile(m.target("/etc/nginx/conf.d/urmail/local.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(local), "server_name box.example.com;") {
		t.Fatal("panel hostname must have an exact HTTP-01 vhost")
	}
	foundDovecot := false
	for _, c := range commands {
		if c == "/usr/bin/doveconf -n" {
			foundDovecot = true
		}
	}
	if !foundDovecot {
		t.Fatal("Ubuntu ships doveconf in /usr/bin")
	}
	foundNSD := false
	for _, c := range commands {
		if c == "/usr/sbin/nsd-checkconf /etc/nsd/nsd.conf" {
			foundNSD = true
		}
	}
	if !foundNSD {
		t.Fatal("NSD checker requires an explicit main config path")
	}
	before := len(commands)
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	if len(commands) != before {
		t.Fatal("unchanged generation restarted services")
	}
	info, err := os.Stat(m.target("/etc/dovecot/conf.d/urmail/db.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("secret mode %o", info.Mode().Perm())
	}
}

func TestConfigApplyRollsBackEveryCommandFailure(t *testing.T) {
	failures := []string{"opendkim -n", "doveconf -n", "postfix check", "nginx -t", "nsd-checkconf", "postmap", "restart opendkim", "restart dovecot", "restart postfix", "restart nginx", "restart nsd", "is-active --quiet nginx"}
	for _, failure := range failures {
		t.Run(failure, func(t *testing.T) {
			m := testManager(t)
			if _, _, err := m.Apply(emptyApply()); err != nil {
				t.Fatal(err)
			}
			path := m.target("/etc/postfix/main.cf")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			m.cfg.PrimaryDomain = "changed.example.com"
			injected := false
			m.run = func(_ context.Context, path string, args ...string) error {
				command := filepath.Base(path) + " " + strings.Join(args, " ")
				if !injected && strings.Contains(command, failure) {
					injected = true
					return errors.New("injected")
				}
				return testCommand(m, path, args...)
			}
			if _, _, err := m.Apply(emptyApply()); err == nil {
				t.Fatal("failure reported success")
			}
			if !injected {
				t.Fatal("failure was not exercised")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("previous configuration was not restored")
			}
			if m.status.Ready || m.status.Degraded {
				t.Fatalf("wrong rollback status %+v", m.status)
			}
			if _, err := os.Stat(filepath.Join(m.root, "pending.json")); !os.IsNotExist(err) {
				t.Fatal("successful rollback retained journal")
			}
			if _, _, err := m.Apply(emptyApply()); err != nil {
				t.Fatalf("retry did not recover: %v", err)
			}
			if !m.status.Ready || m.status.Error != "" {
				t.Fatal("success did not clear error")
			}
		})
	}
}

func TestConfigApplyWriteFailureAndRollbackFailure(t *testing.T) {
	m := testManager(t)
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	m.cfg.PrimaryDomain = "changed.example.com"
	failed := false
	m.fileWriter = func(path string, raw []byte, mode os.FileMode, roots []string) (string, error) {
		if !failed && strings.HasSuffix(path, "/etc/postfix/main.cf") {
			failed = true
			return "", errors.New("disk failure")
		}
		return writeFileAtomic(path, raw, mode, roots)
	}
	if _, _, err := m.Apply(emptyApply()); err == nil {
		t.Fatal("write failure reported success")
	}
	if !failed || m.status.Degraded {
		t.Fatal("write rollback failed")
	}
	m.fileWriter = nil
	m.run = func(_ context.Context, path string, args ...string) error {
		if path == "/usr/bin/systemctl" {
			return errors.New("permanent failure")
		}
		return testCommand(m, path, args...)
	}
	if _, _, err := m.Apply(emptyApply()); err == nil {
		t.Fatal("expected error")
	}
	if !m.status.Degraded {
		t.Fatal("rollback failure must be degraded")
	}
}

func TestConfigApplyRemovesManagedObsoleteFiles(t *testing.T) {
	m := testManager(t)
	old := "/etc/nsd/zones/removed.example.com.txt"
	if err := m.write(old, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	m.paths = []string{old}
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.target(old)); !os.IsNotExist(err) {
		t.Fatal("obsolete managed zone not removed")
	}
}

func TestConfigApplyValidatesSnapshotBeforeWriting(t *testing.T) {
	m := testManager(t)
	p := ApplyParams{Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: "../../evil", Status: "active"}}}}
	raw, _ := json.Marshal(p)
	if _, _, err := m.Apply(raw); err == nil {
		t.Fatal("accepted traversal")
	}
}

func TestConfigApplyRecoversInterruptedJournal(t *testing.T) {
	m := testManager(t)
	path := "/etc/postfix/main.cf"
	if err := m.write(path, []byte("old generation"), 0644); err != nil {
		t.Fatal(err)
	}
	old, err := saveFile(m.target(path))
	if err != nil {
		t.Fatal(err)
	}
	journal := applyJournal{Files: map[string]savedFile{path: old}, Previous: ApplyStatus{Applied: "previous"}, PreviousPaths: []string{path}, PreviousDomains: []string{"example.com"}}
	if err := m.saveJSON("pending.json", journal); err != nil {
		t.Fatal(err)
	}
	if err := m.write(path, []byte("interrupted generation"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.recover(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(m.target(path))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "old generation" || m.status.Ready || m.status.Step != "recovered" || m.status.Applied != "previous" {
		t.Fatalf("bad recovery: %s %+v", raw, m.status)
	}
	if len(m.paths) != 1 || m.paths[0] != path {
		t.Fatal("previous manifest paths not restored")
	}
}

func testCommand(m *ApplyManager, path string, args ...string) error {
	if path == "/usr/sbin/postmap" && len(args) == 1 {
		return os.WriteFile(m.target(args[0]+".db"), []byte("compiled map"), 0644)
	}
	return nil
}

func TestConfigApplyPublishesEveryStagingFailureAndSuccess(t *testing.T) {
	m := testManager(t)
	m.paths = []string{"/etc/opendkim.conf"}
	if _, _, err := m.Apply(emptyApply()); err == nil {
		t.Fatal("unsupported old path accepted")
	}
	raw, _, err := m.Status(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var state ApplyStatus
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	if state.Ready || state.Step != "stage:/etc/opendkim.conf" || state.Error == "" {
		t.Fatalf("stale stage failure %+v", state)
	}
	m.paths = nil
	if _, _, err = m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	raw, _, _ = m.Status(json.RawMessage(`{}`))
	state = ApplyStatus{}
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Ready || state.Step != "ready" || state.Error != "" {
		t.Fatalf("success was not published %+v", state)
	}
}
