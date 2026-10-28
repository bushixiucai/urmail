package agentd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestDNSOnlyRegistryCannotPerformMailOrGeneralSystemOperations(t *testing.T) {
	manager := &ApplyManager{cfg: &config.Config{DNSSecondaryOnly: true}, root: t.TempDir()}
	registry := NewProductionRegistry(manager)
	for _, op := range []string{OpConfigApply, OpSieveActivate, OpCertificateIssue, OpMailboxExpunge, OpBackupLocalCreate, "file.write", "service.restart", "shell.exec"} {
		response := registry.Execute(Request{Version: ProtocolVersion, Op: op, Params: json.RawMessage(`{}`)})
		if response.OK || response.Error == nil || response.Error.Code != CodeUnknownOp {
			t.Fatalf("secondary accepted %s: %+v", op, response)
		}
	}
	if !registry.Has(OpDNSAuthorityManage) || !registry.Has(OpDNSAuthorityStatus) {
		t.Fatal("DNS-only contract unavailable")
	}
	for _, raw := range []string{`{"domain":"../example.test","action":"secondary-install"}`, `{"domain":"example.test","action":"prepare"}`, `{"domain":"example.test","action":"secondary-install","path":"/etc/shadow"}`} {
		if _, _, err := manager.ManageDNSAuthority(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted unsafe request %s", raw)
		}
	}
}
func TestDNSStateIsPrivateAndBackupManifestContainsHashesOnly(t *testing.T) {
	manager := &ApplyManager{root: t.TempDir()}
	state := dnsKeyState{Phase: "prepared", TSIGName: "transfer-one", TSIGSecret: "secret-never-returned"}
	if err := manager.saveDNSState("example.test", state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(manager.dnsDirectory("example.test"), "state.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key state must be private", err)
	}
	manifest, _, err := manager.dnsBackupManifest("example.test", state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(manifest, state.TSIGSecret) || !strings.Contains(manifest, "state.json") {
		t.Fatal("backup manifest leaked a secret or omitted key state")
	}
}
func TestSecondaryRuntimeRejectsInjectionAndUsesAuthenticatedTransfers(t *testing.T) {
	valid := DNSSecondaryBundle{Version: 1, Domain: "example.test", Hostname: "ns2.example.test", Address: "192.0.2.2", PrimaryAddress: "192.0.2.1", KeyName: "transfer-one", Secret: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	result, err := renderSecondaryRuntime([]string{"192.0.2.2"}, map[string]DNSSecondaryBundle{valid.Domain: valid})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "request-xfr: AXFR 192.0.2.1 transfer-one") || strings.Contains(result, "NOKEY") {
		t.Fatal("unauthenticated transfer contract")
	}
	for _, mutate := range []func(*DNSSecondaryBundle){func(b *DNSSecondaryBundle) { b.Domain = "../zone" }, func(b *DNSSecondaryBundle) { b.PrimaryAddress = "192.0.2.1\nprovide-xfr: 0.0.0.0/0 NOKEY" }, func(b *DNSSecondaryBundle) { b.KeyName = "x; shell" }, func(b *DNSSecondaryBundle) { b.Secret = "short" }} {
		bad := valid
		mutate(&bad)
		if _, err = renderSecondaryRuntime([]string{"192.0.2.2"}, map[string]DNSSecondaryBundle{bad.Domain: bad}); err == nil {
			t.Fatal("invalid transfer fields accepted")
		}
	}
}
func TestMaintenanceHoldBlocksDNSMutationBeforeAnyToolOrFilesystemChange(t *testing.T) {
	called := false
	manager := &ApplyManager{cfg: &config.Config{WorkersHeld: true}, root: t.TempDir(), output: func(context.Context, ...string) ([]byte, error) { called = true; return nil, nil }}
	if _, _, err := manager.ManageDNSAuthority(json.RawMessage(`{"domain":"example.test","action":"prepare"}`)); err == nil {
		t.Fatal("held key mutation accepted")
	}
	if called {
		t.Fatal("held request executed a tool")
	}
	entries, err := os.ReadDir(manager.root)
	if err != nil || len(entries) != 0 {
		t.Fatal("held request wrote state")
	}
}

func TestWorkerHoldPreservesDNSBytesManifestAndDoesNotReloadNSD(t *testing.T) {
	manager := testManager(t)
	if _, _, err := manager.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	path := "/etc/nsd/nsd.conf.d/zones.conf"
	before, err := os.ReadFile(manager.target(path))
	if err != nil {
		t.Fatal(err)
	}
	manager.cfg.WorkersHeld = true
	var commands []string
	manager.run = func(_ context.Context, path string, args ...string) error {
		commands = append(commands, path+" "+strings.Join(args, " "))
		return testCommand(manager, path, args...)
	}
	if _, _, err = manager.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(manager.target(path))
	if err != nil || string(after) != string(before) {
		t.Fatal("held reconcile changed authoritative DNS bytes", err)
	}
	found := false
	for _, tracked := range manager.paths {
		if tracked == path {
			found = true
		}
	}
	if !found {
		t.Fatal("transient hold lost authoritative path ownership")
	}
	for _, command := range commands {
		if strings.Contains(command, "restart nsd") || strings.Contains(command, "dnssec-") || strings.Contains(command, "nsd-check") {
			t.Fatal("held reconcile mutated DNS", command)
		}
	}
	manager.cfg.WorkersHeld = false
	if _, _, err = manager.Apply(emptyApply()); err != nil {
		t.Fatal(err)
	}
}

func TestDNSSECWireFormattingAndExpiry(t *testing.T) {
	digest := strings.Repeat("A", 64)
	for _, line := range []string{"example.test. 60 IN DS 123 13 2 " + digest, "example.test. 60 IN DS 123 13 2 " + digest[:56] + " " + digest[56:], "123 13 2 " + digest} {
		if got := dsValue(line); got != "123 13 2 "+digest {
			t.Fatalf("split digest lost: %q", got)
		}
	}
	if dsValue("123 13 2 "+strings.Repeat("A", 40)) != "" {
		t.Fatal("wrong digest length accepted")
	}
	file := filepath.Join(t.TempDir(), "zone.signed")
	raw := "; comment with RRSIG is not a record\nexample.test. 60 IN RRSIG SOA 13 2 60 (\n  20271101000000 20261028000000 123 example.test.\n abc )\n 60 RRSIG NS 13 2 60 20271001000000 20261028000000 123 example.test. abc\n"
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if got := signatureExpiry(file); got.Format("20060102150405") != "20271001000000" {
		t.Fatalf("incorrect earliest multiline expiry %s", got)
	}
}

func TestRetirementWaitUsesCurrentParentTTL(t *testing.T) {
	manager := &ApplyManager{root: t.TempDir(), targetRoot: t.TempDir()}
	domain := "example.test"
	key := "Kexample.test.+013+00123"
	directory := manager.dnsDirectory(domain)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, key+".key"), []byte("example.test. IN DNSKEY 257 3 13 public"), 0600); err != nil {
		t.Fatal(err)
	}
	signed := manager.target("/etc/nsd/zones/example.test.signed")
	if err := os.MkdirAll(filepath.Dir(signed), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signed, []byte("example.test. 60 IN RRSIG SOA 13 2 60 20271101000000 20261028000000 123 example.test. abc"), 0644); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("A", 64)
	manager.output = func(_ context.Context, args ...string) ([]byte, error) {
		switch filepath.Base(args[0]) {
		case "dnssec-dsfromkey":
			return []byte("example.test. IN DS 123 13 2 " + digest), nil
		case "dig":
			if strings.HasPrefix(args[1], "@") {
				return []byte(";; status: NOERROR\n;; flags: qr aa;\nexample.test. 604800 IN DS 123 13 2 " + digest), nil
			}
			return []byte("ns.parent.test.\n"), nil
		case "delv":
			return []byte("; fully validated\n"), nil
		default:
			return nil, nil
		}
	}
	published := time.Now().UTC().Add(-3 * 24 * time.Hour)
	state := dnsKeyState{Current: dnsKeyGeneration{Keys: []string{key}}, PublishedAt: published, ParentDSTTL: 3600}
	observation, err := manager.observeDNS(t.Context(), domain, state)
	if err != nil || !observation.Ready {
		t.Fatalf("live-method fixture did not validate %+v %v", observation, err)
	}
	if !time.Now().Before(observation.RolloverNotBefore) || observation.ParentDSTTL != 604800 {
		t.Fatalf("newly observed higher parent TTL was ignored %+v", observation)
	}
}
