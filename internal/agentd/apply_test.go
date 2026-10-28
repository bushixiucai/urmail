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
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
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
	m.readinessProbe = func(context.Context) error { return nil }
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
	// OPS-008: the newer HTTP services (autoconfig, CardDAV/CalDAV well-known
	// and /api/v1 endpoints, EAS) ride this vhost: the whole host is proxied
	// to urmaild, so they need no generated configuration of their own.
	if !strings.Contains(string(local), "location / {") ||
		!strings.Contains(string(local), "proxy_pass http://127.0.0.1:8080;") {
		t.Fatal("panel vhost must proxy the whole host to urmaild so autoconfig, DAV and EAS ride it")
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
	failures := []string{"sievec", "opendkim -n", "doveconf -n", "postfix check", "nginx -t", "nsd-checkconf", "postmap", "restart opendkim", "restart dovecot", "restart postfix", "restart nginx", "restart nsd", "is-active --quiet nginx"}
	for _, failure := range failures {
		t.Run(failure, func(t *testing.T) {
			m := testManager(t)
			if _, _, err := m.Apply(emptyApply()); err != nil {
				t.Fatal(err)
			}
			oldApplied := m.status.Applied
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
			// OPS-008: a failed apply must never advance the applied
			// generation, even though the desired generation moved.
			if m.status.Applied != oldApplied {
				t.Fatalf("applied generation advanced falsely: %q != %q", m.status.Applied, oldApplied)
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

func TestConfigApplyMidWriteFailureRestoresWholeTree(t *testing.T) {
	m := testManager(t)
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	knownGood := snapshotTree(t, m.targetRoot)
	goodGeneration := m.status.Applied

	// A changed snapshot differs in several rendered files. The injected
	// disk failure hits the second file, leaving a half-applied generation
	// on disk that only the journal can undo.
	m.cfg.PrimaryDomain = "changed.example.com"
	writes := 0
	m.fileWriter = func(path string, raw []byte, mode os.FileMode, roots []string) (string, error) {
		writes++
		if writes == 2 {
			return "", errors.New("injected disk failure on second file")
		}
		return writeFileAtomic(path, raw, mode, roots)
	}
	if _, _, err := m.Apply(emptyApply()); err == nil {
		t.Fatal("mid-write failure reported success")
	}
	if writes < 2 {
		t.Fatalf("injection not exercised: %d writes", writes)
	}
	after := snapshotTree(t, m.targetRoot)
	for path, content := range knownGood {
		if after[path] != content {
			t.Fatalf("file %s was not restored to the known-good generation", path)
		}
	}
	if len(after) != len(knownGood) {
		t.Fatalf("tree file count changed: %d != %d", len(after), len(knownGood))
	}
	if m.status.Applied != goodGeneration || m.status.Ready || m.status.Degraded {
		t.Fatalf("false generation state after rollback: %+v", m.status)
	}
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); !os.IsNotExist(err) {
		t.Fatal("successful rollback retained journal")
	}
	// The agent must converge again on the next apply.
	m.fileWriter = nil
	if _, _, err := m.Apply(emptyApply()); err != nil {
		t.Fatalf("retry after mid-write failure failed: %v", err)
	}
	if !m.status.Ready || m.status.Applied == "" {
		t.Fatalf("retry did not publish a ready generation: %+v", m.status)
	}
}

// snapshotTree records every regular file below root keyed by its path below
// the system target root.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary tree.
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func testCommand(m *ApplyManager, path string, args ...string) error {
	if path == "/usr/bin/sievec" && len(args) == 6 && args[0] == "-P" && args[1] == "sieve_extprograms" && args[2] == "-x" && args[3] == "+vnd.dovecot.execute" {
		args = args[4:]
		source, err := os.ReadFile(m.target(args[0]))
		if err != nil {
			return err
		}
		return os.WriteFile(m.target(args[1]), append([]byte("compiled sieve: "), source...), 0640)
	}
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

func TestPrivateSpamPolicyIsBoundedAndChangesGeneration(t *testing.T) {
	m := testManager(t)
	p := ApplyParams{Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: "example.com", Status: "active"}}, Mailboxes: []postfix.Mailbox{{Address: "alice@example.com", Status: "active"}}}}
	beforeRaw, _ := json.Marshal(p)
	before, _, err := m.Apply(beforeRaw)
	if err != nil {
		t.Fatal(err)
	}
	p.SpamPolicies = []dovecot.SpamPolicy{{MailboxAddress: "alice@example.com", Scope: "mailbox", SenderAddress: "sender@vendor.test", List: "deny"}}
	raw, _ := json.Marshal(p)
	after, _, err := m.Apply(raw)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("recipient policy did not change applied generation")
	}
	path := m.target("/etc/dovecot/urmail-spam/spam-before.sieve")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("private policy is world-readable: %o", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0750 {
		t.Fatalf("private directory mode: %o", info.Mode().Perm())
	}
	p.SpamPolicies[0].MailboxAddress = "other@another.test"
	raw, _ = json.Marshal(p)
	if _, _, err = m.Apply(raw); err == nil {
		t.Fatal("policy outside active mailbox snapshot accepted")
	}
}

func TestConfigApplyRepairsMissingCompiledSieveWithoutSourceChange(t *testing.T) {
	m := testManager(t)
	before, _, err := m.Apply(emptyApply())
	if err != nil {
		t.Fatal(err)
	}
	path := m.target("/etc/dovecot/urmail-spam/spam-before.svbin")
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	after, _, err := m.Apply(emptyApply())
	if err != nil {
		t.Fatal(err)
	}
	if after != before || !m.status.Ready || !m.spamBinaryCurrent() {
		t.Fatal("same-generation apply failed to repair its compiled Sieve artifact")
	}
}

func TestWorkerHoldKeepsConfigurationReadyWithoutAutomaticACME(t *testing.T) {
	m := testManager(t)
	m.cfg.WorkersHeld = true
	m.automaticCertificates = true
	m.run = func(_ context.Context, path string, args ...string) error {
		if path == "/usr/bin/certbot" {
			t.Error("maintenance configuration started ACME")
			return errors.New("unexpected ACME")
		}
		return testCommand(m, path, args...)
	}
	generation, _, err := m.Apply(emptyApply())
	if err != nil {
		t.Fatal(err)
	}
	if generation == "" || !m.status.Ready || m.certificatePending.Load() {
		t.Fatalf("config unavailable or external certificate worker started: %+v", m.status)
	}
	if _, err := os.Stat(m.target("/etc/postfix/main.cf")); err != nil {
		t.Fatal("maintenance removed mail configuration", err)
	}
}
